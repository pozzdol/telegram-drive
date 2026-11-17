// Package auth: register, login, logout and the Secure middleware.
package auth

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"telegram-drive/app"
	"telegram-drive/app/models"
	"telegram-drive/app/utils"
)

type Controller struct{ *app.App }

const cookieName = "token"

func tokenFrom(r *http.Request) string {
	if t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return t
	}
	if c, err := r.Cookie(cookieName); err == nil {
		return c.Value
	}
	return ""
}

// Secure rejects requests without a valid session and passes the user id on.
func (c Controller) Secure(next app.AuthedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, err := userForToken(c.DB, tokenFrom(r))
		if err != nil {
			utils.WriteErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r, uid)
	}
}

// Admin is Secure plus a check that the user is an admin.
func (c Controller) Admin(next app.AuthedHandler) http.HandlerFunc {
	return c.Secure(func(w http.ResponseWriter, r *http.Request, uid string) {
		var admin bool
		if c.DB.QueryRow(`SELECT is_admin FROM users WHERE id = $1`, uid).Scan(&admin); !admin {
			utils.WriteErr(w, http.StatusForbidden, "admin only")
			return
		}
		next(w, r, uid)
	})
}

// Me tells the UI who is logged in and whether to show the admin page.
func (c Controller) Me(w http.ResponseWriter, r *http.Request, uid string) {
	var name string
	var admin bool
	var tgID *int64
	if err := c.DB.QueryRow(`SELECT username, is_admin, telegram_id FROM users WHERE id = $1`, uid).Scan(&name, &admin, &tgID); err != nil {
		utils.WriteErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	utils.WriteJSON(w, http.StatusOK, map[string]any{"id": uid, "username": name, "is_admin": admin, "telegram_linked": tgID != nil})
}

// ChangePassword needs the current password; other sessions of this user are logged out.
func (c Controller) ChangePassword(w http.ResponseWriter, r *http.Request, uid string) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !utils.ReadJSON(w, r, &req) {
		return
	}
	var hash string
	c.DB.QueryRow(`SELECT password_hash FROM users WHERE id = $1`, uid).Scan(&hash)
	if !checkPassword(hash, req.Current) {
		utils.WriteErr(w, http.StatusForbidden, "current password is wrong")
		return
	}
	if err := SetPassword(c.DB, uid, req.New); err != nil {
		utils.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	c.DB.Exec(`DELETE FROM sessions WHERE user_id = $1 AND token_hash <> $2`, uid, hashToken(tokenFrom(r)))
	w.WriteHeader(http.StatusNoContent)
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (c Controller) Register(w http.ResponseWriter, r *http.Request) {
	var req credentials
	if !utils.ReadJSON(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if len(req.Username) < 3 || len(req.Password) < 8 {
		utils.WriteErr(w, http.StatusBadRequest, "username min 3 chars, password min 8 chars")
		return
	}
	hash, err := hashPassword(req.Password)
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	// Only the very first user registers (and becomes admin); everyone else is added from the admin page.
	var id string
	err = c.DB.QueryRow(`INSERT INTO users (id, username, password_hash, is_admin) SELECT $3, $1, $2, true WHERE NOT EXISTS (SELECT 1 FROM users) RETURNING id`,
		req.Username, hash, models.NewID()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		utils.WriteErr(w, http.StatusForbidden, "registration closed")
		return
	}
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	utils.WriteJSON(w, http.StatusCreated, map[string]any{"id": id, "username": req.Username})
}

func (c Controller) Login(w http.ResponseWriter, r *http.Request) {
	var req credentials
	if !utils.ReadJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Username)
	ip := clientIP(r)
	if wait := logins.blocked(ip, name); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		utils.WriteErr(w, http.StatusTooManyRequests, "too many failed logins, try again later")
		return
	}
	var uid string
	hash := dummyHash // unknown users cost the same PBKDF2 work, so timing does not reveal usernames
	err := c.DB.QueryRow(`SELECT id, password_hash FROM users WHERE username = $1`, name).Scan(&uid, &hash)
	if !checkPassword(hash, req.Password) || err != nil {
		logins.fail(ip, name)
		utils.WriteErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	logins.reset(ip, name)
	c.startSession(w, r, uid)
}

// startSession logs uid in: sets the cookie and returns the token for API clients.
func (c Controller) startSession(w http.ResponseWriter, r *http.Request, uid string) {
	tok, err := newSession(c.DB, uid)
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	// Cookie lets the browser UI use <img>/<video src> directly; API clients use the Bearer token.
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: isHTTPS(r), MaxAge: int(sessionTTL.Seconds())})
	utils.WriteJSON(w, http.StatusOK, map[string]string{"token": tok})
}

// isHTTPS also trusts Cloudflare Tunnel / reverse proxy, where TLS ends before this server.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func (c Controller) Logout(w http.ResponseWriter, r *http.Request, uid string) {
	c.DB.Exec(`DELETE FROM sessions WHERE token_hash = $1`, hashToken(tokenFrom(r)))
	http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}
