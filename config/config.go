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

	// Federation (docs/federation.md): sharing with users on other Telegram Drive servers.
	Federation      bool     // FEDERATION=on
	PublicAPIURL    string   // this API as other servers reach it, e.g. https://drive-api.example.com
	FedAllowed      []string // FED_ALLOWED_HOSTS: if set, only these servers
	FedAllowPrivate bool     // FED_ALLOW_PRIVATE=1: allow private addresses (LAN setups)

	TelegramAPIID     int
	TelegramAPIHash   string
	TelegramPhone     string
	TelegramChannelID int64
}

func (c Config) SessionPath() string { return filepath.Join(c.DataDir, "telegram.session") }
func (c Config) UploadDir() string   { return filepath.Join(c.DataDir, "uploads") }
func (c Config) ThumbDir() string    { return filepath.Join(c.DataDir, "thumbs") }
func (c Config) FedKeyPath() string  { return filepath.Join(c.DataDir, "federation.key") }

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
	c.Federation = os.Getenv("FEDERATION") == "on"
	c.PublicAPIURL = strings.TrimRight(os.Getenv("PUBLIC_API_URL"), "/")
	c.FedAllowPrivate = os.Getenv("FED_ALLOW_PRIVATE") == "1"
	for _, h := range strings.Split(os.Getenv("FED_ALLOWED_HOSTS"), ",") {
		if h = strings.TrimSpace(h); h != "" {
			c.FedAllowed = append(c.FedAllowed, h)
		}
	}
	if c.Federation && (c.PublicAPIURL == "" || c.AllowedOrigin == "") {
		return c, errors.New("FEDERATION=on needs PUBLIC_API_URL and ALLOWED_ORIGIN (the UI's domain is the host in addresses)")
	}
	c.TelegramAPIID, _ = strconv.Atoi(os.Getenv("TELEGRAM_API_ID"))
	c.TelegramChannelID, _ = strconv.ParseInt(os.Getenv("TELEGRAM_CHANNEL_ID"), 10, 64)
	if c.TelegramAPIID == 0 || c.TelegramAPIHash == "" || c.TelegramPhone == "" || c.TelegramChannelID == 0 {
		return c, errors.New("set TELEGRAM_API_ID, TELEGRAM_API_HASH, TELEGRAM_PHONE and TELEGRAM_CHANNEL_ID in .env")
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
