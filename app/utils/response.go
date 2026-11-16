// Package utils: small HTTP helpers.
package utils

import (
	"encoding/json"
	"net/http"
	"strconv"
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

// ReadJSON decodes a JSON body (max 1 MiB); on failure it writes 400 and returns false.
func ReadJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		WriteErr(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

// PathID parses the {id} path segment; on failure it writes 400 and returns false.
func PathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		WriteErr(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

// CleanName trims a file/folder name and checks it is 1-255 chars.
func CleanName(s string) (string, bool) {
	s = strings.TrimSpace(s)
	return s, s != "" && len(s) <= 255
}
