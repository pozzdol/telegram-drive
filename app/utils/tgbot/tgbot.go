// Package tgbot sends messages through the Telegram Bot API (the bot behind "Log in with
// Telegram"), for notifications. A user only gets them after allowing the bot to write to them.
package tgbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

var client = &http.Client{Timeout: 15 * time.Second}

// Send posts a plain-text message (no Markdown, so names and comments need no escaping).
func Send(ctx context.Context, token string, chatID int64, text string) error {
	body, _ := json.Marshal(map[string]any{
		"chat_id":              chatID,
		"text":                 text,
		"link_preview_options": map[string]bool{"is_disabled": true},
	})
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.telegram.org/bot"+token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram bot: request failed") // the URL holds the token: never log err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		var e struct {
			Description string `json:"description"`
		}
		json.NewDecoder(res.Body).Decode(&e)
		return fmt.Errorf("telegram bot: %d %s", res.StatusCode, e.Description)
	}
	return nil
}
