// Package app holds what every controller needs.
package app

import (
	"context"
	"database/sql"
	"net/http"

	"telegram-drive/app/utils/tgclient"
)

type App struct {
	Ctx       context.Context // server lifetime, for background Telegram uploads
	DB        *sql.DB
	TG        *tgclient.Client
	MaxUpload int64 // bytes
	UploadDir string
	ThumbDir  string // cached image previews, data/thumbs/<file id>.jpg
	Origin    string // UI origin allowed by CORS
	BotToken  string // Telegram bot for "Log in with Telegram"; empty disables it
	BotName   string
}

// AuthedHandler is a handler behind auth.Controller.Secure; uid is the logged-in user.
type AuthedHandler func(w http.ResponseWriter, r *http.Request, uid int64)
