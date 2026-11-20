package routes

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http/httptest"
	"os"
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
	idA := out["id"].(string)
	c, out = call("POST", "/api/folders", `{"name":"B","parent_id":"`+idA+`"}`)
	expect(201, c, "create B in A")
	idB := out["id"].(string)

	c, _ = call("PATCH", "/api/folders/"+idA, `{"parent_id":"`+idB+`"}`)
	expect(400, c, "move A into its child B")
	c, out = call("GET", "/api/list?folder_id="+idA, "")
	expect(200, c, "list A")
	if n := len(out["folders"].([]any)); n != 1 {
		t.Fatalf("A should contain 1 folder, has %d", n)
	}
	c, _ = call("PATCH", "/api/folders/"+idB, `{"parent_id":"","name":"B2"}`)
	expect(204, c, "move B to root")
	c, _ = call("POST", "/api/delete", `{"folders":["`+idA+`"]}`)
	expect(200, c, "delete empty A")

	c, out = call("GET", "/api/list?folder_id="+idB, "")
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

	// Bulk move: several items into one folder; never a folder into itself or its subfolder.
	_, out = call("POST", "/api/folders", `{"name":"M1"}`)
	idM1 := out["id"].(string)
	_, out = call("POST", "/api/folders", `{"name":"M2"}`)
	idM2 := out["id"].(string)
	_, out = call("POST", "/api/folders", `{"name":"M1child","parent_id":"`+idM1+`"}`)
	idM1c := out["id"].(string)
	c, _ = call("POST", "/api/move", `{"folders":["`+idM1+`"],"to":"`+idM1c+`"}`)
	expect(400, c, "move folder into its own child")
	c, _ = call("POST", "/api/move", `{"folders":["`+idM1+`","`+idM2+`"],"to":"`+idM1+`"}`)
	expect(400, c, "move folder into itself")
	c, _ = call("POST", "/api/move", `{"folders":["`+idM2+`"],"to":"`+models.NewID()+`"}`)
	expect(404, c, "move into unknown folder")
	c, out = call("POST", "/api/move", `{"folders":["`+idM2+`"],"to":"`+idM1c+`"}`)
	expect(200, c, "bulk move")
	if out["folders"].(float64) != 1 {
		t.Fatalf("bulk move count wrong: %v", out)
	}
	_, out = call("GET", "/api/list?folder_id="+idM1c, "")
	if fs := out["folders"].([]any); len(fs) != 1 || fs[0].(map[string]any)["name"] != "M2" {
		t.Fatalf("M2 should now be inside M1child: %v", out)
	}
	c, _ = call("POST", "/api/move", `{"folders":["`+idM1+`"],"to":""}`)
	expect(200, c, "move to root")
	call("POST", "/api/delete", `{"folders":["`+idM1+`"]}`)

	// Bulk delete removes a folder with everything below it, and only the caller's folders.
	_, out = call("POST", "/api/folders", `{"name":"X"}`)
	idX := out["id"].(string)
	_, out = call("POST", "/api/folders", `{"name":"Y","parent_id":"`+idX+`"}`)
	idY := out["id"].(string)
	call("POST", "/api/folders", `{"name":"Z","parent_id":"`+idY+`"}`)
	c, _ = call("POST", "/api/delete", `{}`)
	expect(400, c, "bulk delete nothing")
	c, out = call("POST", "/api/delete", `{"folders":["`+idX+`","`+models.NewID()+`"]}`)
	expect(200, c, "bulk delete folder tree")
	if out["folders"].(float64) != 3 || out["files"].(float64) != 0 {
		t.Fatalf("bulk delete counts wrong: %v", out)
	}
	c, _ = call("GET", "/api/list?folder_id="+idY, "")
	expect(404, c, "list deleted subfolder")

	// Trash: only the picked item is listed; restoring brings back everything inside it.
	trashIDs := func() []string {
		_, out := call("GET", "/api/trash", "")
		ids := []string{}
		for _, it := range out["items"].([]any) {
			ids = append(ids, it.(map[string]any)["id"].(string))
		}
		return ids
	}
	if ids := trashIDs(); len(ids) != 3 || ids[0] != idX { // X, then M1 and A from earlier
		t.Fatalf("trash should list X first of 3 items: %v", ids)
	}
	c, _ = call("POST", "/api/folders", `{"name":"in trash","parent_id":"`+idY+`"}`)
	expect(404, c, "create a folder inside a trashed folder")
	c, _ = call("POST", "/api/trash/restore", `{"ids":["`+idX+`"]}`)
	expect(200, c, "restore X")
	_, out = call("GET", "/api/list?folder_id="+idY, "")
	if len(out["folders"].([]any)) != 1 {
		t.Fatalf("restored Y should still hold Z: %v", out)
	}
	// Y trashed alone, then its parent X: restoring Y alone brings it back at the root.
	call("POST", "/api/delete", `{"folders":["`+idY+`"]}`)
	call("POST", "/api/delete", `{"folders":["`+idX+`"]}`)
	c, _ = call("POST", "/api/trash/restore", `{"ids":["`+idY+`"]}`)
	expect(200, c, "restore Y")
	_, out = call("GET", "/api/list?folder_id="+idY, "")
	if p := out["path"].([]any); len(p) != 1 {
		t.Fatalf("Y should be back at the root: %v", out["path"])
	}
	call("POST", "/api/delete", `{"folders":["`+idY+`"]}`)
	call("POST", "/api/trash/restore", `{"ids":["`+idY+`"]}`) // X is still in the trash
	c, _ = call("POST", "/api/trash/purge", `{"ids":["`+idX+`"]}`)
	expect(200, c, "purge X")
	c, _ = call("GET", "/api/list?folder_id="+idY, "")
	expect(200, c, "Y survives purging its old parent")
	c, _ = call("POST", "/api/trash/purge", `{"all":true}`)
	expect(200, c, "empty the trash")
	if ids := trashIDs(); len(ids) != 0 {
		t.Fatalf("trash should be empty: %v", ids)
	}
	var left int
	db.QueryRow(`SELECT count(*) FROM folders WHERE trashed_at IS NOT NULL`).Scan(&left)
	if left != 0 {
		t.Fatalf("%d trashed folders left after emptying the trash", left)
	}

	// Family accounts: admin adds a user, who sees an empty private drive and no admin page.
	c, out = call("GET", "/api/me", "")
	if out["is_admin"] != true {
		t.Fatalf("first user should be admin: %v", out)
	}
	c, out = call("POST", "/api/admin/users", `{"username":"ibu","password":"pendek"}`)
	expect(400, c, "short password")
	c, out = call("POST", "/api/admin/users", `{"username":"ibu","password":"rahasia456"}`)
	expect(201, c, "admin creates user")
	idIbu := out["id"].(string)
	c, _ = call("POST", "/api/admin/users", `{"username":"ibu","password":"rahasia456"}`)
	expect(409, c, "duplicate username")
	_, out = call("GET", "/api/me", "")
	c, _ = call("PATCH", "/api/admin/users/"+out["id"].(string), `{"is_admin":false}`)
	expect(400, c, "admin demotes self")
	c, _ = call("DELETE", "/api/admin/users/"+out["id"].(string), "")
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

	const f1, f2, f3, f4 = "0190a000-0000-7000-8000-000000000001", "0190a000-0000-7000-8000-000000000002", "0190a000-0000-7000-8000-000000000003", "0190a000-0000-7000-8000-000000000004"
	if _, err := db.Exec(`INSERT INTO files (id, user_id, name, size, mime, msg_id) SELECT $1::uuid, id, 'a.mp4', 10, 'video/mp4', 1 FROM users UNION ALL SELECT $2::uuid, id, 'b.txt', 10, 'text/plain', 2 FROM users`, f1, f2); err != nil {
		t.Fatal(err)
	}
	var jpg bytes.Buffer
	jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 32, 18)), nil)

	// A file whose message was deleted in Telegram is listed as missing, and its record can be
	// removed without Telegram (TG is nil in tests, so any Telegram call would panic).
	if _, err := db.Exec(`INSERT INTO files (id, user_id, name, size, mime, msg_id, missing_at) SELECT $1::uuid, id, 'gone.jpg', 10, 'image/jpeg', 3, 1 FROM users UNION ALL SELECT $2::uuid, id, 'gone2.jpg', 10, 'image/jpeg', 4, 1 FROM users`, f3, f4); err != nil {
		t.Fatal(err)
	}
	listReq := httptest.NewRequest("GET", "/api/list", nil)
	listReq.Header.Set("Authorization", "Bearer "+tok)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, listReq)
	var list struct {
		Files []struct {
			ID      string
			Missing bool
		}
	}
	json.Unmarshal(rec.Body.Bytes(), &list)
	for _, f := range list.Files {
		if want := f.ID == f3 || f.ID == f4; f.Missing != want {
			t.Fatalf("file %s: missing = %v, want %v", f.ID, f.Missing, want)
		}
	}
	if c := call("POST", "/api/delete", tok, []byte(`{"files":["`+f3+`","`+f4+`"]}`)); c != 200 {
		t.Fatalf("trash missing files: want 200, got %d", c)
	}
	if c := call("POST", "/api/trash/purge", tok, []byte(`{"ids":["`+f3+`","`+f4+`"]}`)); c != 200 {
		t.Fatalf("purge missing files: want 200, got %d", c)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM files WHERE id IN ($1, $2)`, f3, f4).Scan(&n)
	if n != 0 {
		t.Fatalf("purged files still have %d rows", n)
	}

	for _, tc := range []struct {
		method, path string
		body         []byte
		want         int
	}{
		{"GET", "/api/files/" + f1 + "/thumb", nil, 404},
		{"PUT", "/api/files/" + f1 + "/thumb", []byte("not a jpeg"), 400},
		{"PUT", "/api/files/" + f2 + "/thumb", jpg.Bytes(), 400},
		{"PUT", "/api/files/" + f1 + "/thumb", jpg.Bytes(), 204},
		{"GET", "/api/files/" + f1 + "/thumb", nil, 200},
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
