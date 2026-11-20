// Package folder: list, create and rename/move folders. Deleting goes through the trash (package file).
package folder

import (
	"fmt"
	"net/http"
	"strings"

	"telegram-drive/app"
	"telegram-drive/app/models"
	"telegram-drive/app/utils"
)

type Controller struct{ *app.App }

// GET /api/list?folder_id= — a folder's contents. "" is the caller's own root. A folder shared
// with the caller (or inside one) works too; its path then starts at the highest folder they
// can see, and "shared" tells the UI to call that root "Shared with me".
func (c Controller) List(w http.ResponseWriter, r *http.Request, uid string) {
	id := r.URL.Query().Get("folder_id")
	ref, err := models.FolderRef(r.Context(), c.DB, uid, id, models.Viewer)
	if err != nil {
		utils.WriteErr(w, http.StatusNotFound, err.Error())
		return
	}
	guestItem := models.GuestUID // a guest sees the path from the linked folder down
	if g := models.GuestFrom(r.Context()); g != nil {
		guestItem = g.ItemID
	}
	role := models.Owner
	if id != "" {
		role, _ = models.Access(r.Context(), c.DB, uid, "folder", id)
	}
	type crumb struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	out := struct {
		Path    []crumb         `json:"path"` // root first, current folder last
		Folders []models.Folder `json:"folders"`
		Files   []models.File   `json:"files"`
		Role    string          `json:"role"`   // owner, editor, commenter or viewer
		Shared  bool            `json:"shared"` // somebody else's folder, reached through a share
	}{[]crumb{}, []models.Folder{}, []models.File{}, role.String(), false}

	rows, err := c.DB.Query(`WITH RECURSIVE up(id, parent_id, name, user_id, depth) AS (
			SELECT id, parent_id, name, user_id, 0 FROM folders WHERE id = $1
			UNION ALL SELECT f.id, f.parent_id, f.name, f.user_id, depth + 1 FROM folders f JOIN up ON f.id = up.parent_id
		) SELECT id, name, user_id = $2 OR id = $3 OR EXISTS (SELECT 1 FROM shares s WHERE s.item_id = up.id AND s.user_id = $2)
		FROM up ORDER BY depth DESC`, ref, uid, guestItem)
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	var visible []bool
	for rows.Next() {
		var c crumb
		var v bool
		rows.Scan(&c.ID, &c.Name, &v)
		out.Path = append(out.Path, c)
		visible = append(visible, v)
	}
	rows.Close()
	// Folders above the highest one the caller owns or was given stay hidden.
	for i, v := range visible {
		if v {
			out.Path = out.Path[i:]
			break
		}
	}
	if len(out.Path) > 0 {
		var top string
		c.DB.QueryRow(`SELECT user_id FROM folders WHERE id = $1`, out.Path[0].ID).Scan(&top)
		out.Shared = top != uid
	}

	// The root lists the caller's own items; a folder lists everything in it, whoever added it.
	where := `trashed_at IS NULL AND (($2::uuid IS NULL AND user_id = $1 AND %s IS NULL) OR %s = $2::uuid) ORDER BY lower(name), name`
	if out.Folders, err = models.QueryFolders(c.DB, `SELECT `+models.FolderColumns+` FROM folders WHERE `+
		fmt.Sprintf(where, "parent_id", "parent_id"), uid, ref); err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	if out.Files, err = models.QueryFiles(c.DB, `SELECT `+models.FileColumns+` FROM files WHERE `+
		fmt.Sprintf(where, "folder_id", "folder_id"), uid, ref); err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	utils.WriteJSON(w, http.StatusOK, out)
}

// GET /api/search?q= — folders and files whose name contains q, across the whole drive.
func (c Controller) Search(w http.ResponseWriter, r *http.Request, uid string) {
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
	if out.Folders, err = models.QueryFolders(c.DB, `SELECT `+models.FolderColumns+` FROM folders
		WHERE user_id = $1 AND trashed_at IS NULL AND name ILIKE $2 ESCAPE '\' ORDER BY lower(name), name LIMIT 50`, uid, like); err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	if out.Files, err = models.QueryFiles(c.DB, `SELECT `+models.FileColumns+` FROM files
		WHERE user_id = $1 AND trashed_at IS NULL AND name ILIKE $2 ESCAPE '\' ORDER BY created_at DESC LIMIT 200`, uid, like); err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	utils.WriteJSON(w, http.StatusOK, out)
}

func (c Controller) Create(w http.ResponseWriter, r *http.Request, uid string) {
	var req struct {
		Name     string `json:"name"`
		ParentID string `json:"parent_id"` // "" = root
	}
	if !utils.ReadJSON(w, r, &req) {
		return
	}
	name, ok := utils.CleanName(req.Name)
	if !ok {
		utils.WriteErr(w, http.StatusBadRequest, "name must be 1-255 chars")
		return
	}
	ref, err := models.FolderRef(r.Context(), c.DB, uid, req.ParentID, models.Editor)
	if err != nil {
		utils.WriteErr(w, http.StatusNotFound, err.Error())
		return
	}
	owner, err := models.OwnerFor(r.Context(), c.DB, uid, ref)
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	id := models.NewID()
	if _, err := c.DB.Exec(`INSERT INTO folders (id, user_id, parent_id, name, guest_name) VALUES ($4, $1, $2, $3, $5)`,
		owner, ref, name, id, models.GuestName(r.Context())); err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	utils.WriteJSON(w, http.StatusCreated, map[string]any{"id": id, "parent_id": ref, "name": name})
}

// rename and/or move. parent_id 0 moves to root; omit a field to leave it unchanged.
func (c Controller) Update(w http.ResponseWriter, r *http.Request, uid string) {
	id, ok := utils.PathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name     *string `json:"name"`
		ParentID *string `json:"parent_id"` // "" = root
	}
	if !utils.ReadJSON(w, r, &req) {
		return
	}
	if role, err := models.Access(r.Context(), c.DB, uid, "folder", id); err != nil || role < models.Editor {
		utils.WriteErr(w, http.StatusNotFound, models.ErrFolderNotFound.Error())
		return
	}
	if req.Name != nil {
		name, ok := utils.CleanName(*req.Name)
		if !ok {
			utils.WriteErr(w, http.StatusBadRequest, "name must be 1-255 chars")
			return
		}
		c.DB.Exec(`UPDATE folders SET name = $1 WHERE id = $2`, name, id)
	}
	if req.ParentID != nil {
		if err := models.CanMove(r.Context(), c.DB, uid, "folder", id, *req.ParentID); err != nil {
			utils.WriteErr(w, http.StatusNotFound, err.Error())
			return
		}
		ref, _ := models.FolderRef(r.Context(), c.DB, uid, *req.ParentID, models.Editor)
		// Refuse moving a folder into itself or one of its own subfolders.
		var cycle int
		c.DB.QueryRow(`WITH RECURSIVE up(id) AS (
				SELECT $1::uuid UNION SELECT parent_id FROM folders JOIN up ON folders.id = up.id WHERE parent_id IS NOT NULL
			) SELECT count(*) FROM up WHERE id = $2`, ref, id).Scan(&cycle)
		if cycle > 0 {
			utils.WriteErr(w, http.StatusBadRequest, "cannot move a folder into itself")
			return
		}
		c.DB.Exec(`UPDATE folders SET parent_id = $1 WHERE id = $2`, ref, id)
	}
	w.WriteHeader(http.StatusNoContent)
}
