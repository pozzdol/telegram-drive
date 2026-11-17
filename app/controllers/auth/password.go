package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const pbkdf2Iter = 600_000 // OWASP 2023 recommendation for PBKDF2-SHA256

func hashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	rand.Read(salt)
	key, err := pbkdf2.Key(sha256.New, pw, salt, pbkdf2Iter, 32)
	if err != nil {
		return "", err
	}
	b64 := base64.RawStdEncoding.EncodeToString
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Iter, b64(salt), b64(key)), nil
}

func checkPassword(stored, pw string) bool {
	p := strings.Split(stored, "$")
	if len(p) != 4 {
		return false
	}
	iter, err1 := strconv.Atoi(p[1])
	salt, err2 := base64.RawStdEncoding.DecodeString(p[2])
	want, err3 := base64.RawStdEncoding.DecodeString(p[3])
	if err1 != nil || err2 != nil || err3 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

const MinPassword = 8

// HashPassword is exported for the admin user controller.
func HashPassword(pw string) (string, error) {
	if len(pw) < MinPassword {
		return "", fmt.Errorf("password min %d chars", MinPassword)
	}
	return hashPassword(pw)
}

// SetPassword replaces the hash; the caller decides which sessions to end.
func SetPassword(db *sql.DB, uid int64, pw string) error {
	hash, err := HashPassword(pw)
	if err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE users SET password_hash = $1 WHERE id = $2`, hash, uid)
	return err
}

const sessionTTL = 30 * 24 * time.Hour

// SessionHash is the stored form of the session token this request carries.
func SessionHash(r *http.Request) string { return hashToken(tokenFrom(r)) }

func hashToken(t string) string {
	s := sha256.Sum256([]byte(t))
	return hex.EncodeToString(s[:])
}

// newSession stores only the token's hash, so a leaked DB file cannot be used to log in.
func newSession(db *sql.DB, uid int64) (string, error) {
	now := time.Now()
	db.Exec(`DELETE FROM sessions WHERE expires_at <= $1`, now.Unix())
	tok := rand.Text()
	_, err := db.Exec(`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		hashToken(tok), uid, now.Add(sessionTTL).Unix())
	return tok, err
}

func userForToken(db *sql.DB, tok string) (int64, error) {
	if tok == "" {
		return 0, errors.New("no token")
	}
	var uid int64
	err := db.QueryRow(`SELECT user_id FROM sessions WHERE token_hash = $1 AND expires_at > $2`,
		hashToken(tok), time.Now().Unix()).Scan(&uid)
	return uid, err
}
