// Package routes maps URLs to controllers.
package routes

import (
	"net/http"

	"telegram-drive/app"
	"telegram-drive/app/controllers/auth"
	"telegram-drive/app/controllers/comment"
	"telegram-drive/app/controllers/file"
	"telegram-drive/app/controllers/folder"
	"telegram-drive/app/controllers/share"
	"telegram-drive/app/controllers/upload"
	"telegram-drive/app/controllers/user"
	"telegram-drive/app/utils"
)

func New(a *app.App) http.Handler {
	authC := auth.Controller{App: a}
	folderC := folder.Controller{App: a}
	fileC := file.Controller{App: a}
	uploadC := upload.Controller{App: a}
	userC := user.Controller{App: a}
	shareC := share.Controller{App: a}
	commentC := comment.Controller{App: a}
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

	return cors(a.Origin, mux)
}

// cors lets the UI on another subdomain (Cloudflare Pages, Vercel) call the API with its cookie.
// It also refuses state-changing requests that a browser sent from any other origin (CSRF),
// including sibling subdomains that SameSite=Strict still counts as same-site.
// Requests without an Origin header (curl, API clients) are not browser cross-site requests.
func cors(origin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && origin != "" && o != origin &&
			r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			utils.WriteErr(w, http.StatusForbidden, "origin not allowed")
			return
		}
		if origin != "" && r.Header.Get("Origin") == origin {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Expose-Headers", "Content-Range") // lets a preview tell it got only part of the file
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
				h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Range, X-Guest") // Range: text previews read only the start
				h.Set("Access-Control-Max-Age", "86400")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
