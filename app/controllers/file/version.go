package file

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"telegram-drive/app/controllers/live"
	"telegram-drive/app/models"
	"telegram-drive/app/utils"
)

// Documents: Markdown and text files edited in the app. Telegram cannot change a file in place,
// so a save uploads the new content as a new message and the file row points at it; the
// previous message stays as a version (table file_versions, see models.versionsSchema).

const (
	MaxDocSize    = 1 << 20 // bigger text files open read-only
	VersionGap    = 10 * 60 // seconds: saves by one person closer than this make one version
	VersionDays   = 30      // unnamed versions older than this are removed
	VersionsKept  = 100     // and at most this many per file
	revHeader     = "X-Rev" // the current version, so a save can tell it is not stale
	errConflictTx = "file changed meanwhile"
)

// Editable reports whether a file is a document the app can edit.
func Editable(name, mime string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown", ".txt":
		return true
	}
	return strings.HasPrefix(mime, "text/markdown") || strings.HasPrefix(mime, "text/plain")
}

// POST /api/documents {"folder_id","kind":"md"|"txt","name"} — a new empty document. It has no
// Telegram message (msg_id 0) until its first save: Telegram does not store empty files.
func (c Controller) CreateDocument(w http.ResponseWriter, r *http.Request, uid string) {
	var req struct {
		FolderID string `json:"folder_id"`
		Kind     string `json:"kind"`
		Name     string `json:"name"`
	}
	if !utils.ReadJSON(w, r, &req) {
		return
	}
	ext, mime := ".md", "text/markdown; charset=utf-8"
	if req.Kind == "txt" {
		ext, mime = ".txt", "text/plain; charset=utf-8"
	} else if req.Kind != "md" {
		utils.WriteErr(w, http.StatusBadRequest, `kind must be "md" or "txt"`)
		return
	}
	if req.Name == "" {
		req.Name = "Dokumen tanpa judul"
	}
	name, ok := utils.CleanName(req.Name)
	if !ok {
		utils.WriteErr(w, http.StatusBadRequest, "name must be 1-255 chars")
		return
	}
	if !Editable(name, "") {
		name += ext
	}
	ref, err := models.FolderRef(r.Context(), c.DB, uid, req.FolderID, models.Editor)
	if err != nil {
		utils.WriteErr(w, http.StatusNotFound, err.Error())
		return
	}
	owner, err := models.OwnerFor(r.Context(), c.DB, uid, ref)
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	id := models.NewID()
	if _, err := c.DB.Exec(`INSERT INTO files (id, user_id, folder_id, name, size, mime, msg_id, guest_name)
		VALUES ($1, $2, $3, $4, 0, $5, 0, $6)`, id, owner, ref, name, mime, models.GuestName(r.Context())); err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	files, err := models.QueryFiles(c.DB, `SELECT `+models.FileColumns+` FROM files WHERE id = $1`, id)
	if err != nil || len(files) == 0 {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	utils.WriteJSON(w, http.StatusCreated, files[0])
}

var errConflict = errors.New(errConflictTx)

// PUT /api/files/{id}/content — saves a document. Body: the whole new content. Header X-Rev: the
// version the client edited (from the download's X-Rev); 409 with the current rev if it changed.
func (c Controller) PutContent(w http.ResponseWriter, r *http.Request, uid string) {
	f, msgID, ok := c.getFile(w, r, uid, models.Editor)
	if !ok {
		return
	}
	if !Editable(f.Name, f.Mime) || f.Missing {
		utils.WriteErr(w, http.StatusBadRequest, "only Markdown and text files can be edited")
		return
	}
	base, err := strconv.Atoi(r.Header.Get(revHeader))
	if err != nil {
		utils.WriteErr(w, http.StatusBadRequest, "X-Rev header required")
		return
	}
	if base != msgID {
		utils.WriteJSON(w, http.StatusConflict, map[string]any{"error": errConflictTx, "rev": msgID})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxDocSize))
	if err != nil {
		utils.WriteErr(w, http.StatusRequestEntityTooLarge, "document too large (max 1 MB)")
		return
	}
	rev, err := c.save(r.Context(), uid, f, msgID, body, false)
	if errors.Is(err, errConflict) {
		utils.WriteJSON(w, http.StatusConflict, map[string]any{"error": errConflictTx, "rev": rev})
		return
	}
	if err != nil {
		utils.ServerErr(w, http.StatusBadGateway, err)
		return
	}
	// From the live editor's saver: the live state stays and is now saved. From anywhere else
	// (the plain-text editor, an API client): whoever edits live loads the new text.
	if seq, err := strconv.ParseInt(r.Header.Get("X-Live-Seq"), 10, 64); err == nil {
		live.Saved(c.DB, f.ID, int64(rev), seq)
	} else {
		live.Reset(c.DB, f.ID)
	}
	utils.WriteJSON(w, http.StatusOK, map[string]any{"rev": rev, "size": len(body)})
}

