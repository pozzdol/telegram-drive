package routes

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"telegram-drive/app"
	"telegram-drive/app/controllers/file"
	"telegram-drive/app/utils/tgclient"
)

// fakeTG is a storage channel in memory.
type fakeTG struct {
	mu   sync.Mutex
	next int
	msgs map[int][]byte
}

func (f *fakeTG) Upload(_ context.Context, r io.Reader, _ string, _ int64, _ string, _ tgclient.ProgressFunc) (int, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.msgs[f.next] = b
	return f.next, nil
}

func (f *fakeTG) Open(_ context.Context, id int, _ int) (io.ReadSeeker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.msgs[id]
	if !ok {
		return nil, tgclient.ErrMissing
	}
	return bytes.NewReader(b), nil
}

func (f *fakeTG) Delete(_ context.Context, ids ...int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range ids {
		delete(f.msgs, id)
	}
	return nil
}

func (f *fakeTG) Present(_ context.Context, ids []int) (map[int]bool, error) {
	out := map[int]bool{}
	for _, id := range ids {
		_, out[id] = f.msgs[id]
	}
	return out, nil
}

// Documents: create, save with a stale-version check, versions grouped per person, name,
// restore, delete, expiry, and purge removing every version's message.
func TestDocuments(t *testing.T) {
	db := testDB(t)
	tg := &fakeTG{msgs: map[int][]byte{}}
	a := &app.App{DB: db, TG: tg, UploadDir: t.TempDir(), ThumbDir: t.TempDir(), MaxUpload: 1 << 30}
	h := New(a)
	type resp struct {
		code int
		body []byte
		rev  string
	}
	call := func(auth, method, path, body string, hdr ...string) resp {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if strings.HasPrefix(body, "{") {
			req.Header.Set("Content-Type", "application/json")
		}
		if k, v, ok := strings.Cut(auth, ":"); ok {
			req.Header.Set(k, v)
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return resp{rec.Code, rec.Body.Bytes(), rec.Header().Get("X-Rev")}
	}
	field := func(r resp, k string) any {
		var m map[string]any
		json.Unmarshal(r.body, &m)
		return m[k]
	}
	expect := func(want int, r resp, what string) {
		t.Helper()
		if r.code != want {
			t.Fatalf("%s: want %d, got %d %s", what, want, r.code, r.body)
		}
	}
	type ver struct {
		ID      string
		Size    int
		By      string
		Label   *string
		Current bool
	}
	versions := func(auth, id string) []ver {
		t.Helper()
		r := call(auth, "GET", "/api/files/"+id+"/versions", "")
		expect(200, r, "versions")
		var out struct{ Versions []ver }
		json.Unmarshal(r.body, &out)
		return out.Versions
	}
	login := func(name string) string {
		r := call("", "POST", "/api/auth/login", `{"username":"`+name+`","password":"rahasia123"}`)
		return "Authorization:Bearer " + field(r, "token").(string)
	}
	call("", "POST", "/api/auth/register", `{"username":"ayah","password":"rahasia123"}`)
	ayah := login("ayah")
	idIbu := field(call(ayah, "POST", "/api/admin/users", `{"username":"ibu","password":"rahasia123"}`), "id").(string)
	ibu := login("ibu")

	r := call(ayah, "POST", "/api/documents", `{"kind":"md"}`)
	expect(201, r, "new document")
	doc := field(r, "id").(string)
	if field(r, "name") != "Dokumen tanpa judul.md" || field(r, "size").(float64) != 0 {
		t.Fatalf("new document: %s", r.body)
	}
	expect(400, call(ayah, "POST", "/api/documents", `{"kind":"exe"}`), "unknown kind")
	r = call(ayah, "GET", "/api/files/"+doc, "")
	if r.code != 200 || len(r.body) != 0 || r.rev != "0" {
		t.Fatalf("empty document download: %d %q rev %q", r.code, r.body, r.rev)
	}

	put := func(auth, rev, body string) resp {
		return call(auth, "PUT", "/api/files/"+doc+"/content", body, "X-Rev", rev)
	}
	r = put(ayah, "0", "# Halo")
	expect(200, r, "first save")
	rev1 := strconv.Itoa(int(field(r, "rev").(float64)))
	expect(409, put(ayah, "0", "# Lama"), "save on a stale version")
	expect(400, call(ayah, "PUT", "/api/files/"+doc+"/content", "x"), "save without X-Rev")
	r = put(ayah, rev1, "# Halo 2")
	expect(200, r, "second save")
	rev2 := strconv.Itoa(int(field(r, "rev").(float64)))
	if vs := versions(ayah, doc); len(vs) != 1 || vs[0].Size != 8 || !vs[0].Current {
		t.Fatalf("saves by one person within minutes are one version: %+v", vs)
	}
	if _, ok := tg.msgs[mustAtoi(rev1)]; ok {
		t.Fatal("the replaced save's message is still in Telegram")
	}

	expect(404, put(ibu, rev2, "# Ibu"), "save without access")
	call(ayah, "PUT", "/api/shares/"+doc, `{"user_id":"`+idIbu+`","role":"viewer"}`)
	expect(404, put(ibu, rev2, "# Ibu"), "viewer saves")
	call(ayah, "PUT", "/api/shares/"+doc, `{"user_id":"`+idIbu+`","role":"editor"}`)
	r = put(ibu, rev2, "# Halo 3")
	expect(200, r, "editor saves")
	rev3 := strconv.Itoa(int(field(r, "rev").(float64)))
	vs := versions(ibu, doc)
	if len(vs) != 2 || vs[0].By != "ibu" || vs[1].By != "ayah" {
		t.Fatalf("another person's save starts a version: %+v", vs)
	}
	old := vs[1].ID

	r = call(ayah, "GET", "/api/files/"+doc+"/versions/"+old+"?inline=1", "")
	if r.code != 200 || string(r.body) != "# Halo 2" {
		t.Fatalf("old version content: %d %q", r.code, r.body)
	}
	expect(204, call(ibu, "PATCH", "/api/files/"+doc+"/versions/"+old, `{"label":"Draf pertama"}`), "name a version")
	expect(200, call(ibu, "POST", "/api/files/"+doc+"/versions/"+old+"/restore", ""), "restore")
	r = call(ayah, "GET", "/api/files/"+doc, "")
	if string(r.body) != "# Halo 2" || r.rev == rev3 {
		t.Fatalf("after restore: %q rev %s", r.body, r.rev)
	}
	vs = versions(ayah, doc)
	if len(vs) != 3 || vs[1].Size != 8 || vs[2].Label == nil {
		t.Fatalf("restore adds a version: %+v", vs)
	}

	expect(404, call(ibu, "DELETE", "/api/files/"+doc+"/versions/"+vs[1].ID, ""), "editor deletes a version")
	expect(400, call(ayah, "DELETE", "/api/files/"+doc+"/versions/"+vs[0].ID, ""), "delete the current version")
	expect(204, call(ayah, "DELETE", "/api/files/"+doc+"/versions/"+vs[1].ID, ""), "owner deletes a version")
	if _, ok := tg.msgs[mustAtoi(rev3)]; ok {
		t.Fatal("deleted version's message is still in Telegram")
	}

	// Expiry: a month later only the current and the named version are left.
	put(ayah, r.rev, "# Halo 4")
	fc := file.Controller{App: a}
	if n, err := fc.ExpireVersions(time.Now().Unix() + 31*24*3600); err != nil || n != 1 {
		t.Fatalf("expire: %d %v", n, err)
	}
	if vs := versions(ayah, doc); len(vs) != 2 || !vs[0].Current || vs[1].Label == nil {
		t.Fatalf("after expiry: %+v", vs)
	}

	// Purge removes every version's message, not just the current one.
	expect(200, call(ayah, "POST", "/api/delete", `{"files":["`+doc+`"]}`), "trash")
	expect(200, call(ayah, "POST", "/api/trash/purge", `{"ids":["`+doc+`"]}`), "purge")
	if len(tg.msgs) != 0 {
		t.Fatalf("messages left after purge: %v", tg.msgs)
	}
}

func mustAtoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// Live editing: tickets, seeding, relaying and storing updates, saves, compaction, a new saver
// when the saver leaves, and a reset when the file changes outside the live editor.
func TestLive(t *testing.T) {
	db := testDB(t)
	tg := &fakeTG{msgs: map[int][]byte{}}
	srv := httptest.NewServer(New(&app.App{DB: db, TG: tg, UploadDir: t.TempDir(), ThumbDir: t.TempDir(), MaxUpload: 1 << 30}))
	defer srv.Close()
	call := func(auth, method, path, body string, hdr ...string) (int, map[string]any) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if strings.HasPrefix(body, "{") {
			req.Header.Set("Content-Type", "application/json")
		}
		if k, v, ok := strings.Cut(auth, ":"); ok {
			req.Header.Set(k, v)
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	login := func(name string) string {
		_, out := call("", "POST", "/api/auth/login", `{"username":"`+name+`","password":"rahasia123"}`)
		return "Authorization:Bearer " + out["token"].(string)
	}
	call("", "POST", "/api/auth/register", `{"username":"ayah","password":"rahasia123"}`)
	ayah := login("ayah")
	_, out := call(ayah, "POST", "/api/admin/users", `{"username":"ibu","password":"rahasia123"}`)
	idIbu := out["id"].(string)
	ibu := login("ibu")
	_, out = call(ayah, "POST", "/api/documents", `{"kind":"md"}`)
	doc := out["id"].(string)
	_, out = call(ayah, "PUT", "/api/files/"+doc+"/content", "# A", "X-Rev", "0")
	rev1 := int64(out["rev"].(float64))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dial := func(auth string) *websocket.Conn {
		t.Helper()
		code, out := call(auth, "POST", "/api/files/"+doc+"/live/ticket", "")
		if code != 200 {
			t.Fatalf("ticket: %d", code)
		}
		ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/files/"+doc+"/live?t="+out["ticket"].(string), nil)
		if err != nil {
			t.Fatal(err)
		}
		return ws
	}
	read := func(ws *websocket.Conn, want byte) []byte {
		t.Helper()
		_, b, err := ws.Read(ctx)
		if err != nil || len(b) == 0 || b[0] != want {
			t.Fatalf("want frame %d, got %v %v", want, b, err)
		}
		return b[1:]
	}
	type hello struct {
		State    []byte
		Updates  [][]byte
		Seq, Rev int64
		SavedSeq int64 `json:"saved_seq"`
		Seed     bool
		Saver    bool
		Name     string
	}
	readHello := func(ws *websocket.Conn) hello {
		t.Helper()
		var h hello
		json.Unmarshal(read(ws, 0), &h)
		return h
	}
	send := func(ws *websocket.Conn, b ...byte) {
		t.Helper()
		if err := ws.Write(ctx, websocket.MessageBinary, b); err != nil {
			t.Fatal(err)
		}
	}

	if code, _ := call(ibu, "POST", "/api/files/"+doc+"/live/ticket", ""); code != 404 {
		t.Fatalf("ticket without access: %d", code)
	}
	if _, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/files/"+doc+"/live?t=nope", nil); err == nil {
		t.Fatal("WebSocket with a made-up ticket")
	}
	call(ayah, "PUT", "/api/shares/"+doc, `{"user_id":"`+idIbu+`","role":"editor"}`)

	a := dial(ayah)
	if h := readHello(a); !h.Seed || !h.Saver || h.State != nil || h.Rev != rev1 || h.Name != "ayah" {
		t.Fatalf("first hello: %+v", h)
	}
	b := dial(ibu)
	if h := readHello(b); h.Seed || h.Saver {
		t.Fatalf("second hello: %+v", h)
	}
	send(a, 1, 'S', '0') // seed
	if h := readHello(b); string(h.State) != "S0" {
		t.Fatalf("hello after seed: %+v", h)
	}
	readHello(a)
	send(a, 2, 'u', '1')
	if got := read(a, 7); binary.BigEndian.Uint64(got) != 1 {
		t.Fatalf("ack: %v", got)
	}
	if got := read(b, 2); binary.BigEndian.Uint64(got) != 1 || string(got[8:]) != "u1" {
		t.Fatalf("relayed update: %v", got)
	}
	send(b, 3, 'c')
	if got := read(a, 3); string(got) != "c" {
		t.Fatalf("awareness: %v", got)
	}

	code, out := call(ayah, "PUT", "/api/files/"+doc+"/content", "# A u1", "X-Rev", strconv.FormatInt(rev1, 10), "X-Live-Seq", "1")
	if code != 200 {
		t.Fatalf("live save: %d", code)
	}
	rev2 := int64(out["rev"].(float64))
	for _, ws := range []*websocket.Conn{a, b} {
		var s struct{ Rev, Seq int64 }
		json.Unmarshal(read(ws, 8), &s)
		if s.Rev != rev2 || s.Seq != 1 {
			t.Fatalf("saved: %+v", s)
		}
	}
	send(a, append([]byte{5}, append(binary.BigEndian.AppendUint64(nil, 1), 'S', '1')...)...)
	c := dial(ayah)
	if h := readHello(c); string(h.State) != "S1" || len(h.Updates) != 0 || h.Seq != 1 || h.SavedSeq != 1 || h.Rev != rev2 {
		t.Fatalf("hello after compaction: %+v", h)
	}

	a.Close(websocket.StatusNormalClosure, "")
	if got := read(b, 4); !strings.Contains(string(got), "true") {
		t.Fatalf("new saver: %s", got)
	}

	// A save from outside the live editor resets everyone.
	call(ayah, "PUT", "/api/files/"+doc+"/content", "# Lain", "X-Rev", strconv.FormatInt(rev2, 10))
	read(b, 6)
	read(c, 6)
	var n int
	db.QueryRow(`SELECT count(*) FROM doc_live`).Scan(&n)
	if n != 0 {
		t.Fatal("live state kept after an outside save")
	}
}

// Pictures of a document: a hidden folder beside it, reachable through the document (also by a
// link guest of the document), moved and trashed with it, left out of lists and Photos.
func TestDocAssets(t *testing.T) {
	db := testDB(t)
	tg := &fakeTG{msgs: map[int][]byte{}}
	h := New(&app.App{DB: db, TG: tg, UploadDir: t.TempDir(), ThumbDir: t.TempDir(), MaxUpload: 1 << 30})
	call := func(auth, method, path, body string) (int, map[string]any, []byte) {
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
		return rec.Code, out, rec.Body.Bytes()
	}
	expect := func(want, got int, what string) {
		t.Helper()
		if got != want {
			t.Fatalf("%s: want %d, got %d", what, want, got)
		}
	}
	call("", "POST", "/api/auth/register", `{"username":"ayah","password":"rahasia123"}`)
	_, out, _ := call("", "POST", "/api/auth/login", `{"username":"ayah","password":"rahasia123"}`)
	a := "Authorization:Bearer " + out["token"].(string)
	_, out, _ = call(a, "POST", "/api/folders", `{"name":"Catatan"}`)
	dir := out["id"].(string)
	_, out, _ = call(a, "POST", "/api/documents", `{"kind":"md","name":"resep","folder_id":"`+dir+`"}`)
	doc := out["id"].(string)

	code, out, _ := call(a, "GET", "/api/files/"+doc+"/assets", "")
	if code != 200 || out["folder_id"] != nil || out["name"] != "resep.md.assets" {
		t.Fatalf("no pictures yet: %d %v", code, out)
	}
	_, out, _ = call(a, "POST", "/api/files/"+doc+"/assets", "")
	assets := out["folder_id"].(string)
	_, out, _ = call(a, "POST", "/api/files/"+doc+"/assets", "")
	if out["folder_id"] != assets {
		t.Fatal("a second call made another pictures folder")
	}
	// A picture in it (uploads need Telegram; the row is what matters here).
	tg.msgs[99] = []byte("PNGDATA")
	const pic = "0190a000-0000-7000-8000-0000000000d1"
	if _, err := db.Exec(`INSERT INTO files (id, user_id, folder_id, name, size, mime, msg_id) SELECT $1, user_id, $2, 'kue.png', 7, 'image/png', 99 FROM folders WHERE id = $2`, pic, assets); err != nil {
		t.Fatal(err)
	}
	_, out, _ = call(a, "GET", "/api/list?folder_id="+dir, "")
	if len(out["folders"].([]any)) != 0 || len(out["files"].([]any)) != 1 {
		t.Fatalf("the pictures folder shows in the list: %v", out)
	}
	_, out, _ = call(a, "GET", "/api/media", "")
	if len(out["files"].([]any)) != 0 {
		t.Fatalf("a document's picture shows in Photos: %v", out)
	}
	code, _, body := call(a, "GET", "/api/files/"+doc+"/asset?path=resep.md.assets/kue.png&inline=1", "")
	if code != 200 || string(body) != "PNGDATA" {
		t.Fatalf("picture by path: %d %q", code, body)
	}
	code, _, _ = call(a, "GET", "/api/files/"+doc+"/asset?path=lain.assets/kue.png", "")
	expect(404, code, "picture outside the document's folder")

	// A link guest of the document alone may add pictures and see them.
	_, out, _ = call(a, "PUT", "/api/links/"+doc, `{"enabled":true,"role":"editor","require_name":false}`)
	_, out, _ = call("", "POST", "/api/s/"+out["token"].(string)+"/session", `{}`)
	g := "X-Guest:" + out["guest"].(string)
	code, _, _ = call(g, "GET", "/api/files/"+doc+"/asset?path=resep.md.assets/kue.png", "")
	expect(200, code, "guest reads a picture")
	code, _, _ = call(g, "POST", "/api/uploads", `{"name":"foto.jpg","size":5,"mime":"image/jpeg","folder_id":"`+assets+`"}`)
	expect(201, code, "guest uploads into the pictures folder")
	code, _, _ = call(g, "POST", "/api/uploads", `{"name":"foto.jpg","size":5,"mime":"image/jpeg","folder_id":"`+dir+`"}`)
	expect(404, code, "guest uploads beside the document")

	// Moves and the trash take the pictures along.
	expect(204, func() int { c, _, _ := call(a, "PATCH", "/api/files/"+doc, `{"folder_id":""}`); return c }(), "move to root")
	var parent *string
	db.QueryRow(`SELECT parent_id FROM folders WHERE id = $1`, assets).Scan(&parent)
	if parent != nil {
		t.Fatal("pictures folder did not follow the document")
	}
	call(a, "POST", "/api/delete", `{"files":["`+doc+`"]}`)
	_, out, _ = call(a, "GET", "/api/trash", "")
	if items := out["items"].([]any); len(items) != 1 {
		t.Fatalf("trash lists more than the document: %v", items)
	}
	call(a, "POST", "/api/trash/restore", `{"ids":["`+doc+`"]}`)
	code, _, _ = call(a, "GET", "/api/files/"+pic, "")
	expect(200, code, "picture back after restore")
	call(a, "POST", "/api/delete", `{"files":["`+doc+`"]}`)
	call(a, "POST", "/api/trash/purge", `{"ids":["`+doc+`"]}`)
	if _, ok := tg.msgs[99]; ok {
		t.Fatal("picture still in Telegram after purge")
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM folders WHERE id = $1`, assets).Scan(&n)
	if n != 0 {
		t.Fatal("pictures folder left after purge")
	}
}
