package routes

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"telegram-drive/app"
	"telegram-drive/app/models"
)

// testDB gives an empty database. Set TEST_DATABASE_URL to a throwaway PostgreSQL database:
// every table in its public schema is dropped.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	raw, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	db, err := models.Open(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// Auth and folder rules, no Telegram needed.
func TestAuthAndFolders(t *testing.T) {
	db := testDB(t)
	h := New(&app.App{DB: db, UploadDir: t.TempDir(), ThumbDir: t.TempDir(), MaxUpload: 1 << 30, Origin: "https://drive.example.com"})

	token := ""
	call := func(method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if strings.HasPrefix(body, "{") {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	expect := func(want, got int, what string) {
		t.Helper()
		if got != want {
			t.Fatalf("%s: want %d, got %d", what, want, got)
		}
	}

	c, _ := call("GET", "/api/list", "")
	expect(401, c, "list without token")
	c, _ = call("POST", "/api/auth/register", `{"username":"fikri","password":"rahasia123"}`)
	expect(201, c, "first register")
	c, _ = call("POST", "/api/auth/register", `{"username":"other","password":"rahasia123"}`)
	expect(403, c, "second register")
	c, _ = call("POST", "/api/auth/login", `{"username":"fikri","password":"salah12345"}`)
	expect(401, c, "wrong password")
	c, out := call("POST", "/api/auth/login", `{"username":"fikri","password":"rahasia123"}`)
	expect(200, c, "login")
	token = out["token"].(string)

	_, out = call("POST", "/api/folders", `{"name":"A"}`)
	idA := int(out["id"].(float64))
	c, out = call("POST", "/api/folders", `{"name":"B","parent_id":`+itoa(idA)+`}`)
	expect(201, c, "create B in A")
	idB := int(out["id"].(float64))

	c, _ = call("PATCH", "/api/folders/"+itoa(idA), `{"parent_id":`+itoa(idB)+`}`)
	expect(400, c, "move A into its child B")
	c, _ = call("DELETE", "/api/folders/"+itoa(idA), "")
	expect(409, c, "delete non-empty A")

	c, out = call("GET", "/api/list?folder_id="+itoa(idA), "")
	expect(200, c, "list A")
	if n := len(out["folders"].([]any)); n != 1 {
		t.Fatalf("A should contain 1 folder, has %d", n)
	}
	c, _ = call("PATCH", "/api/folders/"+itoa(idB), `{"parent_id":0,"name":"B2"}`)
	expect(204, c, "move B to root")
	c, _ = call("DELETE", "/api/folders/"+itoa(idA), "")
	expect(204, c, "delete empty A")

	c, out = call("GET", "/api/list?folder_id="+itoa(idB), "")
	if p := out["path"].([]any); len(p) != 1 || p[0].(map[string]any)["name"] != "B2" {
		t.Fatalf("breadcrumb path wrong: %v", out["path"])
	}

	// Chunked upload: offsets must be sequential; a replayed chunk cannot advance twice.
	// (The final chunk would start the Telegram send, so stop before it.)
	c, out = call("POST", "/api/uploads", `{"name":"x.bin","size":10}`)
	expect(201, c, "create upload")
	up := "/api/uploads/" + out["id"].(string)
	c, _ = call("PUT", up+"?offset=5", "abcde")
	expect(409, c, "chunk at wrong offset")
	c, out = call("PUT", up+"?offset=0", "abcd")
	expect(200, c, "first chunk")
	c, _ = call("PUT", up+"?offset=0", "abcd")
	expect(409, c, "replayed chunk")
	c, _ = call("PUT", up+"?offset=4", "0123456789")
	expect(400, c, "chunk past file size")
	c, out = call("GET", up, "")
	if out["received"].(float64) != 4 || out["status"] != "receiving" {
		t.Fatalf("upload status wrong: %v", out)
	}

	// CSRF: a browser request from another origin, or a non-JSON body, never changes anything.
	csrf := httptest.NewRequest("POST", "/api/folders", strings.NewReader(`{"name":"evil"}`))
	csrf.Header.Set("Content-Type", "application/json")
	csrf.Header.Set("Origin", "https://evil.fkriachmd.qzz.io")
	csrf.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, csrf)
	expect(403, rec.Code, "POST from foreign origin")
	plain := httptest.NewRequest("POST", "/api/folders", strings.NewReader(`{"name":"evil"}`))
	plain.Header.Set("Content-Type", "text/plain")
	plain.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, plain)
	expect(415, rec.Code, "POST with text/plain body")

	// Bulk delete removes a folder with everything below it, and only the caller's folders.
	_, out = call("POST", "/api/folders", `{"name":"X"}`)
	idX := itoa(int(out["id"].(float64)))
	_, out = call("POST", "/api/folders", `{"name":"Y","parent_id":`+idX+`}`)
	idY := itoa(int(out["id"].(float64)))
	call("POST", "/api/folders", `{"name":"Z","parent_id":`+idY+`}`)
	c, _ = call("POST", "/api/delete", `{}`)
	expect(400, c, "bulk delete nothing")
	c, out = call("POST", "/api/delete", `{"folders":[`+idX+`, 999999]}`)
	expect(200, c, "bulk delete folder tree")
	if out["folders"].(float64) != 3 || out["files"].(float64) != 0 {
		t.Fatalf("bulk delete counts wrong: %v", out)
	}
	c, _ = call("GET", "/api/list?folder_id="+idY, "")
	expect(404, c, "list deleted subfolder")

	// Family accounts: admin adds a user, who sees an empty private drive and no admin page.
	c, out = call("GET", "/api/me", "")
	if out["is_admin"] != true {
		t.Fatalf("first user should be admin: %v", out)
	}
	c, out = call("POST", "/api/admin/users", `{"username":"ibu","password":"pendek"}`)
	expect(400, c, "short password")
	c, out = call("POST", "/api/admin/users", `{"username":"ibu","password":"rahasia456"}`)
	expect(201, c, "admin creates user")
	idIbu := itoa(int(out["id"].(float64)))
	c, _ = call("POST", "/api/admin/users", `{"username":"ibu","password":"rahasia456"}`)
	expect(409, c, "duplicate username")
	_, out = call("GET", "/api/me", "")
	c, _ = call("PATCH", "/api/admin/users/"+itoa(int(out["id"].(float64))), `{"is_admin":false}`)
	expect(400, c, "admin demotes self")
	c, _ = call("DELETE", "/api/admin/users/"+itoa(int(out["id"].(float64))), "")
	expect(400, c, "admin deletes self")

	adminToken := token
	_, out = call("POST", "/api/auth/login", `{"username":"ibu","password":"rahasia456"}`)
	token = out["token"].(string)
	c, out = call("GET", "/api/list", "")
	if len(out["folders"].([]any)) != 0 {
		t.Fatalf("new user should see an empty drive, got %v", out)
	}
	c, _ = call("GET", "/api/admin/users", "")
	expect(403, c, "non-admin lists users")
	c, _ = call("PUT", "/api/me/password", `{"current":"salah","new":"barubaru123"}`)
	expect(403, c, "change password with wrong current")
	c, _ = call("PUT", "/api/me/password", `{"current":"rahasia456","new":"barubaru123"}`)
	expect(204, c, "change own password")

	token = adminToken
	c, _ = call("PATCH", "/api/admin/users/"+idIbu, `{"username":"Ibu","password":"reset12345"}`)
	expect(204, c, "admin renames and resets")
	c, out = call("GET", "/api/admin/users", "")
	if us := out["users"].([]any); len(us) != 2 || us[1].(map[string]any)["username"] != "Ibu" {
		t.Fatalf("user list wrong: %v", out)
	}
	c, _ = call("DELETE", "/api/admin/users/"+idIbu, "")
	expect(204, c, "delete user with empty drive")

	c, _ = call("POST", "/api/auth/logout", "")
	expect(204, c, "logout")
	c, _ = call("GET", "/api/list", "")
	expect(401, c, "list after logout")
}

