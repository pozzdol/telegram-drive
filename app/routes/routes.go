// Package routes maps URLs to controllers.
package routes

import (
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"

	"telegram-drive/app"
	"telegram-drive/app/controllers/auth"
	"telegram-drive/app/controllers/comment"
	"telegram-drive/app/controllers/file"
	"telegram-drive/app/controllers/folder"
	"telegram-drive/app/controllers/live"
	"telegram-drive/app/controllers/share"
	"telegram-drive/app/controllers/upload"
	"telegram-drive/app/controllers/user"
	"telegram-drive/app/utils"
	"telegram-drive/webui"
)

func New(a *app.App) http.Handler {
	authC := auth.Controller{App: a}
	folderC := folder.Controller{App: a}
	fileC := file.Controller{App: a}
	uploadC := upload.Controller{App: a}
	userC := user.Controller{App: a}
	shareC := share.Controller{App: a}
	commentC := comment.Controller{App: a}
	liveC := live.Controller{App: a}
	secure, admin := authC.Secure, authC.Admin
	// Also open to guests of a public link, within what the link shows (see models.Access).
	guestOK := authC.SecureOrGuest

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		utils.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Auth
	mux.HandleFunc("POST /api/auth/register", authC.Register)
	mux.HandleFunc("POST /api/auth/login", authC.Login)
	mux.HandleFunc("POST /api/auth/logout", secure(authC.Logout))
	mux.HandleFunc("GET /api/me", secure(authC.Me))
	mux.HandleFunc("GET /api/auth/config", authC.Config)
	mux.HandleFunc("POST /api/auth/telegram", authC.TelegramLogin)
	mux.HandleFunc("PUT /api/me/telegram", secure(authC.LinkTelegram))
	mux.HandleFunc("DELETE /api/me/telegram", secure(authC.UnlinkTelegram))
	mux.HandleFunc("PUT /api/me/password", secure(authC.ChangePassword))

	// Admin: family accounts
	mux.HandleFunc("GET /api/admin/users", admin(userC.List))
	mux.HandleFunc("POST /api/admin/users", admin(userC.Create))
	mux.HandleFunc("PATCH /api/admin/users/{id}", admin(userC.Update))
	mux.HandleFunc("DELETE /api/admin/users/{id}", admin(userC.Delete))

	// Folders
	mux.HandleFunc("GET /api/list", guestOK(folderC.List))
	mux.HandleFunc("POST /api/folders", guestOK(folderC.Create))
	mux.HandleFunc("PATCH /api/folders/{id}", guestOK(folderC.Update))
	mux.HandleFunc("GET /api/search", secure(folderC.Search))

	// Uploads (chunked)
	mux.HandleFunc("POST /api/uploads", guestOK(uploadC.Create))
	mux.HandleFunc("PUT /api/uploads/{id}", guestOK(uploadC.Chunk))
	mux.HandleFunc("GET /api/uploads/{id}", guestOK(uploadC.Status))

	// Files
	mux.HandleFunc("GET /api/files/{id}", guestOK(fileC.Download)) // also serves HEAD
	mux.HandleFunc("GET /api/files/{id}/thumb", guestOK(fileC.Thumb))
	mux.HandleFunc("PUT /api/files/{id}/thumb", guestOK(fileC.PutThumb))
	mux.HandleFunc("POST /api/files/{id}/check", guestOK(fileC.Check)) // still in Telegram?
	mux.HandleFunc("GET /api/media", secure(fileC.Media))
	mux.HandleFunc("GET /api/usage", secure(fileC.Usage))
	mux.HandleFunc("PATCH /api/files/{id}", guestOK(fileC.Update))
	mux.HandleFunc("POST /api/delete", guestOK(fileC.BulkTrash)) // into the trash; folders with everything inside
	mux.HandleFunc("POST /api/move", guestOK(fileC.BulkMove))    // several files/folders into one folder

	// Documents (Markdown and text edited in the app) and their versions
	mux.HandleFunc("POST /api/documents", guestOK(fileC.CreateDocument))
	mux.HandleFunc("PUT /api/files/{id}/content", guestOK(fileC.PutContent))
	mux.HandleFunc("GET /api/files/{id}/versions", guestOK(fileC.Versions))
	mux.HandleFunc("GET /api/files/{id}/versions/{vid}", guestOK(fileC.VersionDownload))
	mux.HandleFunc("PATCH /api/files/{id}/versions/{vid}", guestOK(fileC.VersionLabel))
	mux.HandleFunc("POST /api/files/{id}/versions/{vid}/restore", guestOK(fileC.VersionRestore))
	mux.HandleFunc("DELETE /api/files/{id}/versions/{vid}", guestOK(fileC.VersionDelete))
	mux.HandleFunc("GET /api/files/{id}/assets", guestOK(fileC.Assets))
	mux.HandleFunc("POST /api/files/{id}/assets", guestOK(fileC.CreateAssets))
	mux.HandleFunc("GET /api/files/{id}/asset", guestOK(fileC.Asset)) // ?path=<doc>.assets/<picture>
	mux.HandleFunc("POST /api/files/{id}/live/ticket", guestOK(liveC.Ticket))
	mux.HandleFunc("GET /api/files/{id}/live", liveC.Serve) // WebSocket; the ticket is the auth

	// Sharing with other accounts
	mux.HandleFunc("GET /api/people", secure(shareC.People))
	mux.HandleFunc("GET /api/shared", secure(shareC.Shared)) // shared with me
	mux.HandleFunc("GET /api/shares/{id}", secure(shareC.List))
	mux.HandleFunc("PUT /api/shares/{id}", secure(shareC.Put))
	mux.HandleFunc("DELETE /api/shares/{id}/{user}", secure(shareC.Delete))

	// Comments on files
	mux.HandleFunc("GET /api/files/{id}/comments", guestOK(commentC.List))
	mux.HandleFunc("POST /api/files/{id}/comments", guestOK(commentC.Create))
	mux.HandleFunc("DELETE /api/comments/{id}", guestOK(commentC.Delete))

	// Public links
	mux.HandleFunc("GET /api/s/{token}", authC.LinkInfo)
	mux.HandleFunc("POST /api/s/{token}/session", authC.LinkSession)
	mux.HandleFunc("GET /api/links/{id}", guestOK(shareC.GetLink))
	mux.HandleFunc("PUT /api/links/{id}", guestOK(shareC.PutLink))
	mux.HandleFunc("POST /api/links/{id}/reset", guestOK(shareC.ResetLink))

	// Trash
	mux.HandleFunc("GET /api/trash", secure(fileC.TrashList))
	mux.HandleFunc("POST /api/trash/restore", secure(fileC.TrashRestore))
	mux.HandleFunc("POST /api/trash/purge", secure(fileC.TrashPurge)) // for good, Telegram messages too

	// Release binaries carry the UI: serve it on the same origin as the API.
	if ui := webui.FS(); ui != nil {
		mux.Handle("/", uiHandler(ui))
	}

	return cors(a.Origin, mux)
}

