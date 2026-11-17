package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func TestVerifyTelegram(t *testing.T) {
	const token = "123456:test-token"
	now := time.Unix(1_800_000_000, 0)
	sign := func(check string) string {
		secret := sha256.Sum256([]byte(token))
		m := hmac.New(sha256.New, secret[:])
		m.Write([]byte(check))
		return hex.EncodeToString(m.Sum(nil))
	}
	data := func(authDate float64, hash string) map[string]any {
		return map[string]any{"id": float64(42), "first_name": "Fikri", "username": "fikri", "auth_date": authDate, "hash": hash}
	}
	good := sign("auth_date=1800000000\nfirst_name=Fikri\nid=42\nusername=fikri")

	if id, err := verifyTelegram(token, data(1_800_000_000, good), now); err != nil || id != 42 {
		t.Fatalf("valid data: id %d, err %v", id, err)
	}
	if _, err := verifyTelegram("123456:other", data(1_800_000_000, good), now); err == nil {
		t.Fatal("accepted a signature from another bot")
	}
	tampered := data(1_800_000_000, good)
	tampered["id"] = float64(43)
	if _, err := verifyTelegram(token, tampered, now); err == nil {
		t.Fatal("accepted a changed id")
	}
	old := sign("auth_date=1799990000\nfirst_name=Fikri\nid=42\nusername=fikri")
	if _, err := verifyTelegram(token, data(1_799_990_000, old), now); err == nil {
		t.Fatal("accepted login data older than an hour")
	}
}
