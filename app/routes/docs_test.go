package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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
