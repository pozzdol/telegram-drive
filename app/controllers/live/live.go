// Package live: editing a document together. Browsers merge their edits with Yjs (a CRDT); the
// server does not run Yjs. It relays each browser's updates to the others and stores them, so a
// browser that joins late or reconnects gets everything it missed. One connected browser, the
// saver, writes the Markdown to Telegram (PUT /api/files/{id}/content) and then sends a compacted
// state. See docs/editor.md, section 5.
//
// Frames are binary; the first byte is the type:
//
//	0 hello      server → client  JSON helloMsg
//	1 seed       client → server  Yjs state built from the saved file (only when asked to seed)
//	2 update     both             client → server: a Yjs update; server → client: 8-byte seq + update
//	3 awareness  both             cursors and names, relayed, never stored
//	4 saver      server → client  JSON {"saver": bool}: this browser saves from now on (or not)
//	5 compact    client → server  8-byte seq + full Yjs state including every update up to seq
//	6 reset      server → client  the file changed outside the editor: load it again
//	7 ack        server → client  8-byte seq given to the sender's last update
//	8 saved      server → client  JSON {"rev", "seq"}: saved to Telegram up to seq
package live

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/fnv"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"telegram-drive/app"
	"telegram-drive/app/models"
	"telegram-drive/app/utils"
)

const (
	tHello byte = iota
	tSeed
	tUpdate
	tAwareness
	tSaver
	tCompact
	tReset
	tAck
	tSaved
)

const (
	maxUpdate   = 256 << 10 // one edit
	maxState    = 8 << 20   // a seed or compact: the whole document with its history
	maxPeople   = 20        // per document
	maxRate     = 50        // frames per second per connection
	recheck     = 30 * time.Second
	ticketValid = time.Minute
)

type Controller struct{ *app.App }

// ---- tickets: a WebSocket cannot carry headers, and the cookie does not reach another site ----

type ticket struct {
	file    string
	uid     string
	guest   *models.Guest
	expires time.Time
}

var tickets = struct {
	sync.Mutex
	m map[string]ticket
}{m: map[string]ticket{}}

// POST /api/files/{id}/live/ticket — a one-time ticket for the WebSocket, valid a minute.
func (c Controller) Ticket(w http.ResponseWriter, r *http.Request, uid string) {
	id, ok := utils.PathID(w, r)
	if !ok {
		return
	}
	if role, err := models.Access(r.Context(), c.DB, uid, "file", id); err != nil || role < models.Editor {
		utils.WriteErr(w, http.StatusNotFound, "file not found")
		return
	}
	b := make([]byte, 24)
	rand.Read(b)
	t := base64.RawURLEncoding.EncodeToString(b)
	tickets.Lock()
	now := time.Now()
	for k, v := range tickets.m {
		if now.After(v.expires) {
			delete(tickets.m, k)
		}
	}
	tickets.m[t] = ticket{file: id, uid: uid, guest: models.GuestFrom(r.Context()), expires: now.Add(ticketValid)}
	tickets.Unlock()
	utils.WriteJSON(w, http.StatusOK, map[string]string{"ticket": t})
}

func takeTicket(t, file string) (ticket, bool) {
	tickets.Lock()
	defer tickets.Unlock()
	v, ok := tickets.m[t]
	delete(tickets.m, t)
	return v, ok && v.file == file && time.Now().Before(v.expires)
}

// ---- hubs: the people editing one document ----

type conn struct {
	ws    *websocket.Conn
	send  chan []byte
	t     ticket
	name  string
	color string
}

type hub struct {
	mu    sync.Mutex
	file  string
	conns []*conn // in the order they joined; conns[0] is the saver
}

var hubs = struct {
	sync.Mutex
	m map[string]*hub
}{m: map[string]*hub{}}

func hubFor(file string) *hub {
	hubs.Lock()
	defer hubs.Unlock()
	h := hubs.m[file]
	if h == nil {
		h = &hub{file: file}
		hubs.m[file] = h
	}
	return h
}

// push queues a frame; a browser too slow to take it is dropped (it reconnects and catches up).
func (k *conn) push(b []byte) {
	select {
	case k.send <- b:
	default:
		k.ws.Close(websocket.StatusPolicyViolation, "too slow")
	}
}

