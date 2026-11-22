// Package app holds what every controller needs.
package app

import (
	"context"
	"database/sql"
	"io"
	"net/http"

	"telegram-drive/app/utils/tgclient"
)

type App struct {
	Ctx       context.Context // server lifetime, for background Telegram uploads
	DB        *sql.DB
	TG        Telegram
	MaxUpload int64 // bytes
	UploadDir string
	ThumbDir  string // cached image previews, data/thumbs/<file id>.jpg
	Origin    string // UI origin allowed by CORS
	BotToken  string // Telegram bot for "Log in with Telegram"; empty disables it
	BotName   string
}

// AuthedHandler is a handler behind auth.Controller.Secure; uid is the logged-in user.
type AuthedHandler func(w http.ResponseWriter, r *http.Request, uid string)

// Telegram is the storage channel (*tgclient.Client); tests use a fake.
type Telegram interface {
	Upload(ctx context.Context, r io.Reader, name string, size int64, mimeType string, onProgress tgclient.ProgressFunc) (int, error)
	Open(ctx context.Context, msgID int, parallel int) (io.ReadSeeker, error)
	Delete(ctx context.Context, msgIDs ...int) error
	Present(ctx context.Context, msgIDs []int) (map[int]bool, error)
}
