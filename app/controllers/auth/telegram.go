package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"telegram-drive/app/models"
	"telegram-drive/app/utils"
)

// "Log in with Telegram": the UI opens Telegram's login popup, which returns the user's
// profile signed with the bot token (https://core.telegram.org/widgets/login#checking-authorization).
// A Telegram account only logs in to a drive account it was linked to first; nobody can
// create an account this way.

const telegramAuthMaxAge = time.Hour

// verifyTelegram checks the signature and age of the popup's data and returns the Telegram user id.
func verifyTelegram(botToken string, data map[string]any, now time.Time) (int64, error) {
	hash, _ := data["hash"].(string)
	if botToken == "" || hash == "" {
		return 0, errors.New("missing hash")
	}
	lines := make([]string, 0, len(data))
	for k, v := range data {
		if k == "hash" {
			continue
		}
		switch v := v.(type) {
		case string:
			lines = append(lines, k+"="+v)
		case float64: // JSON numbers: id, auth_date
			lines = append(lines, k+"="+strconv.FormatInt(int64(v), 10))
		default:
			return 0, fmt.Errorf("unexpected field %q", k)
		}
	}
	sort.Strings(lines)
	secret := sha256.Sum256([]byte(botToken))
	mac := hmac.New(sha256.New, secret[:])
	mac.Write([]byte(strings.Join(lines, "\n")))
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(strings.ToLower(hash))) {
		return 0, errors.New("bad signature")
	}
	authDate, _ := data["auth_date"].(float64)
	if age := now.Sub(time.Unix(int64(authDate), 0)); age > telegramAuthMaxAge || age < -time.Minute {
		return 0, errors.New("login data expired")
	}
	id, _ := data["id"].(float64)
	if id <= 0 {
		return 0, errors.New("missing id")
	}
	return int64(id), nil
}

// GET /api/auth/config — public: tells the login page whether to show the Telegram button.
// bot_id is the numeric part of the token before ":", which Telegram's login popup needs; it is not secret.
func (c Controller) Config(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"telegram_bot": nil, "telegram_bot_id": nil}
	if id, _, ok := strings.Cut(c.BotToken, ":"); ok && c.BotName != "" {
		out["telegram_bot"], out["telegram_bot_id"] = c.BotName, id
	}
	utils.WriteJSON(w, http.StatusOK, out)
}

func (c Controller) readTelegram(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if c.BotToken == "" {
		utils.WriteErr(w, http.StatusNotFound, "Telegram login is not configured")
		return 0, false
	}
	var data map[string]any
	if !utils.ReadJSON(w, r, &data) {
		return 0, false
	}
	tgID, err := verifyTelegram(c.BotToken, data, time.Now())
	if err != nil {
		logins.fail(clientIP(r), "telegram:"+clientIP(r)) // per IP only, so nobody can lock others out
		utils.WriteErr(w, http.StatusUnauthorized, "Telegram login could not be verified: "+err.Error())
		return 0, false
	}
	return tgID, true
}

// POST /api/auth/telegram — log in with a linked Telegram account.
func (c Controller) TelegramLogin(w http.ResponseWriter, r *http.Request) {
	if wait := logins.blocked(clientIP(r), "telegram:"+clientIP(r)); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		utils.WriteErr(w, http.StatusTooManyRequests, "too many failed logins, try again later")
		return
	}
	tgID, ok := c.readTelegram(w, r)
	if !ok {
		return
	}
	var uid string
	if err := c.DB.QueryRow(`SELECT id FROM users WHERE telegram_id = $1`, tgID).Scan(&uid); err != nil {
		utils.WriteErr(w, http.StatusForbidden, "this Telegram account is not linked to a drive account")
		return
	}
	c.startSession(w, r, uid)
}

// PUT /api/me/telegram — link the Telegram account from the popup to the logged-in user.
func (c Controller) LinkTelegram(w http.ResponseWriter, r *http.Request, uid string) {
	tgID, ok := c.readTelegram(w, r)
	if !ok {
		return
	}
	if _, err := c.DB.Exec(`UPDATE users SET telegram_id = $1 WHERE id = $2`, tgID, uid); err != nil {
		if models.IsUniqueViolation(err) {
			utils.WriteErr(w, http.StatusConflict, "this Telegram account is already linked to another drive account")
			return
		}
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/me/telegram — unlink; password login keeps working.
func (c Controller) UnlinkTelegram(w http.ResponseWriter, r *http.Request, uid string) {
	c.DB.Exec(`UPDATE users SET telegram_id = NULL WHERE id = $1`, uid)
	w.WriteHeader(http.StatusNoContent)
}