func frame(t byte, parts ...[]byte) []byte {
	out := []byte{t}
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func seqBytes(n int64) []byte { return binary.BigEndian.AppendUint64(nil, uint64(n)) }

func jsonFrame(t byte, v any) []byte {
	b, _ := json.Marshal(v)
	return frame(t, b)
}

type helloMsg struct {
	State    []byte   `json:"state"`   // null: not seeded yet
	Updates  [][]byte `json:"updates"` // after state, in order
	Seq      int64    `json:"seq"`     // the last of them
	Rev      int64    `json:"rev"`     // saved version the text is based on (X-Rev for saves)
	SavedSeq int64    `json:"saved_seq"`
	Seed     bool     `json:"seed"` // build the document from the saved file and send it
	Saver    bool     `json:"saver"`
	Name     string   `json:"name"`
	Color    string   `json:"color"`
}

// hello tells k everything about the document. Called with h.mu held.
func (h *hub) hello(db *sql.DB, k *conn) error {
	m := helloMsg{Name: k.name, Color: k.color, Saver: len(h.conns) > 0 && h.conns[0] == k, Updates: [][]byte{}}
	err := db.QueryRow(`SELECT state, seq, base_msg_id, saved_seq FROM doc_live WHERE file_id = $1`, h.file).Scan(&m.State, &m.Seq, &m.Rev, &m.SavedSeq)
	if errors.Is(err, sql.ErrNoRows) {
		m.State, m.Seed = nil, m.Saver
		db.QueryRow(`SELECT msg_id FROM files WHERE id = $1`, h.file).Scan(&m.Rev)
		k.push(jsonFrame(tHello, m))
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := db.Query(`SELECT data FROM doc_updates WHERE file_id = $1 ORDER BY seq`, h.file)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var d []byte
		rows.Scan(&d)
		m.Updates = append(m.Updates, d)
	}
	k.push(jsonFrame(tHello, m))
	return rows.Err()
}

func (h *hub) others(k *conn, b []byte) {
	for _, o := range h.conns {
		if o != k {
			o.push(b)
		}
	}
}

func (h *hub) leave(db *sql.DB, k *conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, o := range h.conns {
		if o == k {
			h.conns = append(h.conns[:i], h.conns[i+1:]...)
			if i == 0 && len(h.conns) > 0 { // the saver left: the next one takes over
				var seeded bool
				db.QueryRow(`SELECT true FROM doc_live WHERE file_id = $1`, h.file).Scan(&seeded)
				if seeded {
					h.conns[0].push(jsonFrame(tSaver, map[string]bool{"saver": true}))
				} else {
					h.hello(db, h.conns[0]) // it was waiting for the seed: it seeds now
				}
			}
			break
		}
	}
	if len(h.conns) == 0 {
		hubs.Lock()
		if hubs.m[h.file] == h {
			delete(hubs.m, h.file)
		}
		hubs.Unlock()
	}
}

// handle applies one frame from k. Called with h.mu held.
func (h *hub) handle(db *sql.DB, k *conn, b []byte) error {
	if len(b) == 0 {
		return errors.New("empty frame")
	}
	t, body := b[0], b[1:]
	switch t {
	case tAwareness:
		if len(body) > maxUpdate {
			return errors.New("awareness too large")
		}
		h.others(k, b)
	case tUpdate:
		if len(body) > maxUpdate {
			return errors.New("update too large")
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var seq int64
		if err := tx.QueryRow(`UPDATE doc_live SET seq = seq + 1, updated_at = $2 WHERE file_id = $1 RETURNING seq`, h.file, time.Now().Unix()).Scan(&seq); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil // not seeded yet: the browser gets a hello once it is, with everything
			}
			return err
		}
		if _, err := tx.Exec(`INSERT INTO doc_updates (file_id, seq, data) VALUES ($1, $2, $3)`, h.file, seq, body); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		h.others(k, frame(tUpdate, seqBytes(seq), body))
		k.push(frame(tAck, seqBytes(seq)))
	case tSeed:
		res, err := db.Exec(`INSERT INTO doc_live (file_id, base_msg_id, state, updated_at)
			SELECT id, msg_id, $2, $3 FROM files WHERE id = $1 AND NOT EXISTS (SELECT 1 FROM doc_live WHERE file_id = $1)`,
			h.file, body, time.Now().Unix())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			for _, o := range h.conns {
				if o != k {
					h.hello(db, o)
				}
			}
		}
		h.hello(db, k)
	case tCompact:
		if len(body) < 8 || len(h.conns) == 0 || h.conns[0] != k {
			return nil // only the saver compacts
		}
		seq := int64(binary.BigEndian.Uint64(body))
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		res, err := tx.Exec(`UPDATE doc_live SET state = $2, state_seq = $3 WHERE file_id = $1 AND seq >= $3 AND state_seq < $3`, h.file, body[8:], seq)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			if _, err := tx.Exec(`DELETE FROM doc_updates WHERE file_id = $1 AND seq <= $2`, h.file, seq); err != nil {
				return err
			}
		}
		return tx.Commit()
	default:
		return errors.New("unknown frame")
	}
	return nil
}