var itoa = strconv.Itoa

// Video previews come from the browser: only valid JPEGs for video files are kept.
func TestVideoThumb(t *testing.T) {
	db := testDB(t)
	h := New(&app.App{DB: db, UploadDir: t.TempDir(), ThumbDir: t.TempDir(), MaxUpload: 1 << 30})
	call := func(method, path, token string, body []byte) int {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		if bytes.HasPrefix(body, []byte("{")) {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	call("POST", "/api/auth/register", "", []byte(`{"username":"fikri","password":"rahasia123"}`))
	login := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"username":"fikri","password":"rahasia123"}`))
	login.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, login)
	var out map[string]string
	json.Unmarshal(rec.Body.Bytes(), &out)
	tok := out["token"]

	db.Exec(`INSERT INTO files (id, user_id, name, size, mime, msg_id) VALUES (1, 1, 'a.mp4', 10, 'video/mp4', 1), (2, 1, 'b.txt', 10, 'text/plain', 2)`)
	var jpg bytes.Buffer
	jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 32, 18)), nil)

	for _, tc := range []struct {
		method, path string
		body         []byte
		want         int
	}{
		{"GET", "/api/files/1/thumb", nil, 404},
		{"PUT", "/api/files/1/thumb", []byte("not a jpeg"), 400},
		{"PUT", "/api/files/2/thumb", jpg.Bytes(), 400},
		{"PUT", "/api/files/1/thumb", jpg.Bytes(), 204},
		{"GET", "/api/files/1/thumb", nil, 200},
	} {
		if got := call(tc.method, tc.path, tok, tc.body); got != tc.want {
			t.Fatalf("%s %s: want %d, got %d", tc.method, tc.path, tc.want, got)
		}
	}
}

// Ten failed logins block the IP and the username for a while.
func TestLoginRateLimit(t *testing.T) {
	db := testDB(t)
	h := New(&app.App{DB: db, UploadDir: t.TempDir(), ThumbDir: t.TempDir(), MaxUpload: 1 << 30})
	try := func() int {
		req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"username":"ratelimit-test","password":"wrong-password"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("CF-Connecting-IP", "203.0.113.77")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := 0; i < 10; i++ {
		if c := try(); c != 401 {
			t.Fatalf("attempt %d: want 401, got %d", i+1, c)
		}
	}
	if c := try(); c != 429 {
		t.Fatalf("11th attempt: want 429, got %d", c)
	}
}