// save stores body as the new content of f, whose current message is old. It returns the new
// rev (message id), or errConflict and the current rev if someone saved in between.
// newVersion forces a version of its own (a restore) instead of joining the newest one.
func (c Controller) save(ctx context.Context, uid string, f models.File, old int, body []byte, newVersion bool) (int, error) {
	msg := 0
	if len(body) > 0 {
		var err error
		msg, err = c.TG.Upload(ctx, bytes.NewReader(body), f.Name, int64(len(body)), f.Mime, func(int64) {})
		if err != nil {
			return 0, err
		}
	}
	drop := msg // the message to delete afterwards: the new one if the save fails
	defer func() {
		if drop != 0 {
			c.deleteMessages(context.Background(), []int{drop})
		}
	}()
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	res, err := tx.Exec(`UPDATE files SET msg_id = $1, size = $2, updated_at = $3 WHERE id = $4 AND msg_id = $5 AND trashed_at IS NULL`,
		msg, len(body), now, f.ID, old)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var cur int
		c.DB.QueryRow(`SELECT msg_id FROM files WHERE id = $1`, f.ID).Scan(&cur)
		return cur, errConflict
	}
	// A file uploaded and never edited has no versions yet: its upload becomes the first one.
	if _, err := tx.Exec(`INSERT INTO file_versions (id, file_id, msg_id, size, created_at, saved_at, saved_by, guest_name)
		SELECT $1, id, $2, $3, created_at, created_at, CASE WHEN guest_name IS NULL THEN user_id END, guest_name FROM files
		WHERE id = $4 AND NOT EXISTS (SELECT 1 FROM file_versions WHERE file_id = $4)`,
		models.NewID(), old, f.Size, f.ID); err != nil {
		return 0, err
	}
	var last struct {
		id      string
		savedAt int64
		by      *string
		guest   *string
		label   *string
	}
	if err := tx.QueryRow(`SELECT id, saved_at, saved_by, guest_name, label FROM file_versions WHERE file_id = $1
		ORDER BY created_at DESC, id DESC LIMIT 1`, f.ID).Scan(&last.id, &last.savedAt, &last.by, &last.guest, &last.label); err != nil {
		return 0, err
	}
	by, guest := models.Actor(ctx, uid), models.GuestName(ctx)
	same := (last.by != nil && by != nil && *last.by == by.(string)) ||
		(last.guest != nil && guest != nil && *last.guest == guest.(string))
	if !newVersion && same && last.label == nil && now-last.savedAt < VersionGap {
		// Same person still editing: the newest version takes the new content, its old message goes.
		_, err = tx.Exec(`UPDATE file_versions SET msg_id = $1, size = $2, saved_at = $3 WHERE id = $4`, msg, len(body), now, last.id)
		drop = old
	} else {
		_, err = tx.Exec(`INSERT INTO file_versions (id, file_id, msg_id, size, created_at, saved_at, saved_by, guest_name)
			VALUES ($1, $2, $3, $4, $5, $5, $6, $7)`, models.NewID(), f.ID, msg, len(body), now, by, guest)
		drop = 0
	}
	if err != nil {
		drop = msg
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		drop = msg
		return 0, err
	}
	return msg, nil
}

type version struct {
	ID        string  `json:"id"`
	Size      int64   `json:"size"`
	CreatedAt int64   `json:"created_at"`
	SavedAt   int64   `json:"saved_at"`
	By        string  `json:"by"`
	Label     *string `json:"label"`
	Current   bool    `json:"current"`
	msgID     int
}

