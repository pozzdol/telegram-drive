package file

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"telegram-drive/app/models"
	"telegram-drive/app/utils"
)

// Pictures in a document live in a hidden folder beside it, "<document name>.assets", marked
// with assets_of = the document. The Markdown links them by relative path
// (![](notes.md.assets/photo.png)), so the file stays readable in other Markdown tools.
// The folder keeps its name when the document is renamed: the links keep working.

type assets struct {
	FolderID *string  `json:"folder_id"` // null: no pictures yet
	Name     string   `json:"name"`      // the folder name, the first part of the links
	Files    []string `json:"files"`     // names already used
}

func (c Controller) assetsOf(f models.File) (assets, error) {
	a := assets{Name: f.Name + ".assets", Files: []string{}}
	err := c.DB.QueryRow(`SELECT id, name FROM folders WHERE assets_of = $1`, f.ID).Scan(&a.FolderID, &a.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return a, nil
	}
	if err != nil {
		return a, err
	}
	rows, err := c.DB.Query(`SELECT name FROM files WHERE folder_id = $1`, *a.FolderID)
	if err != nil {
		return a, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		rows.Scan(&n)
		a.Files = append(a.Files, n)
	}
	return a, rows.Err()
}

// GET /api/files/{id}/assets — the document's pictures folder and the names in it.
func (c Controller) Assets(w http.ResponseWriter, r *http.Request, uid string) {
	f, _, ok := c.getFile(w, r, uid, models.Viewer)
	if !ok {
		return
	}
	a, err := c.assetsOf(f)
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	utils.WriteJSON(w, http.StatusOK, a)
}

// POST /api/files/{id}/assets — the same, creating the folder if needed (editors), so pictures
// can be uploaded into it with the normal upload.
func (c Controller) CreateAssets(w http.ResponseWriter, r *http.Request, uid string) {
	f, _, ok := c.getFile(w, r, uid, models.Editor)
	if !ok {
		return
	}
	if !Editable(f.Name, f.Mime) {
		utils.WriteErr(w, http.StatusBadRequest, "only documents have a pictures folder")
		return
	}
	a, err := c.assetsOf(f)
	if err == nil && a.FolderID == nil {
		id := models.NewID()
		// Beside the document, owned by the document's owner; NOT EXISTS keeps it to one folder.
		_, err = c.DB.Exec(`INSERT INTO folders (id, user_id, parent_id, name, guest_name, assets_of)
			SELECT $1, user_id, folder_id, $2, $3, id FROM files WHERE id = $4 AND NOT EXISTS (SELECT 1 FROM folders WHERE assets_of = $4)`,
			id, a.Name, models.GuestName(r.Context()), f.ID)
		if err == nil {
			a, err = c.assetsOf(f)
		}
	}
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	utils.WriteJSON(w, http.StatusOK, a)
}

// GET /api/files/{id}/asset?path=<folder>/<picture>[&inline=1] — a picture of the document, by
// the relative path the Markdown uses. Only the document's own pictures folder is reachable.
func (c Controller) Asset(w http.ResponseWriter, r *http.Request, uid string) {
	f, _, ok := c.getFile(w, r, uid, models.Viewer)
	if !ok {
		return
	}
	dir, name, ok := strings.Cut(strings.TrimPrefix(r.URL.Query().Get("path"), "./"), "/")
	var p models.File
	var msgID int
	err := c.DB.QueryRow(`SELECT x.id, x.name, x.size, x.mime, x.created_at, x.msg_id FROM files x JOIN folders d ON d.id = x.folder_id
		WHERE d.assets_of = $1 AND d.name = $2 AND x.name = $3 AND x.trashed_at IS NULL ORDER BY x.created_at LIMIT 1`, f.ID, dir, name).
		Scan(&p.ID, &p.Name, &p.Size, &p.Mime, &p.CreatedAt, &msgID)
	if !ok || err != nil {
		utils.WriteErr(w, http.StatusNotFound, "picture not found")
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	c.serve(w, r, p, msgID, p.CreatedAt)
}
