// Entry point: load config, open the DB, log in to Telegram, serve HTTP.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"telegram-drive/app"
	"telegram-drive/app/controllers/file"
	"telegram-drive/app/controllers/upload"
	"telegram-drive/app/models"
	"telegram-drive/app/routes"
	"telegram-drive/app/utils/tgclient"
	"telegram-drive/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	for _, dir := range []string{cfg.UploadDir(), cfg.ThumbDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			log.Fatal(err)
		}
	}
	db, err := models.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tg := tgclient.New(cfg.TelegramAPIID, cfg.TelegramAPIHash, cfg.SessionPath())
	err = tg.Run(ctx, cfg.TelegramPhone, cfg.TelegramChannelID, func(ctx context.Context) error {
		a := &app.App{Ctx: ctx, DB: db, TG: tg, MaxUpload: cfg.MaxUploadMB << 20, UploadDir: cfg.UploadDir(), ThumbDir: cfg.ThumbDir(), Origin: cfg.AllowedOrigin}
		upload.Controller{App: a}.Resume()
		go file.Controller{App: a}.BackfillTakenAt()

		srv := &http.Server{Addr: cfg.Host + ":" + cfg.Port, Handler: routes.New(a), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			<-ctx.Done()
			srv.Shutdown(context.Background())
		}()
		log.Printf("listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