// versions lists a file's versions, newest (current) first. A file never edited has no rows
// and gets one, made up from the file itself, with the file's id.
func (c Controller) versions(f models.File, msgID int) ([]version, error) {
	rows, err := c.DB.Query(`SELECT v.id, v.size, v.created_at, v.saved_at, coalesce(u.username, coalesce(v.guest_name, 'Tamu') || ' (tamu)'), v.label, v.msg_id
		FROM file_versions v LEFT JOIN users u ON u.id = v.saved_by WHERE v.file_id = $1 ORDER BY v.created_at DESC, v.id DESC`, f.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []version{}
	for rows.Next() {
		var v version
		if err := rows.Scan(&v.ID, &v.Size, &v.CreatedAt, &v.SavedAt, &v.By, &v.Label, &v.msgID); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		var by string
		c.DB.QueryRow(`SELECT coalesce(guest_name || ' (tamu)', (SELECT username FROM users WHERE id = files.user_id)) FROM files WHERE id = $1`, f.ID).Scan(&by)
		out = append(out, version{ID: f.ID, Size: f.Size, CreatedAt: f.CreatedAt, SavedAt: f.CreatedAt, By: by, msgID: msgID})
	}
	out[0].Current = true
	return out, nil
}

// GET /api/files/{id}/versions
func (c Controller) Versions(w http.ResponseWriter, r *http.Request, uid string) {
	f, msgID, ok := c.getFile(w, r, uid, models.Viewer)
	if !ok {
		return
	}
	vs, err := c.versions(f, msgID)
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	role, _ := models.Access(r.Context(), c.DB, uid, "file", f.ID)
	utils.WriteJSON(w, http.StatusOK, map[string]any{"versions": vs, "role": role.String(),
		"editable": Editable(f.Name, f.Mime) && f.Size <= MaxDocSize})
}

// getVersion loads file {id} with role min and its version {vid}.
func (c Controller) getVersion(w http.ResponseWriter, r *http.Request, uid string, min models.Role) (f models.File, msgID int, v version, ok bool) {
	if f, msgID, ok = c.getFile(w, r, uid, min); !ok {
		return
	}
	vs, err := c.versions(f, msgID)
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return f, 0, v, false
	}
	for _, v := range vs {
		if v.ID == r.PathValue("vid") {
			return f, msgID, v, true
		}
	}
	utils.WriteErr(w, http.StatusNotFound, "version not found")
	return f, 0, v, false
}

// GET /api/files/{id}/versions/{vid}[?inline=1] — the content of one version.
func (c Controller) VersionDownload(w http.ResponseWriter, r *http.Request, uid string) {
	f, _, v, ok := c.getVersion(w, r, uid, models.Viewer)
	if !ok {
		return
	}
	f.Size = v.Size
	c.serve(w, r, f, v.msgID, v.SavedAt)
}

