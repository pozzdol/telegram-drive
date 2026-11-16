// Package user: the admin page's user management (family accounts).
// Every user has a private drive; files and folders are already scoped by user_id.
package user

import (
	"errors"
	"net/http"
	"strings"

	"telegram-drive/app"
	"telegram-drive/app/controllers/auth"
	"telegram-drive/app/utils"
)

type Controller struct{ *app.App }

type userRow struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	IsAdmin   bool   `json:"is_admin"`
	CreatedAt int64  `json:"created_at"`
	Files     int64  `json:"files"`
	Bytes     int64  `json:"bytes"`
}

func (c Controller) List(w http.ResponseWriter, r *http.Request, uid int64) {
	rows, err := c.DB.Query(`SELECT u.id, u.username, u.is_admin, u.created_at, count(f.id), coalesce(sum(f.size), 0)
		FROM users u LEFT JOIN files f ON f.user_id = u.id GROUP BY u.id ORDER BY u.id`)
	if err != nil {
		utils.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	users := []userRow{}
	for rows.Next() {
		var u userRow
		if err := rows.Scan(&u.ID, &u.Username, &u.IsAdmin, &u.CreatedAt, &u.Files, &u.Bytes); err != nil {
			utils.WriteErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		users = append(users, u)
	}
	utils.WriteJSON(w, http.StatusOK, map[string]any{"users": users})
}

func cleanUsername(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) < 3 || len(s) > 64 {
		return "", errors.New("username must be 3-64 chars")
	}
	return s, nil
}

func writeDBErr(w http.ResponseWriter, err error) {
	if strings.Contains(err.Error(), "UNIQUE") {
		utils.WriteErr(w, http.StatusConflict, "username already taken")
		return
	}
	utils.WriteErr(w, http.StatusInternalServerError, err.Error())
}

func (c Controller) Create(w http.ResponseWriter, r *http.Request, uid int64) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		IsAdmin  bool   `json:"is_admin"`
	}
	if !utils.ReadJSON(w, r, &req) {
		return
	}
	name, err := cleanUsername(req.Username)
	if err != nil {
		utils.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		utils.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := c.DB.Exec(`INSERT INTO users (username, password_hash, is_admin) VALUES (?, ?, ?)`, name, hash, req.IsAdmin)
	if err != nil {
		writeDBErr(w, err)
		return
	}
	id, _ := res.LastInsertId()
	utils.WriteJSON(w, http.StatusCreated, map[string]any{"id": id, "username": name})
}

// Update renames, resets the password (logging that user out everywhere) or changes the admin flag.
// An admin cannot remove their own admin flag, so the drive never ends up without an admin.
func (c Controller) Update(w http.ResponseWriter, r *http.Request, uid int64) {
	id, ok := utils.PathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Username *string `json:"username"`
		Password *string `json:"password"`
		IsAdmin  *bool   `json:"is_admin"`
	}
	if !utils.ReadJSON(w, r, &req) {
		return
	}
	if req.IsAdmin != nil && !*req.IsAdmin && id == uid {
		utils.WriteErr(w, http.StatusBadRequest, "you cannot remove your own admin role")
		return
	}
	var exists bool
	c.DB.QueryRow(`SELECT 1 FROM users WHERE id = ?`, id).Scan(&exists)
	if !exists {
		utils.WriteErr(w, http.StatusNotFound, "user not found")
		return
	}
	if req.Username != nil {
		name, err := cleanUsername(*req.Username)
		if err != nil {
			utils.WriteErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if _, err := c.DB.Exec(`UPDATE users SET username = ? WHERE id = ?`, name, id); err != nil {
			writeDBErr(w, err)
			return
		}
	}
	if req.Password != nil {
		if err := auth.SetPassword(c.DB, id, *req.Password); err != nil {
			utils.WriteErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if id != uid {
			c.DB.Exec(`DELETE FROM sessions WHERE user_id = ?`, id)
		}
	}
	if req.IsAdmin != nil {
		c.DB.Exec(`UPDATE users SET is_admin = ? WHERE id = ?`, *req.IsAdmin, id)
	}
	w.WriteHeader(http.StatusNoContent)
}

// Delete only removes users with an empty drive: their files live in Telegram and
// should be deleted (or downloaded) by that user first, not orphaned in the channel.
func (c Controller) Delete(w http.ResponseWriter, r *http.Request, uid int64) {
	id, ok := utils.PathID(w, r)
	if !ok {
		return
	}
	if id == uid {
		utils.WriteErr(w, http.StatusBadRequest, "you cannot delete yourself")
		return
	}
	var files int64
	c.DB.QueryRow(`SELECT count(*) FROM files WHERE user_id = ?`, id).Scan(&files)
	if files > 0 {
		utils.WriteErr(w, http.StatusConflict, "user still has files")
		return
	}
	res, err := c.DB.Exec(`DELETE FROM users WHERE id = ?`, id) // sessions, folders, uploads cascade
	if err != nil {
		utils.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		utils.WriteErr(w, http.StatusNotFound, "user not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