// uiHandler serves the built UI. Paths that are not files (/s/<token>, ?folder=…) get
// index.html, since the app routes in the browser. Hashed assets are cached for a year.
func uiHandler(ui fs.FS) http.Handler {
	files := http.FileServerFS(ui)
	csp := strings.Join([]string{
		"default-src 'self'",
		"script-src 'self' https://telegram.org", // Telegram login widget
		"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com",
		"font-src https://fonts.gstatic.com",
		"img-src 'self' data: blob: https://*.telegram.org https://t.me",
		"media-src 'self' blob:",
		"connect-src 'self'",
		"frame-src https://oauth.telegram.org",
		"frame-ancestors 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"object-src 'none'",
	}, "; ")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			utils.WriteErr(w, http.StatusNotFound, "not found")
			return
		}
		h := w.Header()
		// The live editor's WebSocket: older Safari does not count ws(s) as 'self'.
		policy := csp
		if validHost.MatchString(r.Host) {
			scheme := "ws://"
			if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
				scheme = "wss://"
			}
			policy = strings.Replace(csp, "connect-src 'self'", "connect-src 'self' "+scheme+r.Host, 1)
		}
		h.Set("Content-Security-Policy", policy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if st, err := fs.Stat(ui, name); name == "" || err != nil || st.IsDir() {
			h.Set("Cache-Control", "no-cache")
			http.ServeFileFS(w, r, ui, "index.html")
			return
		}
		if strings.HasPrefix(name, "assets/") {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

var validHost = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]+)?$`)

// sameHost reports whether an Origin header names the host the request came to, which is how
// requests from the UI look when the binary serves it itself.
func sameHost(origin, host string) bool {
	u, err := url.Parse(origin)
	return err == nil && u.Host == host
}

// cors lets the UI on another subdomain (Cloudflare Pages, Vercel) call the API with its cookie.
// It also refuses state-changing requests that a browser sent from any other origin (CSRF),
// including sibling subdomains that SameSite=Strict still counts as same-site.
// Requests without an Origin header (curl, API clients) are not browser cross-site requests.
func cors(origin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && o != origin && !sameHost(o, r.Host) &&
			r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			utils.WriteErr(w, http.StatusForbidden, "origin not allowed")
			return
		}
		if origin != "" && r.Header.Get("Origin") == origin {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Expose-Headers", "Content-Range, X-Rev") // lets a preview tell it got only part of the file
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
				h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Range, X-Guest, X-Rev, X-Live-Seq") // Range: text previews read only the start
				h.Set("Access-Control-Max-Age", "86400")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