// PATCH /api/files/{id}/versions/{vid} {"label": "…" or null} — name a version, so it is kept.
func (c Controller) VersionLabel(w http.ResponseWriter, r *http.Request, uid string) {
	f, _, v, ok := c.getVersion(w, r, uid, models.Editor)
	if !ok {
		return
	}
	var req struct {
		Label *string `json:"label"`
	}
	if !utils.ReadJSON(w, r, &req) {
		return
	}
	var label any
	if req.Label != nil && strings.TrimSpace(*req.Label) != "" {
		l, ok := utils.CleanName(*req.Label)
		if !ok {
			utils.WriteErr(w, http.StatusBadRequest, "name must be 1-255 chars")
			return
		}
		label = l
	}
	if v.ID == f.ID { // never edited: the made-up version needs a row to hold the name
		if _, err := c.DB.Exec(`INSERT INTO file_versions (id, file_id, msg_id, size, created_at, saved_at, saved_by, guest_name, label)
			SELECT $1, id, msg_id, size, created_at, created_at, CASE WHEN guest_name IS NULL THEN user_id END, guest_name, $2 FROM files WHERE id = $1`,
			f.ID, label); err != nil {
			utils.ServerErr(w, http.StatusInternalServerError, err)
			return
		}
	} else if _, err := c.DB.Exec(`UPDATE file_versions SET label = $1 WHERE id = $2`, label, v.ID); err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/files/{id}/versions/{vid}/restore — the version's content becomes a new current
// version, so a restore can be undone like any other change.
func (c Controller) VersionRestore(w http.ResponseWriter, r *http.Request, uid string) {
	f, msgID, v, ok := c.getVersion(w, r, uid, models.Editor)
	if !ok {
		return
	}
	if v.Current {
		utils.WriteErr(w, http.StatusBadRequest, "this is already the current version")
		return
	}
	if v.Size > MaxDocSize {
		utils.WriteErr(w, http.StatusBadRequest, "version too large to restore (max 1 MB)")
		return
	}
	var body []byte
	if v.msgID != 0 {
		rs, err := c.TG.Open(r.Context(), v.msgID, 1)
		if err != nil {
			c.openFailed(w, f.ID, err)
			return
		}
		if body, err = io.ReadAll(io.LimitReader(rs, MaxDocSize)); err != nil {
			utils.ServerErr(w, http.StatusBadGateway, err)
			return
		}
	}
	rev, err := c.save(r.Context(), uid, f, msgID, body, true)
	if errors.Is(err, errConflict) {
		utils.WriteJSON(w, http.StatusConflict, map[string]any{"error": errConflictTx, "rev": rev})
		return
	}
	if err != nil {
		utils.ServerErr(w, http.StatusBadGateway, err)
		return
	}
	live.Reset(c.DB, f.ID)
	models.LogAccess(r.Context(), c.DB, uid, f.ID, "restored the version of "+time.Unix(v.CreatedAt, 0).UTC().Format(time.RFC3339))
	utils.WriteJSON(w, http.StatusOK, map[string]any{"rev": rev, "size": len(body)})
}

// DELETE /api/files/{id}/versions/{vid} — owner only; the current version cannot go.
func (c Controller) VersionDelete(w http.ResponseWriter, r *http.Request, uid string) {
	f, _, v, ok := c.getVersion(w, r, uid, models.Owner)
	if !ok {
		return
	}
	if v.Current {
		utils.WriteErr(w, http.StatusBadRequest, "the current version cannot be deleted")
		return
	}
	if err := c.dropVersions([]version{v}); err != nil {
		utils.ServerErr(w, http.StatusBadGateway, err)
		return
	}
	models.LogAccess(r.Context(), c.DB, uid, f.ID, "deleted the version of "+time.Unix(v.CreatedAt, 0).UTC().Format(time.RFC3339))
	w.WriteHeader(http.StatusNoContent)
}

// dropVersions deletes versions: Telegram messages first, then the rows.
func (c Controller) dropVersions(vs []version) error {
	for start := 0; start < len(vs); start += 100 {
		batch := vs[start:min(start+100, len(vs))]
		ids, msgs := make([]string, len(batch)), []int{}
		for i, v := range batch {
			ids[i] = v.ID
			if v.msgID != 0 {
				msgs = append(msgs, v.msgID)
			}
		}
		if err := c.deleteMessages(c.ctx(), msgs); err != nil {
			return err
		}
		if _, err := c.DB.Exec(`DELETE FROM file_versions WHERE id = ANY($1::uuid[])`, ids); err != nil {
			return err
		}
	}
	return nil
}

func (c Controller) ctx() context.Context {
	if c.Ctx != nil {
		return c.Ctx
	}
	return context.Background()
}

// expiredVersions: unnamed, not current, and older than VersionDays or beyond the newest
// VersionsKept of their file.
func (c Controller) expiredVersions(now int64) ([]version, error) {
	rows, err := c.DB.Query(`SELECT id, msg_id FROM (
			SELECT id, msg_id, label, saved_at,
				row_number() OVER (PARTITION BY file_id ORDER BY created_at DESC, id DESC) AS n
			FROM file_versions) x
		WHERE n > 1 AND label IS NULL AND (saved_at < $1 OR n > $2)`, now-VersionDays*24*3600, VersionsKept)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []version
	for rows.Next() {
		var v version
		rows.Scan(&v.ID, &v.msgID)
		out = append(out, v)
	}
	return out, rows.Err()
}

// ExpireVersions removes expired versions (see expiredVersions) and returns how many.
func (c Controller) ExpireVersions(now int64) (int, error) {
	vs, err := c.expiredVersions(now)
	if err != nil || len(vs) == 0 {
		return 0, err
	}
	return len(vs), c.dropVersions(vs)
}

// WatchVersions removes expired versions every interval.
func (c Controller) WatchVersions(interval time.Duration) {
	for {
		select {
		case <-c.Ctx.Done():
			return
		case <-time.After(interval):
		}
		if n, err := c.ExpireVersions(time.Now().Unix()); n > 0 || err != nil {
			log.Printf("versions: removed %d expired; err=%v", n, err)
		}
	}
}

// versionMessages are the Telegram messages of the versions of files, except msg 0 and the
// files' current messages (the caller deletes those).
func versionMessages(db *sql.DB, files []string) ([]int, error) {
	rows, err := db.Query(`SELECT DISTINCT v.msg_id FROM file_versions v JOIN files f ON f.id = v.file_id
		WHERE v.file_id = ANY($1::uuid[]) AND v.msg_id <> 0 AND v.msg_id <> f.msg_id`, files)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var m int
		rows.Scan(&m)
		out = append(out, m)
	}
	return out, rows.Err()
}
