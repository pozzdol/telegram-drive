package share

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"telegram-drive/app/controllers/auth"
	"telegram-drive/app/models"
	"telegram-drive/app/utils"
)

type linkSettings struct {
	Enabled     bool     `json:"enabled"` // false = restricted: only people it is shared with
	Token       string   `json:"token"`
	Role        string   `json:"role"`
	ExpiresAt   *int64   `json:"expires_at"`
	Password    bool     `json:"password"` // a password is set (it is never sent back)
	NoDownload  bool     `json:"no_download"`
	RequireName bool     `json:"require_name"`
	MaxFile     int64    `json:"max_file"`
	UploadLimit int64    `json:"upload_limit"`
	Uploaded    int64    `json:"uploaded"`
	Log         []logRow `json:"log"`
}

type logRow struct {
	At     int64  `json:"at"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
}

func (c Controller) loadLink(id string) (linkSettings, error) {
	l := linkSettings{Log: []logRow{}}
	var hash *string
	err := c.DB.QueryRow(`SELECT enabled, token, role, expires_at, password_hash, no_download, require_name, max_file, upload_limit, uploaded
		FROM links WHERE item_id = $1`, id).Scan(&l.Enabled, &l.Token, &l.Role, &l.ExpiresAt, &hash, &l.NoDownload, &l.RequireName,
		&l.MaxFile, &l.UploadLimit, &l.Uploaded)
	if errors.Is(err, sql.ErrNoRows) { // never set up: restricted, with the defaults
		return linkSettings{Role: "viewer", RequireName: true, MaxFile: 2 << 30, UploadLimit: 20 << 30, Log: l.Log}, nil
	}
	l.Password = hash != nil
	rows, err2 := c.DB.Query(`SELECT at, actor, action FROM access_log WHERE item_id = $1 ORDER BY id DESC LIMIT 20`, id)
	if err2 == nil {
		for rows.Next() {
			var x logRow
			rows.Scan(&x.At, &x.Actor, &x.Action)
			l.Log = append(l.Log, x)
		}
		rows.Close()
	}
	return l, err
}

// GET /api/links/{id} — the public link of a file or folder, and the recent access changes.
// Editors (also guests with an edit link) may see and change it.
func (c Controller) GetLink(w http.ResponseWriter, r *http.Request, uid string) {
	id, _, _, _, ok := c.access(w, r, uid, models.Editor)
	if !ok {
		return
	}
	l, err := c.loadLink(id)
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	utils.WriteJSON(w, http.StatusOK, l)
}

// PUT /api/links/{id} — changes the link; fields left out stay as they are.
// password: "" removes it, any other string sets it.
func (c Controller) PutLink(w http.ResponseWriter, r *http.Request, uid string) {
	id, kind, _, _, ok := c.access(w, r, uid, models.Editor)
	if !ok {
		return
	}
	var req struct {
		Enabled     *bool   `json:"enabled"`
		Role        *string `json:"role"`
		ExpiresAt   *int64  `json:"expires_at"` // 0 = never
		Password    *string `json:"password"`
		NoDownload  *bool   `json:"no_download"`
		RequireName *bool   `json:"require_name"`
		MaxFile     *int64  `json:"max_file"`
		UploadLimit *int64  `json:"upload_limit"`
	}
	if !utils.ReadJSON(w, r, &req) {
		return
	}
	if req.Role != nil {
		if _, ok := models.ParseShareRole(*req.Role); !ok {
			utils.WriteErr(w, http.StatusBadRequest, "role must be viewer, commenter or editor")
			return
		}
	}
	if (req.MaxFile != nil && *req.MaxFile < 1) || (req.UploadLimit != nil && *req.UploadLimit < 0) ||
		(req.ExpiresAt != nil && *req.ExpiresAt != 0 && *req.ExpiresAt < time.Now().Unix()) {
		utils.WriteErr(w, http.StatusBadRequest, "invalid limit or expiry")
		return
	}
	tx, err := c.DB.BeginTx(r.Context(), nil)
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO links (item_id, item_type, token, created_by) VALUES ($1, $2, $3, $4) ON CONFLICT (item_id) DO NOTHING`,
		id, kind, rand.Text(), models.Actor(r.Context(), uid)); err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	var changes []string
	set := func(col string, v any, what string) {
		if err == nil {
			_, err = tx.Exec(fmt.Sprintf(`UPDATE links SET %s = $1 WHERE item_id = $2`, col), v, id)
		}
		changes = append(changes, what)
	}
	if req.Enabled != nil {
		set("enabled", *req.Enabled, map[bool]string{true: "membuka link untuk siapa saja", false: "membatasi link"}[*req.Enabled])
	}
	if req.Role != nil {
		set("role", *req.Role, "mengubah peran link menjadi "+*req.Role)
	}
	if req.ExpiresAt != nil {
		var v any
		what := "menghapus tanggal kedaluwarsa"
		if *req.ExpiresAt != 0 {
			v, what = *req.ExpiresAt, "mengatur tanggal kedaluwarsa"
		}
		set("expires_at", v, what)
	}
	if req.Password != nil {
		var v any
		what := "menghapus password link"
		if *req.Password != "" {
			if len([]rune(*req.Password)) < 4 {
				utils.WriteErr(w, http.StatusBadRequest, "link password must be at least 4 characters")
				return
			}
			h, herr := auth.HashLinkPassword(*req.Password)
			if herr != nil {
				utils.ServerErr(w, http.StatusInternalServerError, herr)
				return
			}
			v, what = h, "mengganti password link"
		}
		set("password_hash", v, what)
		if err == nil { // everyone who opened it with the old password opens it again
			_, err = tx.Exec(`DELETE FROM guest_sessions WHERE item_id = $1`, id)
		}
	}
	if req.NoDownload != nil {
		set("no_download", *req.NoDownload, map[bool]string{true: "mematikan download", false: "menyalakan download"}[*req.NoDownload])
	}
	if req.RequireName != nil {
		set("require_name", *req.RequireName, map[bool]string{true: "mewajibkan nama tamu", false: "tidak mewajibkan nama tamu"}[*req.RequireName])
	}
	if req.MaxFile != nil {
		set("max_file", *req.MaxFile, "mengubah batas ukuran file tamu")
	}
	if req.UploadLimit != nil {
		set("upload_limit", *req.UploadLimit, "mengubah batas total upload tamu")
	}
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := tx.Commit(); err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	for _, what := range changes {
		models.LogAccess(r.Context(), c.DB, uid, id, what)
	}
	l, _ := c.loadLink(id)
	utils.WriteJSON(w, http.StatusOK, l)
}

// POST /api/links/{id}/reset — a new link address; the old one stops working at once.
func (c Controller) ResetLink(w http.ResponseWriter, r *http.Request, uid string) {
	id, _, _, _, ok := c.access(w, r, uid, models.Editor)
	if !ok {
		return
	}
	res, err := c.DB.Exec(`UPDATE links SET token = $1 WHERE item_id = $2`, rand.Text(), id)
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		utils.WriteErr(w, http.StatusNotFound, "no link yet")
		return
	}
	c.DB.Exec(`DELETE FROM guest_sessions WHERE item_id = $1`, id)
	models.LogAccess(r.Context(), c.DB, uid, id, "membuat link baru (link lama tidak berlaku)")
	l, _ := c.loadLink(id)
	utils.WriteJSON(w, http.StatusOK, l)
}
