// Package share: share files and folders with other accounts, and list what others shared.
package share

import (
	"database/sql"
	"errors"
	"net/http"

	"telegram-drive/app"
	"telegram-drive/app/models"
	"telegram-drive/app/utils"
)

type Controller struct{ *app.App }

type person struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// GET /api/people — every other account, to pick whom to share with.
func (c Controller) People(w http.ResponseWriter, r *http.Request, uid string) {
	rows, err := c.DB.Query(`SELECT id, username FROM users WHERE id <> $1 ORDER BY lower(username)`, uid)
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	out := []person{}
	for rows.Next() {
		var p person
		rows.Scan(&p.ID, &p.Username)
		out = append(out, p)
	}
	utils.WriteJSON(w, http.StatusOK, map[string]any{"people": out})
}

// item finds whether id is a file or a folder, and its owner.
func (c Controller) item(id string) (kind, owner string, err error) {
	err = c.DB.QueryRow(`SELECT 'folder', user_id FROM folders WHERE id = $1
		UNION ALL SELECT 'file', user_id FROM files WHERE id = $1`, id).Scan(&kind, &owner)
	return
}

// access loads the item in the URL and checks uid's role on it, writing 404 when too low.
func (c Controller) access(w http.ResponseWriter, r *http.Request, uid string, min models.Role) (id, kind, owner string, role models.Role, ok bool) {
	id, ok = utils.PathID(w, r)
	if !ok {
		return
	}
	var err error
	if kind, owner, err = c.item(id); err == nil {
		role, err = models.Access(r.Context(), c.DB, uid, kind, id)
	}
	if err != nil || role < min {
		utils.WriteErr(w, http.StatusNotFound, "not found")
		return id, kind, owner, role, false
	}
	return id, kind, owner, role, true
}

type grant struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// GET /api/shares/{id} — who has access: the owner and everyone it is shared with directly.
func (c Controller) List(w http.ResponseWriter, r *http.Request, uid string) {
	id, _, owner, role, ok := c.access(w, r, uid, models.Viewer)
	if !ok {
		return
	}
	var o person
	c.DB.QueryRow(`SELECT id, username FROM users WHERE id = $1`, owner).Scan(&o.ID, &o.Username)
	rows, err := c.DB.Query(`SELECT s.user_id, u.username, s.role FROM shares s JOIN users u ON u.id = s.user_id
		WHERE s.item_id = $1 ORDER BY s.created_at`, id)
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	people := []grant{}
	for rows.Next() {
		var g grant
		rows.Scan(&g.UserID, &g.Username, &g.Role)
		people = append(people, g)
	}
	utils.WriteJSON(w, http.StatusOK, map[string]any{"owner": o, "role": role.String(), "people": people})
}

// PUT /api/shares/{id} {"user_id", "role"} — shares with someone, or changes their role.
// Editors may share, like the owner.
func (c Controller) Put(w http.ResponseWriter, r *http.Request, uid string) {
	id, kind, owner, _, ok := c.access(w, r, uid, models.Editor)
	if !ok {
		return
	}
	var req struct {
		UserID string `json:"user_id"`
		Role   string `json:"role"`
	}
	if !utils.ReadJSON(w, r, &req) {
		return
	}
	role, ok := models.ParseShareRole(req.Role)
	if !ok {
		utils.WriteErr(w, http.StatusBadRequest, "role must be viewer, commenter or editor")
		return
	}
	if !models.ValidID(req.UserID) || req.UserID == owner {
		utils.WriteErr(w, http.StatusBadRequest, "pick another account than the owner")
		return
	}
	_, err := c.DB.Exec(`INSERT INTO shares (item_id, item_type, user_id, role, created_by) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (item_id, user_id) DO UPDATE SET role = EXCLUDED.role`, id, kind, req.UserID, role.String(), uid)
	if models.IsForeignKeyViolation(err) {
		utils.WriteErr(w, http.StatusNotFound, "no such account")
		return
	}
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/shares/{id}/{user} — takes someone's access away. Anybody may remove themselves.
func (c Controller) Delete(w http.ResponseWriter, r *http.Request, uid string) {
	user := r.PathValue("user")
	min := models.Editor
	if user == uid {
		min = models.Viewer
	}
	id, _, _, _, ok := c.access(w, r, uid, min)
	if !ok {
		return
	}
	if !models.ValidID(user) {
		utils.WriteErr(w, http.StatusBadRequest, "invalid user id")
		return
	}
	c.DB.Exec(`DELETE FROM shares WHERE item_id = $1 AND user_id = $2`, id, user)
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/shared — what others shared with the caller directly, newest first.
func (c Controller) Shared(w http.ResponseWriter, r *http.Request, uid string) {
	out := struct {
		Folders []models.Folder `json:"folders"`
		Files   []models.File   `json:"files"`
	}{[]models.Folder{}, []models.File{}}
	rows, err := c.DB.Query(`SELECT d.id, d.parent_id, d.name, d.created_at, u.username, s.role
		FROM shares s JOIN folders d ON d.id = s.item_id JOIN users u ON u.id = d.user_id
		WHERE s.user_id = $1 AND d.trashed_at IS NULL AND d.user_id <> $1 ORDER BY s.created_at DESC`, uid)
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	for rows.Next() {
		var f models.Folder
		rows.Scan(&f.ID, &f.ParentID, &f.Name, &f.CreatedAt, &f.Owner, &f.Role)
		f.Shared = true
		out.Folders = append(out.Folders, f)
	}
	rows.Close()
	rows, err = c.DB.Query(`SELECT f.id, f.folder_id, f.name, f.size, f.mime, f.created_at, f.taken_at, f.missing_at IS NOT NULL, u.username, s.role
		FROM shares s JOIN files f ON f.id = s.item_id JOIN users u ON u.id = f.user_id
		WHERE s.user_id = $1 AND f.trashed_at IS NULL AND f.user_id <> $1 ORDER BY s.created_at DESC`, uid)
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var f models.File
		if err := rows.Scan(&f.ID, &f.FolderID, &f.Name, &f.Size, &f.Mime, &f.CreatedAt, &f.TakenAt, &f.Missing, &f.Owner, &f.Role); err != nil && !errors.Is(err, sql.ErrNoRows) {
			utils.ServerErr(w, http.StatusInternalServerError, err)
			return
		}
		if f.TakenAt != nil && *f.TakenAt == 0 {
			f.TakenAt = nil
		}
		f.Shared = true
		out.Files = append(out.Files, f)
	}
	utils.WriteJSON(w, http.StatusOK, out)
}
