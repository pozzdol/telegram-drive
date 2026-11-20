// Entry point: load config, open the DB, log in to Telegram, serve HTTP.
//
//	telegram-drive              serve the API (and the UI, in release builds)
//	telegram-drive login        log in to Telegram once (asks for the code) and set up the storage channel
//	telegram-drive admin NAME   create or reset an admin account; reads the password from stdin
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"telegram-drive/app"
	"telegram-drive/app/controllers/auth"
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
	if len(os.Args) > 1 && os.Args[1] != "serve" {
		if err := command(cfg, os.Args[1:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := cfg.NeedTelegram(); err != nil {
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

	tg := newTelegram(cfg)
	err = tg.Run(ctx, cfg.TelegramPhone, cfg.TelegramChannelID, func(ctx context.Context) error {
		a := &app.App{Ctx: ctx, DB: db, TG: tg, MaxUpload: cfg.MaxUploadMB << 20, UploadDir: cfg.UploadDir(), ThumbDir: cfg.ThumbDir(), Origin: cfg.AllowedOrigin, BotToken: cfg.TelegramBotToken, BotName: cfg.TelegramBotName}
		upload.Controller{App: a}.Resume()
		go file.Controller{App: a}.BackfillTakenAt()
		go file.Controller{App: a}.WatchMissing(6 * time.Hour)
		go file.Controller{App: a}.WatchTrash(time.Hour)

		srv := &http.Server{Addr: cfg.Host + ":" + cfg.Port, Handler: routes.New(a), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
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

// newTelegram makes the Telegram client; a storage channel it creates is remembered in data/.
func newTelegram(cfg config.Config) *tgclient.Client {
	tg := tgclient.New(cfg.TelegramAPIID, cfg.TelegramAPIHash, cfg.SessionPath())
	tg.OnNewChannel = func(id int64) error {
		return os.WriteFile(cfg.ChannelIDPath(), []byte(strconv.FormatInt(id, 10)+"\n"), 0o600)
	}
	return tg
}

func command(cfg config.Config, args []string) error {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	switch args[0] {
	case "login":
		if err := cfg.NeedTelegram(); err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return newTelegram(cfg).Run(ctx, cfg.TelegramPhone, cfg.TelegramChannelID, func(context.Context) error {
			fmt.Println("Telegram login OK.")
			return nil
		})
	case "admin":
		if len(args) != 2 || len(strings.TrimSpace(args[1])) < 3 {
			return errors.New("usage: telegram-drive admin USERNAME   (password on stdin, at least 8 characters)")
		}
		pw, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && pw == "" {
			return errors.New("read password from stdin")
		}
		pw = strings.TrimRight(pw, "\r\n")
		db, err := models.Open(cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer db.Close()
		hash, err := auth.HashPassword(pw)
		if err != nil {
			return err
		}
		name := strings.TrimSpace(args[1])
		if _, err := db.Exec(`INSERT INTO users (id, username, password_hash, is_admin) VALUES ($1, $2, $3, true)
			ON CONFLICT (username) DO UPDATE SET password_hash = EXCLUDED.password_hash, is_admin = true`, models.NewID(), name, hash); err != nil {
			return err
		}
		db.Exec(`DELETE FROM sessions WHERE user_id = (SELECT id FROM users WHERE username = $1)`, name)
		fmt.Printf("Admin %q is ready.\n", name)
		return nil
	}
	return fmt.Errorf("unknown command %q (use: serve, login, admin USERNAME)", args[0])
}
