// Package routes maps URLs to controllers.
package routes

import (
	"net/http"

	"telegram-drive/app"
	"telegram-drive/app/controllers/auth"
	"telegram-drive/app/controllers/file"
	"telegram-drive/app/controllers/folder"
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
	secure, admin := authC.Secure, authC.Admin

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		utils.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Auth
	mux.HandleFunc("POST /api/auth/register", authC.Register)
	mux.HandleFunc("POST /api/auth/login", authC.Login)
	mux.HandleFunc("POST /api/auth/logout", secure(authC.Logout))
	mux.HandleFunc("GET /api/me", secure(authC.Me))
	mux.HandleFunc("PUT /api/me/password", secure(authC.ChangePassword))

	// Admin: family accounts
	mux.HandleFunc("GET /api/admin/users", admin(userC.List))
	mux.HandleFunc("POST /api/admin/users", admin(userC.Create))
	mux.HandleFunc("PATCH /api/admin/users/{id}", admin(userC.Update))
	mux.HandleFunc("DELETE /api/admin/users/{id}", admin(userC.Delete))

	// Folders
	mux.HandleFunc("GET /api/list", secure(folderC.List))
	mux.HandleFunc("POST /api/folders", secure(folderC.Create))
	mux.HandleFunc("PATCH /api/folders/{id}", secure(folderC.Update))
	mux.HandleFunc("DELETE /api/folders/{id}", secure(folderC.Delete))
	mux.HandleFunc("GET /api/search", secure(folderC.Search))

	// Uploads (chunked)
	mux.HandleFunc("POST /api/uploads", secure(uploadC.Create))
	mux.HandleFunc("PUT /api/uploads/{id}", secure(uploadC.Chunk))
	mux.HandleFunc("GET /api/uploads/{id}", secure(uploadC.Status))

	// Files
	mux.HandleFunc("GET /api/files/{id}", secure(fileC.Download)) // also serves HEAD
	mux.HandleFunc("GET /api/files/{id}/thumb", secure(fileC.Thumb))
	mux.HandleFunc("PUT /api/files/{id}/thumb", secure(fileC.PutThumb))
	mux.HandleFunc("GET /api/media", secure(fileC.Media))
	mux.HandleFunc("GET /api/usage", secure(fileC.Usage))
	mux.HandleFunc("PATCH /api/files/{id}", secure(fileC.Update))
	mux.HandleFunc("DELETE /api/files/{id}", secure(fileC.Delete))

	return cors(a.Origin, mux)
}

// cors lets the UI on another subdomain (Cloudflare Pages, Vercel) call the API with its cookie.
func cors(origin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin != "" && r.Header.Get("Origin") == origin {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Add("Vary", "Origin")
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
				h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
				h.Set("Access-Control-Max-Age", "86400")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