// GET /api/files/{id}/live?t=<ticket> — the WebSocket.
func (c Controller) Serve(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("id")
	t, ok := takeTicket(r.URL.Query().Get("t"), file)
	if !ok {
		utils.WriteErr(w, http.StatusUnauthorized, "ticket invalid or used")
		return
	}
	opts := &websocket.AcceptOptions{}
	if u, err := url.Parse(c.Origin); err == nil && u.Host != "" {
		opts.OriginPatterns = []string{u.Host} // the UI on another site (Cloudflare Pages); same host is always allowed
	}
	ws, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	ws.SetReadLimit(maxState + 9)
	k := &conn{ws: ws, send: make(chan []byte, 512), t: t}
	k.name, k.color = c.who(t)

	var h *hub
	for { // a hub that emptied (and left the map) while we waited is not the document's hub any more
		h = hubFor(file)
		h.mu.Lock()
		hubs.Lock()
		current := hubs.m[file] == h
		hubs.Unlock()
		if current {
			break
		}
		h.mu.Unlock()
	}
	if len(h.conns) >= maxPeople {
		h.mu.Unlock()
		ws.Close(websocket.StatusTryAgainLater, "too many people in this document")
		return
	}
	h.conns = append(h.conns, k)
	err = h.hello(c.DB, k)
	h.mu.Unlock()
	defer h.leave(c.DB, k)
	if err != nil {
		log.Printf("live %s: %v", file, err)
		ws.Close(websocket.StatusInternalError, "server error")
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() { // writer
		for {
			select {
			case <-ctx.Done():
				return
			case b := <-k.send:
				wctx, done := context.WithTimeout(ctx, 30*time.Second)
				err := ws.Write(wctx, websocket.MessageBinary, b)
				done()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	go func() { // a removed share, a disabled link or a trashed file ends the session
		tick := time.NewTicker(recheck)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if !c.allowed(ctx, t) {
					ws.Close(websocket.StatusPolicyViolation, "access removed")
					cancel()
					return
				}
			}
		}
	}()

	window, count := time.Now(), 0
	for {
		_, b, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if time.Since(window) > time.Second {
			window, count = time.Now(), 0
		}
		if count++; count > maxRate {
			ws.Close(websocket.StatusPolicyViolation, "too many messages")
			return
		}
		h.mu.Lock()
		err = h.handle(c.DB, k, b)
		h.mu.Unlock()
		if err != nil {
			log.Printf("live %s: %v", file, err)
			ws.Close(websocket.StatusPolicyViolation, err.Error())
			return
		}
	}
}

// allowed re-checks that the ticket's holder may still edit the file.
func (c Controller) allowed(ctx context.Context, t ticket) bool {
	if t.guest != nil {
		g, err := models.LoadGuest(ctx, c.DB, t.guest.Session)
		if err != nil {
			return false
		}
		ctx = models.WithGuest(ctx, g)
	}
	role, err := models.Access(ctx, c.DB, t.uid, "file", t.file)
	return err == nil && role >= models.Editor
}

var colors = []string{"#d9480f", "#2b8a3e", "#1971c2", "#9c36b5", "#c2255c", "#0c8599", "#e67700", "#5f3dc4"}

// who is the name and cursor colour others see.
func (c Controller) who(t ticket) (string, string) {
	name := "Tamu"
	key := t.uid
	if t.guest != nil {
		if t.guest.Name != "" {
			name = t.guest.Name
		}
		name += " (tamu)"
		key = t.guest.Session
	} else {
		c.DB.QueryRow(`SELECT username FROM users WHERE id = $1`, t.uid).Scan(&name)
	}
	f := fnv.New32a()
	f.Write([]byte(key))
	return strings.TrimSpace(name), colors[f.Sum32()%uint32(len(colors))]
}

// Saved records a save made by the live saver (PUT /content with X-Live-Seq): the live state is
// now based on rev, and saved up to seq. Everyone hears about it.
func Saved(db *sql.DB, file string, rev, seq int64) {
	h := existing(file)
	defer h.unlock()
	db.Exec(`UPDATE doc_live SET base_msg_id = $2, saved_seq = CASE WHEN saved_seq > $3 THEN saved_seq ELSE $3 END WHERE file_id = $1`, file, rev, seq)
	h.all(jsonFrame(tSaved, map[string]int64{"rev": rev, "seq": seq}))
}

// Reset forgets the live state of a file whose content changed outside the live editor (a
// restore, the plain-text editor): everyone editing it loads it again.
func Reset(db *sql.DB, file string) {
	h := existing(file)
	defer h.unlock()
	db.Exec(`DELETE FROM doc_live WHERE file_id = $1`, file)
	h.all(frame(tReset))
}

// existing returns the file's hub locked, or nil if nobody has the document open.
func existing(file string) *hub {
	hubs.Lock()
	h := hubs.m[file]
	hubs.Unlock()
	if h != nil {
		h.mu.Lock()
	}
	return h
}

func (h *hub) unlock() {
	if h != nil {
		h.mu.Unlock()
	}
}

func (h *hub) all(b []byte) {
	if h == nil {
		return
	}
	for _, o := range h.conns {
		o.push(b)
	}
}
