package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"net/http"
	"strings"
	"time"

	"telegram-drive/app"
	"telegram-drive/app/models"
	"telegram-drive/app/utils"
)

// Guests: people who open a public link (/s/<token>) without an account. They get a guest
// session, sent as the X-Guest header (or ?g= where a header cannot go, such as <img src>),
// and act with the link's role on the linked item and everything inside it.

const guestTTL = 7 * 24 * time.Hour

func guestToken(r *http.Request) string {
	if t := r.Header.Get("X-Guest"); t != "" {
		return t
	}
	return r.URL.Query().Get("g")
}

// SecureOrGuest lets in a logged-in user, or a guest of a public link that still works.
// A guest token wins over the login cookie, so a logged-in family member who opens a
// link sees exactly what the link shows.
func (c Controller) SecureOrGuest(next app.AuthedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if t := guestToken(r); t != "" {
			g, err := c.guest(r.Context(), t)
			if err != nil {
				utils.WriteErr(w, http.StatusUnauthorized, "this link no longer works")
				return
			}
			next(w, r.WithContext(models.WithGuest(r.Context(), g)), models.GuestUID)
			return
		}
		c.Secure(next)(w, r)
	}
}

func (c Controller) guest(ctx context.Context, tok string) (*models.Guest, error) {
	return models.LoadGuest(ctx, c.DB, hashToken(tok))
}

type linkInfo struct {
	ItemID      string `json:"item_id"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Owner       string `json:"owner"`
	Role        string `json:"role"`
	Password    bool   `json:"password"`     // ask for it before opening
	RequireName bool   `json:"require_name"` // ask the guest's name before they change anything
	NoDownload  bool   `json:"no_download"`
	MaxFile     int64  `json:"max_file"`
}

// link finds a working public link by its token.
func (c Controller) link(ctx context.Context, token string) (l linkInfo, hash *string, err error) {
	var trashed bool
	if token == "" || len(token) > 64 {
		return l, nil, sql.ErrNoRows
	}
	err = c.DB.QueryRowContext(ctx, `SELECT l.item_id, l.item_type, l.role, l.password_hash, l.require_name, l.no_download, l.max_file,
			coalesce(d.name, f.name), u.username, d.trashed_at IS NOT NULL OR f.trashed_at IS NOT NULL
		FROM links l LEFT JOIN folders d ON d.id = l.item_id LEFT JOIN files f ON f.id = l.item_id
		JOIN users u ON u.id = coalesce(d.user_id, f.user_id)
		WHERE l.token = $1 AND l.enabled AND (l.expires_at IS NULL OR l.expires_at > $2)`, token, time.Now().Unix()).
		Scan(&l.ItemID, &l.Type, &l.Role, &hash, &l.RequireName, &l.NoDownload, &l.MaxFile, &l.Name, &l.Owner, &trashed)
	if err == nil && trashed {
		err = sql.ErrNoRows // a link to something in the trash stops working until it is restored
	}
	l.Password = hash != nil
	return l, hash, err
}

// GET /api/s/{token} — what a public link opens, so the page can ask for a password or a name.
func (c Controller) LinkInfo(w http.ResponseWriter, r *http.Request) {
	l, _, err := c.link(r.Context(), r.PathValue("token"))
	if err != nil {
		utils.WriteErr(w, http.StatusNotFound, "this link does not exist or no longer works")
		return
	}
	if l.Role == "viewer" { // a name is only needed to record who changed something
		l.RequireName = false
	}
	utils.WriteJSON(w, http.StatusOK, l)
}

// POST /api/s/{token}/session {"password"?, "name"?} — opens the link: a guest token for the
// X-Guest header. Wrong passwords count against the IP like failed logins.
func (c Controller) LinkSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
		Name     string `json:"name"`
	}
	if !utils.ReadJSON(w, r, &req) {
		return
	}
	token := r.PathValue("token")
	ip := clientIP(r)
	if wait := logins.blocked(ip, "link:"+token); wait > 0 {
		utils.WriteErr(w, http.StatusTooManyRequests, "too many wrong passwords; try again later")
		return
	}
	l, hash, err := c.link(r.Context(), token)
	if err != nil {
		utils.WriteErr(w, http.StatusNotFound, "this link does not exist or no longer works")
		return
	}
	if hash != nil && !checkPassword(*hash, req.Password) {
		logins.fail(ip, "link:"+token)
		utils.WriteErr(w, http.StatusForbidden, "wrong password")
		return
	}
	name, _ := utils.CleanName(req.Name)
	if len([]rune(name)) > 60 {
		name = string([]rune(name)[:60])
	}
	if l.RequireName && l.Role != "viewer" && strings.TrimSpace(name) == "" {
		utils.WriteErr(w, http.StatusBadRequest, "name required")
		return
	}
	tok := rand.Text()
	if _, err := c.DB.Exec(`INSERT INTO guest_sessions (token_hash, item_id, link_token, name, expires_at) VALUES ($1, $2, $3, $4, $5)`,
		hashToken(tok), l.ItemID, token, name, time.Now().Add(guestTTL).Unix()); err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	c.DB.Exec(`DELETE FROM guest_sessions WHERE expires_at < $1`, time.Now().Unix())
	out := map[string]any{"guest": tok, "link": l, "name": name}
	// A link to one file: the page shows that file, so send its details along.
	if l.Type == "file" {
		files, err := models.QueryFiles(c.DB, `SELECT `+models.FileColumns+` FROM files WHERE id = $1`, l.ItemID)
		if err == nil && len(files) == 1 {
			out["file"] = files[0]
		}
	}
	utils.WriteJSON(w, http.StatusOK, out)
}
