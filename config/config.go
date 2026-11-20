// Package config reads settings from .env and the environment.
package config

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Host             string
	Port             string
	DataDir          string
	MaxUploadMB      int64 // 2000 for a normal Telegram account, 4000 for Premium
	DatabaseURL      string
	AllowedOrigin    string // UI origin allowed by CORS, e.g. https://drive.example.com
	TelegramBotToken string // optional: enables "Log in with Telegram" (token from @BotFather)
	TelegramBotName  string // the bot\'s username, without @

	TelegramAPIID     int
	TelegramAPIHash   string
	TelegramPhone     string
	TelegramChannelID int64
}

func (c Config) SessionPath() string { return filepath.Join(c.DataDir, "telegram.session") }
func (c Config) UploadDir() string   { return filepath.Join(c.DataDir, "uploads") }
func (c Config) ThumbDir() string    { return filepath.Join(c.DataDir, "thumbs") }

// ChannelIDPath holds the id of the storage channel the server created itself, when
// TELEGRAM_CHANNEL_ID is not set.
func (c Config) ChannelIDPath() string { return filepath.Join(c.DataDir, "channel.id") }

// NeedTelegram reports what is missing to connect to Telegram (serve and login need it).
func (c Config) NeedTelegram() error {
	if c.TelegramAPIID == 0 || c.TelegramAPIHash == "" || c.TelegramPhone == "" {
		return errors.New("set TELEGRAM_API_ID, TELEGRAM_API_HASH (from https://my.telegram.org) and TELEGRAM_PHONE in .env")
	}
	return nil
}

// Load reads .env (real environment variables win) and validates required values.
func Load() (Config, error) {
	loadDotEnv(".env")
	c := Config{
		Host:             env("HOST", "127.0.0.1"), // 0.0.0.0 only when nothing on this machine proxies it
		Port:             env("PORT", "8080"),
		DataDir:          env("DATA_DIR", "data"),
		AllowedOrigin:    os.Getenv("ALLOWED_ORIGIN"),
		TelegramBotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramBotName:  strings.TrimPrefix(os.Getenv("TELEGRAM_BOT_USERNAME"), "@"),
		DatabaseURL:      os.Getenv("DATABASE_URL"),
		TelegramAPIHash:  os.Getenv("TELEGRAM_API_HASH"),
		TelegramPhone:    os.Getenv("TELEGRAM_PHONE"),
	}
	c.TelegramAPIID, _ = strconv.Atoi(os.Getenv("TELEGRAM_API_ID"))
	c.TelegramChannelID, _ = strconv.ParseInt(os.Getenv("TELEGRAM_CHANNEL_ID"), 10, 64)
	if c.TelegramChannelID == 0 { // created by the server on first login (see tgclient.OnNewChannel)
		b, _ := os.ReadFile(c.ChannelIDPath())
		c.TelegramChannelID, _ = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	}
	if c.DatabaseURL == "" {
		return c, errors.New("set DATABASE_URL in .env, e.g. postgres://user:pass@localhost:5432/telegram_drive?sslmode=disable")
	}
	var err error
	if c.MaxUploadMB, err = strconv.ParseInt(env("MAX_UPLOAD_MB", "2000"), 10, 64); err != nil {
		return c, errors.New("MAX_UPLOAD_MB must be a number")
	}
	return c, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func loadDotEnv(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		k = strings.TrimSpace(k)
		if _, set := os.LookupEnv(k); !set {
			os.Setenv(k, strings.Trim(strings.TrimSpace(v), `"'`))
		}
	}
}
