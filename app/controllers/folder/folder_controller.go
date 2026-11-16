// Package folder: list, create, rename/move and delete folders.
package folder

import (
	"net/http"
	"strconv"
	"strings"

	"telegram-drive/app"
	"telegram-drive/app/models"
	"telegram-drive/app/utils"
)

type Controller struct{ *app.App }

func (c Controller) List(w http.ResponseWriter, r *http.Request, uid int64) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("folder_id"), 10, 64)
	ref, err := models.FolderRef(c.DB, uid, id)
	if err != nil {
		utils.WriteErr(w, http.StatusNotFound, err.Error())
		return
	}
	type crumb struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	out := struct {
		Path    []crumb         `json:"path"` // root first, current folder last
		Folders []models.Folder `json:"folders"`
		Files   []models.File   `json:"files"`
	}{[]crumb{}, []models.Folder{}, []models.File{}}

	rows, err := c.DB.Query(`WITH RECURSIVE up(id, parent_id, name, depth) AS (
			SELECT id, parent_id, name, 0 FROM folders WHERE id = ?
			UNION ALL SELECT f.id, f.parent_id, f.name, depth + 1 FROM folders f JOIN up ON f.id = up.parent_id
		) SELECT id, name FROM up ORDER BY depth DESC`, ref)
	if err != nil {
		utils.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for rows.Next() {
		var c crumb
		rows.Scan(&c.ID, &c.Name)
		out.Path = append(out.Path, c)
	}
	rows.Close()

	if out.Folders, err = models.QueryFolders(c.DB, `SELECT id, parent_id, name, created_at FROM folders
		WHERE user_id = ? AND parent_id IS ? ORDER BY name COLLATE NOCASE`, uid, ref); err != nil {
		utils.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out.Files, err = models.QueryFiles(c.DB, `SELECT `+models.FileColumns+` FROM files
		WHERE user_id = ? AND folder_id IS ? ORDER BY name COLLATE NOCASE`, uid, ref); err != nil {
		utils.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	utils.WriteJSON(w, http.StatusOK, out)
}

// GET /api/search?q= — folders and files whose name contains q, across the whole drive.
func (c Controller) Search(w http.ResponseWriter, r *http.Request, uid int64) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	out := struct {
		Folders []models.Folder `json:"folders"`
		Files   []models.File   `json:"files"`
	}{[]models.Folder{}, []models.File{}}
	if q == "" {
		utils.WriteJSON(w, http.StatusOK, out)
		return
	}
	like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
	var err error
	if out.Folders, err = models.QueryFolders(c.DB, `SELECT id, parent_id, name, created_at FROM folders
		WHERE user_id = ? AND name LIKE ? ESCAPE '\' ORDER BY name COLLATE NOCASE LIMIT 50`, uid, like); err != nil {
		utils.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out.Files, err = models.QueryFiles(c.DB, `SELECT `+models.FileColumns+` FROM files
		WHERE user_id = ? AND name LIKE ? ESCAPE '\' ORDER BY created_at DESC LIMIT 200`, uid, like); err != nil {
		utils.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	utils.WriteJSON(w, http.StatusOK, out)
}

func (c Controller) Create(w http.ResponseWriter, r *http.Request, uid int64) {
	var req struct {
		Name     string `json:"name"`
		ParentID int64  `json:"parent_id"`
	}
	if !utils.ReadJSON(w, r, &req) {
		return
	}
	name, ok := utils.CleanName(req.Name)
	if !ok {
		utils.WriteErr(w, http.StatusBadRequest, "name must be 1-255 chars")
		return
	}
	ref, err := models.FolderRef(c.DB, uid, req.ParentID)
	if err != nil {
		utils.WriteErr(w, http.StatusNotFound, err.Error())
		return
	}
	res, err := c.DB.Exec(`INSERT INTO folders (user_id, parent_id, name) VALUES (?, ?, ?)`, uid, ref, name)
	if err != nil {
		utils.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	utils.WriteJSON(w, http.StatusCreated, map[string]any{"id": id, "parent_id": ref, "name": name})
}

// rename and/or move. parent_id 0 moves to root; omit a field to leave it unchanged.
func (c Controller) Update(w http.ResponseWriter, r *http.Request, uid int64) {
	id, ok := utils.PathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name     *string `json:"name"`
		ParentID *int64  `json:"parent_id"`
	}
	if !utils.ReadJSON(w, r, &req) {
		return
	}
	if _, err := models.FolderRef(c.DB, uid, id); err != nil {
		utils.WriteErr(w, http.StatusNotFound, err.Error())
		return
	}
	if req.Name != nil {
		name, ok := utils.CleanName(*req.Name)
		if !ok {
			utils.WriteErr(w, http.StatusBadRequest, "name must be 1-255 chars")
			return
		}
		c.DB.Exec(`UPDATE folders SET name = ? WHERE id = ?`, name, id)
	}
	if req.ParentID != nil {
		ref, err := models.FolderRef(c.DB, uid, *req.ParentID)
		if err != nil {
			utils.WriteErr(w, http.StatusNotFound, err.Error())
			return
		}
		// Refuse moving a folder into itself or one of its own subfolders.
		var cycle int
		c.DB.QueryRow(`WITH RECURSIVE up(id) AS (
				SELECT ? UNION SELECT parent_id FROM folders JOIN up ON folders.id = up.id WHERE parent_id IS NOT NULL
			) SELECT count(*) FROM up WHERE id = ?`, ref, id).Scan(&cycle)
		if cycle > 0 {
			utils.WriteErr(w, http.StatusBadRequest, "cannot move a folder into itself")
			return
		}
		c.DB.Exec(`UPDATE folders SET parent_id = ? WHERE id = ?`, ref, id)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c Controller) Delete(w http.ResponseWriter, r *http.Request, uid int64) {
	id, ok := utils.PathID(w, r)
	if !ok {
		return
	}
	// ponytail: only empty folders; recursive delete would also need to delete every Telegram message inside.
	res, err := c.DB.Exec(`DELETE FROM folders WHERE id = ? AND user_id = ?`, id, uid)
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			utils.WriteErr(w, http.StatusConflict, "folder not empty")
			return
		}
		utils.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		utils.WriteErr(w, http.StatusNotFound, models.ErrFolderNotFound.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
