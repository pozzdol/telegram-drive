// Package utils: small HTTP helpers.
package utils

import (
	"encoding/json"
	"github.com/google/uuid"
	"log"
	"mime"
	"net/http"
	"strings"
)

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func WriteErr(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}

// ServerErr logs the real error and sends the client a generic message, so database and
// Telegram internals never reach the browser.
func ServerErr(w http.ResponseWriter, status int, err error) {
	log.Printf("%s: %v", http.StatusText(status), err)
	msg := "internal error"
	if status == http.StatusBadGateway {
		msg = "Telegram request failed, try again"
	}
	WriteErr(w, status, msg)
}

// ReadJSON decodes a JSON body (max 1 MiB); on failure it writes 400 and returns false.
// Requiring Content-Type: application/json makes browsers send a CORS preflight, so other
// sites cannot submit forms or no-cors fetches to the API with the user's cookie.
func ReadJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		WriteErr(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		WriteErr(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

// PathID reads the {id} path segment, a UUID; on failure it writes 400 and returns false.
func PathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		WriteErr(w, http.StatusBadRequest, "invalid id")
		return "", false
	}
	return id.String(), true
}

// CleanName trims a file/folder name and checks it is 1-255 chars.
func CleanName(s string) (string, bool) {
	s = strings.TrimSpace(s)
	return s, s != "" && len(s) <= 255
}
