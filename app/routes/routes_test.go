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
		// In the trash the owner may still look at a file (the trash page previews it), not change it.
		{"POST", "/api/delete", []byte(`{"files":["` + f1 + `"]}`), 200},
		{"GET", "/api/files/" + f1 + "/thumb", nil, 200},
		{"PATCH", "/api/files/" + f1, []byte(`{"name":"x.mp4"}`), 404},
		{"PUT", "/api/files/" + f1 + "/thumb", jpg.Bytes(), 204},
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

// Sharing: a folder shared with another account covers everything inside it; the role decides
// what they may do, and editors work only inside the owner's tree.
func TestSharing(t *testing.T) {
	db := testDB(t)
	h := New(&app.App{DB: db, UploadDir: t.TempDir(), ThumbDir: t.TempDir(), MaxUpload: 1 << 30})
	call := func(token, method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if strings.HasPrefix(body, "{") {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Authorization", "Bearer "+token)
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
	login := func(name, pw string) string {
		_, out := call("", "POST", "/api/auth/login", `{"username":"`+name+`","password":"`+pw+`"}`)
		return out["token"].(string)
	}
	call("", "POST", "/api/auth/register", `{"username":"ayah","password":"rahasia123"}`)
	a := login("ayah", "rahasia123")
	_, out := call(a, "POST", "/api/admin/users", `{"username":"ibu","password":"rahasia123"}`)
	idB := out["id"].(string)
	_, out = call(a, "POST", "/api/admin/users", `{"username":"adik","password":"rahasia123"}`)
	idC := out["id"].(string)
	b, c := login("ibu", "rahasia123"), login("adik", "rahasia123")

	_, out = call(a, "POST", "/api/folders", `{"name":"Keluarga"}`)
	p := out["id"].(string)
	_, out = call(a, "POST", "/api/folders", `{"name":"Liburan","parent_id":"`+p+`"}`)
	q := out["id"].(string)

	code, _ := call(b, "GET", "/api/list?folder_id="+p, "")
	expect(404, code, "list a folder nobody shared")
	code, _ = call(b, "GET", "/api/shares/"+p, "")
	expect(404, code, "see who has access without access")
	code, _ = call(a, "PUT", "/api/shares/"+p, `{"user_id":"`+idB+`","role":"owner"}`)
	expect(400, code, "share with the owner role")
	code, _ = call(a, "PUT", "/api/shares/"+p, `{"user_id":"`+idB+`","role":"viewer"}`)
	expect(204, code, "share as viewer")

	_, out = call(b, "GET", "/api/shared", "")
	if fs := out["folders"].([]any); len(fs) != 1 || fs[0].(map[string]any)["owner"] != "ayah" {
		t.Fatalf("shared with me: %v", out)
	}
	code, out = call(b, "GET", "/api/list?folder_id="+q, "")
	expect(200, code, "viewer lists a subfolder")
	if out["role"] != "viewer" || out["shared"] != true || len(out["path"].([]any)) != 2 {
		t.Fatalf("viewer list of the subfolder: %v", out)
	}
	code, _ = call(b, "PATCH", "/api/folders/"+q, `{"name":"x"}`)
	expect(404, code, "viewer renames")
	code, _ = call(b, "POST", "/api/folders", `{"name":"x","parent_id":"`+p+`"}`)
	expect(404, code, "viewer creates a folder")
	_, out = call(b, "POST", "/api/delete", `{"folders":["`+q+`"]}`)
	if out["folders"].(float64) != 0 {
		t.Fatalf("viewer deleted something: %v", out)
	}

	call(a, "PUT", "/api/shares/"+p, `{"user_id":"`+idB+`","role":"editor"}`)
	code, out = call(b, "POST", "/api/folders", `{"name":"Dari ibu","parent_id":"`+p+`"}`)
	expect(201, code, "editor creates a folder")
	r := out["id"].(string)
	_, out = call(a, "GET", "/api/list?folder_id="+p, "")
	if len(out["folders"].([]any)) != 2 || out["role"] != "owner" {
		t.Fatalf("owner should see the editor's folder: %v", out)
	}
	_, out = call(b, "POST", "/api/move", `{"folders":["`+q+`"],"to":""}`)
	if out["folders"].(float64) != 0 {
		t.Fatalf("editor moved the owner's folder out of their tree: %v", out)
	}
	_, out = call(b, "POST", "/api/move", `{"folders":["`+q+`"],"to":"`+r+`"}`)
	if out["folders"].(float64) != 1 {
		t.Fatalf("editor should move within the shared folder: %v", out)
	}
	_, out = call(b, "POST", "/api/delete", `{"folders":["`+q+`"]}`)
	if out["folders"].(float64) != 1 {
		t.Fatalf("editor should delete: %v", out)
	}
	_, out = call(a, "GET", "/api/trash", "")
	if its := out["items"].([]any); len(its) != 1 || its[0].(map[string]any)["id"] != q {
		t.Fatalf("deleted folder should be in the owner's trash: %v", out)
	}
	code, _ = call(b, "POST", "/api/trash/restore", `{"ids":["`+q+`"]}`)
	expect(200, code, "editor undoes the delete")
	code, _ = call(b, "GET", "/api/list?folder_id="+q, "")
	expect(200, code, "restored folder is back")
	code, _ = call(b, "POST", "/api/trash/purge", `{"ids":["`+q+`"]}`)
	expect(200, code, "purge someone else's item (ignored)")

	code, _ = call(b, "PUT", "/api/shares/"+p, `{"user_id":"`+idC+`","role":"viewer"}`)
	expect(204, code, "editor shares further")
	code, _ = call(c, "GET", "/api/list?folder_id="+q, "")
	expect(200, code, "reshared viewer lists")
	_, out = call(c, "GET", "/api/shares/"+p, "")
	if out["role"] != "viewer" || len(out["people"].([]any)) != 2 || out["owner"].(map[string]any)["username"] != "ayah" {
		t.Fatalf("access list: %v", out)
	}
	code, _ = call(c, "DELETE", "/api/shares/"+p+"/"+idB, "")
	expect(404, code, "viewer removes someone else")
	code, _ = call(c, "DELETE", "/api/shares/"+p+"/"+idC, "")
	expect(204, code, "viewer leaves")
	code, _ = call(c, "GET", "/api/list?folder_id="+p, "")
	expect(404, code, "after leaving")

	// Purging a shared folder for good removes its shares too.
	call(a, "POST", "/api/delete", `{"folders":["`+p+`"]}`)
	call(a, "POST", "/api/trash/purge", `{"all":true}`)
	var n int
	db.QueryRow(`SELECT count(*) FROM shares`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d shares left after purging", n)
	}
}

// Public links: guests without an account see the linked folder with the link's role; the
// owner keeps full access whatever the link settings are.
func TestPublicLinks(t *testing.T) {
	db := testDB(t)
	h := New(&app.App{DB: db, UploadDir: t.TempDir(), ThumbDir: t.TempDir(), MaxUpload: 1 << 30})
	call := func(auth, method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if strings.HasPrefix(body, "{") {
			req.Header.Set("Content-Type", "application/json")
		}
		if k, v, ok := strings.Cut(auth, ":"); ok {
			req.Header.Set(k, v)
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
	call("", "POST", "/api/auth/register", `{"username":"ayah","password":"rahasia123"}`)
	_, out := call("", "POST", "/api/auth/login", `{"username":"ayah","password":"rahasia123"}`)
	a := "Authorization:Bearer " + out["token"].(string)
	_, out = call(a, "POST", "/api/folders", `{"name":"Pernikahan"}`)
	p := out["id"].(string)
	_, out = call(a, "POST", "/api/folders", `{"name":"Akad","parent_id":"`+p+`"}`)
	q := out["id"].(string)

	code, _ := call("", "GET", "/api/s/nope", "")
	expect(404, code, "unknown link")
	code, out = call(a, "PUT", "/api/links/"+p, `{"enabled":true,"role":"viewer"}`)
	expect(200, code, "open the link")
	token := out["token"].(string)
	code, out = call("", "GET", "/api/s/"+token, "")
	expect(200, code, "link info")
	if out["name"] != "Pernikahan" || out["password"] != false || out["require_name"] != false || out["owner"] != "ayah" {
		t.Fatalf("link info: %v", out)
	}
	session := func(body string) (int, string) {
		code, out := call("", "POST", "/api/s/"+token+"/session", body)
		g, _ := out["guest"].(string)
		return code, "X-Guest:" + g
	}
	_, g := session(`{}`)
	code, out = call(g, "GET", "/api/list?folder_id="+q, "")
	expect(200, code, "guest lists a subfolder")
	if p := out["path"].([]any); len(p) != 2 || out["role"] != "viewer" {
		t.Fatalf("guest path should start at the linked folder: %v", out)
	}
	code, _ = call(g, "GET", "/api/list", "")
	expect(404, code, "guest lists the owner's root")
	code, _ = call(g, "POST", "/api/folders", `{"name":"x","parent_id":"`+p+`"}`)
	expect(404, code, "viewer guest creates a folder")
	code, _ = call(g, "GET", "/api/shared", "")
	expect(401, code, "guest opens a logged-in page")

	// Editor link with a password and a name: old guests must open it again.
	call(a, "PUT", "/api/links/"+p, `{"role":"editor","password":"kunci","require_name":true,"max_file":100,"upload_limit":120}`)
	code, _ = call(g, "GET", "/api/list?folder_id="+p, "")
	expect(401, code, "guest after the password changed")
	code, _ = session(`{"password":"salah","name":"Budi"}`)
	expect(403, code, "wrong link password")
	code, _ = session(`{"password":"kunci"}`)
	expect(400, code, "no name")
	code, g = session(`{"password":"kunci","name":"Budi"}`)
	expect(200, code, "open with password and name")
	code, out = call(g, "POST", "/api/folders", `{"name":"Dari Budi","parent_id":"`+p+`"}`)
	expect(201, code, "editor guest creates a folder")
	r := out["id"].(string)
	var owner, guestName string
	db.QueryRow(`SELECT u.username, f.guest_name FROM folders f JOIN users u ON u.id = f.user_id WHERE f.id = $1`, r).Scan(&owner, &guestName)
	if owner != "ayah" || guestName != "Budi" {
		t.Fatalf("guest folder: owner %q, guest %q", owner, guestName)
	}
	code, _ = call(g, "POST", "/api/uploads", `{"name":"a.jpg","size":200,"folder_id":"`+p+`"}`)
	expect(413, code, "guest file over the per-file cap")
	code, _ = call(g, "POST", "/api/uploads", `{"name":"a.jpg","size":80,"folder_id":"`+p+`"}`)
	expect(201, code, "guest upload")
	code, _ = call(g, "POST", "/api/uploads", `{"name":"b.jpg","size":80,"folder_id":"`+p+`"}`)
	expect(413, code, "guest upload over the link total")
	code, _ = call(g, "POST", "/api/move", `{"folders":["`+q+`"],"to":""}`)
	expect(404, code, "guest moves a folder out of the link")
	_, out = call(g, "POST", "/api/move", `{"folders":["`+q+`"],"to":"`+r+`"}`)
	if out["folders"].(float64) != 1 {
		t.Fatalf("guest move inside the link: %v", out)
	}
	_, out = call(g, "POST", "/api/delete", `{"folders":["`+q+`"]}`)
	if out["folders"].(float64) != 1 {
		t.Fatalf("guest delete: %v", out)
	}
	_, out = call(a, "GET", "/api/trash", "")
	if len(out["items"].([]any)) != 1 {
		t.Fatalf("the guest's delete should be in the owner's trash: %v", out)
	}

	// A guest editor may change the link, even shut it; the owner is never locked out.
	code, _ = call(g, "PUT", "/api/links/"+p, `{"enabled":false}`)
	expect(200, code, "guest restricts the link")
	code, _ = call(g, "GET", "/api/list?folder_id="+p, "")
	expect(401, code, "guest after the link was restricted")
	code, _ = call(a, "GET", "/api/list?folder_id="+p, "")
	expect(200, code, "owner after the link was restricted")
	_, out = call(a, "PUT", "/api/links/"+p, `{"enabled":true,"password":""}`)
	if len(out["log"].([]any)) < 3 || out["password"] != false {
		t.Fatalf("access log or password: %v", out)
	}
	call(a, "POST", "/api/links/"+p+"/reset", "")
	code, _ = call("", "GET", "/api/s/"+token, "")
	expect(404, code, "old link after reset")
}

// Comments: commenters and above may write; authors and editors may delete; guests too.
func TestComments(t *testing.T) {
	db := testDB(t)
	h := New(&app.App{DB: db, UploadDir: t.TempDir(), ThumbDir: t.TempDir(), MaxUpload: 1 << 30})
	call := func(auth, method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if strings.HasPrefix(body, "{") {
			req.Header.Set("Content-Type", "application/json")
		}
		if k, v, ok := strings.Cut(auth, ":"); ok {
			req.Header.Set(k, v)
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
	login := func(name string) string {
		_, out := call("", "POST", "/api/auth/login", `{"username":"`+name+`","password":"rahasia123"}`)
		return "Authorization:Bearer " + out["token"].(string)
	}
	call("", "POST", "/api/auth/register", `{"username":"ayah","password":"rahasia123"}`)
	a := login("ayah")
	_, out := call(a, "POST", "/api/admin/users", `{"username":"ibu","password":"rahasia123"}`)
	idB := out["id"].(string)
	b := login("ibu")
	const f = "0190a000-0000-7000-8000-0000000000c1"
	if _, err := db.Exec(`INSERT INTO files (id, user_id, name, size, mime, msg_id) SELECT $1::uuid, id, 'foto.jpg', 10, 'image/jpeg', 1 FROM users WHERE username = 'ayah'`, f); err != nil {
		t.Fatal(err)
	}
	path := "/api/files/" + f + "/comments"

	code, _ := call(b, "GET", path, "")
	expect(404, code, "comments without access")
	call(a, "PUT", "/api/shares/"+f, `{"user_id":"`+idB+`","role":"viewer"}`)
	code, out = call(b, "GET", path, "")
	if code != 200 || out["can_comment"] != false {
		t.Fatalf("viewer reads comments: %d %v", code, out)
	}
	code, _ = call(b, "POST", path, `{"body":"bagus"}`)
	expect(404, code, "viewer comments")
	call(a, "PUT", "/api/shares/"+f, `{"user_id":"`+idB+`","role":"commenter"}`)
	code, _ = call(b, "POST", path, `{"body":"   "}`)
	expect(400, code, "empty comment")
	code, out = call(b, "POST", path, `{"body":"Fotonya bagus!"}`)
	expect(201, code, "commenter comments")
	mine := out["id"].(string)
	code, out = call(a, "POST", path, `{"body":"Terima kasih"}`)
	expect(201, code, "owner comments")
	theirs := out["id"].(string)

	_, out = call(a, "GET", "/api/list", "")
	if fs := out["files"].([]any); fs[0].(map[string]any)["comments"].(float64) != 2 {
		t.Fatalf("comment count in the list: %v", fs)
	}
	_, out = call(b, "GET", path, "")
	cs := out["comments"].([]any)
	if len(cs) != 2 || cs[0].(map[string]any)["author"] != "ibu" || cs[0].(map[string]any)["mine"] != true || cs[1].(map[string]any)["can_delete"] != false {
		t.Fatalf("comments as seen by the commenter: %v", cs)
	}
	code, _ = call(b, "DELETE", "/api/comments/"+theirs, "")
	expect(404, code, "commenter deletes the owner's comment")
	code, _ = call(a, "DELETE", "/api/comments/"+mine, "")
	expect(204, code, "owner deletes someone's comment")

	// A guest of a commenter link writes under their name and may delete only their own.
	_, out = call(a, "PUT", "/api/links/"+f, `{"enabled":true,"role":"commenter"}`)
	_, out = call("", "POST", "/api/s/"+out["token"].(string)+"/session", `{"name":"Budi"}`)
	g := "X-Guest:" + out["guest"].(string)
	code, out = call(g, "POST", path, `{"body":"Selamat ya"}`)
	expect(201, code, "guest comments")
	gid := out["id"].(string)
	_, out = call(a, "GET", path, "")
	if cs := out["comments"].([]any); cs[len(cs)-1].(map[string]any)["author"] != "Budi" || cs[len(cs)-1].(map[string]any)["guest"] != true {
		t.Fatalf("guest comment: %v", cs)
	}
	code, _ = call(g, "DELETE", "/api/comments/"+theirs, "")
	expect(404, code, "guest deletes the owner's comment")
	code, _ = call(g, "DELETE", "/api/comments/"+gid, "")
	expect(204, code, "guest deletes their own comment")
}

// Paging: walking page tokens returns every item once, folders first, in the asked order.
func TestPaging(t *testing.T) {
	db := testDB(t)
	h := New(&app.App{DB: db, UploadDir: t.TempDir(), ThumbDir: t.TempDir(), MaxUpload: 1 << 30})
	token := ""
	call := func(method, path, body string) map[string]any {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code >= 300 {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
		}
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	call("POST", "/api/auth/register", `{"username":"ayah","password":"rahasia123"}`)
	token = call("POST", "/api/auth/login", `{"username":"ayah","password":"rahasia123"}`)["token"].(string)
	for _, n := range []string{"b", "A", "c", "D", "e"} {
		call("POST", "/api/folders", `{"name":"folder `+n+`"}`)
	}
	if _, err := db.Exec(`INSERT INTO files (id, user_id, name, size, mime, msg_id, created_at)
		SELECT gen_random_uuid(), u.id, 'file ' || i, i * 10, CASE WHEN i % 2 = 0 THEN 'image/jpeg' ELSE 'text/plain' END, i, 1000 + i
		FROM users u, generate_series(1, 8) i`); err != nil {
		t.Fatal(err)
	}
	walk := func(base string) (names []string, pages int) {
		next := ""
		for {
			u := base + "&limit=3"
			if next != "" {
				u += "&page_token=" + next
			}
			out := call("GET", u, "")
			pages++
			for _, k := range []string{"folders", "files"} {
				for _, it := range out[k].([]any) {
					names = append(names, it.(map[string]any)["name"].(string))
				}
			}
			next, _ = out["next_page_token"].(string)
			if next == "" || pages > 20 {
				return
			}
		}
	}
	names, pages := walk("/api/list?sort=name")
	want := "folder A,folder b,folder c,folder D,folder e,file 1,file 2,file 3,file 4,file 5,file 6,file 7,file 8"
	if strings.Join(names, ",") != want || pages != 5 {
		t.Fatalf("by name, %d pages: %v", pages, names)
	}
	names, _ = walk("/api/list?sort=size")
	if strings.Join(names[5:], ",") != "file 8,file 7,file 6,file 5,file 4,file 3,file 2,file 1" {
		t.Fatalf("by size: %v", names)
	}
	names, _ = walk("/api/search?q=file&sort=date")
	if strings.Join(names, ",") != "file 8,file 7,file 6,file 5,file 4,file 3,file 2,file 1" {
		t.Fatalf("search by date: %v", names)
	}
	var media []string
	next := ""
	for i := 0; i < 5; i++ {
		out := call("GET", "/api/media?limit=3&page_token="+next, "")
		for _, it := range out["files"].([]any) {
			media = append(media, it.(map[string]any)["name"].(string))
		}
		if next, _ = out["next_page_token"].(string); next == "" {
			break
		}
	}
	if strings.Join(media, ",") != "file 8,file 6,file 4,file 2" {
		t.Fatalf("media pages: %v", media)
	}
}
