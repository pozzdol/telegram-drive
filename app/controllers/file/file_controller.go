// Package file: download (with Range), rename/move and delete files.
package file

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"telegram-drive/app/utils/tgclient"
	"time"

	"telegram-drive/app"
	"telegram-drive/app/models"
	"telegram-drive/app/utils"
	"telegram-drive/app/utils/thumb"
)

type Controller struct{ *app.App }

// getFile loads a file row owned by uid, writing 404 if missing.
func (c Controller) getFile(w http.ResponseWriter, r *http.Request, uid int64) (f models.File, msgID int, ok bool) {
	id, ok := utils.PathID(w, r)
	if !ok {
		return f, 0, false
	}
	err := c.DB.QueryRow(`SELECT id, folder_id, name, size, mime, created_at, msg_id, missing_at IS NOT NULL FROM files WHERE id = $1 AND user_id = $2`,
		id, uid).Scan(&f.ID, &f.FolderID, &f.Name, &f.Size, &f.Mime, &f.CreatedAt, &msgID, &f.Missing)
	if err != nil {
		utils.WriteErr(w, http.StatusNotFound, "file not found")
		return f, 0, false
	}
	return f, msgID, true
}

// GET /api/files/{id}[?inline=1] — supports Range, so videos can seek and downloads can resume.
func (c Controller) Download(w http.ResponseWriter, r *http.Request, uid int64) {
	f, msgID, ok := c.getFile(w, r, uid)
	if !ok {
		return
	}
	rs, err := c.TG.Open(r.Context(), msgID, 8)
	if err != nil {
		c.openFailed(w, f.ID, err)
		return
	}
	disp := "attachment"
	if r.URL.Query().Get("inline") == "1" && inlineSafe(f.Mime) {
		disp = "inline"
	}
	w.Header().Set("Content-Type", f.Mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Even if a type slips past inlineSafe, a file opened from the API origin cannot run scripts.
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self'; media-src 'self'; style-src 'unsafe-inline'")
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": f.Name}))
	http.ServeContent(w, r, f.Name, time.Unix(f.CreatedAt, 0), rs)
}

// deletable is the message to delete in Telegram, or none when it is already gone there.
func deletable(missing bool, msgID int) []int {
	if missing {
		return nil
	}
	return []int{msgID}
}

func (c Controller) deleteMessages(ctx context.Context, msgs []int) error {
	if len(msgs) == 0 {
		return nil
	}
	return c.TG.Delete(ctx, msgs...)
}

// openFailed answers a failed Telegram read. A deleted message is recorded on the file and
// reported as 410 Gone, so the UI can say so and offer to remove the record.
func (c Controller) openFailed(w http.ResponseWriter, fileID int64, err error) {
	if errors.Is(err, tgclient.ErrMissing) {
		c.DB.Exec(`UPDATE files SET missing_at = extract(epoch FROM now())::bigint WHERE id = $1 AND missing_at IS NULL`, fileID)
		utils.WriteJSON(w, http.StatusGone, map[string]any{"error": "this file no longer exists in Telegram", "missing": true})
		return
	}
	utils.ServerErr(w, http.StatusBadGateway, err)
}

// POST /api/files/{id}/check — asks Telegram whether the file's message still exists.
func (c Controller) Check(w http.ResponseWriter, r *http.Request, uid int64) {
	f, msgID, ok := c.getFile(w, r, uid)
	if !ok {
		return
	}
	present, err := c.TG.Present(r.Context(), []int{msgID})
	if err != nil {
		utils.ServerErr(w, http.StatusBadGateway, err)
		return
	}
	c.setMissing(map[int64]bool{f.ID: !present[msgID]})
	utils.WriteJSON(w, http.StatusOK, map[string]bool{"missing": !present[msgID]})
}

// setMissing records which files are gone from Telegram (true) or present again (false).
func (c Controller) setMissing(state map[int64]bool) {
	for id, missing := range state {
		if missing {
			c.DB.Exec(`UPDATE files SET missing_at = extract(epoch FROM now())::bigint WHERE id = $1 AND missing_at IS NULL`, id)
		} else {
			c.DB.Exec(`UPDATE files SET missing_at = NULL WHERE id = $1 AND missing_at IS NOT NULL`, id)
		}
	}
}

// WatchMissing checks every file against Telegram, 100 messages per request, at startup and
// then every interval, so files deleted in Telegram show up as missing in the UI.
func (c Controller) WatchMissing(interval time.Duration) {
	for {
		select {
		case <-c.Ctx.Done():
			return
		case <-time.After(time.Minute): // let startup (Telegram login, upload resume) settle first
		}
		if n, err := c.checkAll(); err != nil {
			log.Printf("missing-file check: %v", err)
		} else if n > 0 {
			log.Printf("missing-file check: %d file(s) no longer in Telegram", n)
		}
		select {
		case <-c.Ctx.Done():
			return
		case <-time.After(interval - time.Minute):
		}
	}
}

func (c Controller) checkAll() (missing int, err error) {
	rows, err := c.DB.Query(`SELECT id, msg_id FROM files ORDER BY id`)
	if err != nil {
		return 0, err
	}
	byMsg := map[int]int64{}
	var msgs []int
	for rows.Next() {
		var id int64
		var msg int
		rows.Scan(&id, &msg)
		byMsg[msg] = id
		msgs = append(msgs, msg)
	}
	rows.Close()
	for start := 0; start < len(msgs); start += 100 {
		batch := msgs[start:min(start+100, len(msgs))]
		present, err := c.TG.Present(c.Ctx, batch)
		if err != nil {
			return missing, err
		}
		state := map[int64]bool{}
		for msg, ok := range present {
			state[byMsg[msg]] = !ok
			if !ok {
				missing++
			}
		}
		c.setMissing(state)
	}
	return missing, nil
}

// ThumbPath is where the cached preview of file id lives.
func ThumbPath(dir string, id int64) string {
	return filepath.Join(dir, strconv.FormatInt(id, 10)+".jpg")
}

// Limits concurrent thumbnail builds: each one downloads the full image from Telegram.
var thumbSlots = make(chan struct{}, 3)

// GET /api/files/{id}/thumb — 480 px JPEG preview, built once and cached in data/thumbs.
// The UI adds ?v=<created_at> so the immutable cache never shows a reused id's old picture.
func (c Controller) Thumb(w http.ResponseWriter, r *http.Request, uid int64) {
	f, msgID, ok := c.getFile(w, r, uid)
	if !ok {
		return
	}
	path := ThumbPath(c.ThumbDir, f.ID)
	if isVideo(f.Mime) {
		// Videos have no server-side decoder; the browser makes their preview (PutThumb).
		if _, err := os.Stat(path); err != nil {
			utils.WriteErr(w, http.StatusNotFound, "no preview yet")
			return
		}
	} else if !thumb.Supported(f.Mime) {
		utils.WriteErr(w, http.StatusNotFound, "no preview for this file type")
		return
	}
	if _, err := os.Stat(path); err != nil {
		thumbSlots <- struct{}{}
		err := func() error {
			defer func() { <-thumbSlots }()
			if _, err := os.Stat(path); err == nil { // built while we waited
				return nil
			}
			rs, err := c.TG.Open(r.Context(), msgID, 4)
			if err != nil {
				return err
			}
			return thumb.Make(rs, path)
		}()
		if err != nil {
			c.openFailed(w, f.ID, err)
			return
		}
	}
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeFile(w, r, path)
}

func isVideo(mime string) bool { return strings.HasPrefix(mime, "video/") }

// PUT /api/files/{id}/thumb — a JPEG frame the browser grabbed from the video.
// ponytail: browser decodes the video (any codec it can play), so the server needs no ffmpeg.
// Add an ffmpeg path only if previews must exist before anyone opens the drive in a browser.
func (c Controller) PutThumb(w http.ResponseWriter, r *http.Request, uid int64) {
	f, _, ok := c.getFile(w, r, uid)
	if !ok {
		return
	}
	if !isVideo(f.Mime) {
		utils.WriteErr(w, http.StatusBadRequest, "only videos take an uploaded preview")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		utils.WriteErr(w, http.StatusRequestEntityTooLarge, "preview too large (max 1 MB)")
		return
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(body))
	if err != nil || cfg.Width > 1024 || cfg.Height > 1024 {
		utils.WriteErr(w, http.StatusBadRequest, "preview must be a JPEG up to 1024 px")
		return
	}
	if _, err := jpeg.Decode(bytes.NewReader(body)); err != nil {
		utils.WriteErr(w, http.StatusBadRequest, "preview is not a valid JPEG")
		return
	}
	path := ThumbPath(c.ThumbDir, f.ID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/move {"files":[ids],"folders":[ids],"to":folderID} — moves several items at once;
// "to" 0 is the root. All or nothing: one transaction, and a folder can never go into itself
// or one of its own subfolders.
func (c Controller) BulkMove(w http.ResponseWriter, r *http.Request, uid int64) {
	var req struct {
		Files   []int64 `json:"files"`
		Folders []int64 `json:"folders"`
		To      int64   `json:"to"`
	}
	if !utils.ReadJSON(w, r, &req) {
		return
	}
	if n := len(req.Files) + len(req.Folders); n == 0 || n > 5000 {
		utils.WriteErr(w, http.StatusBadRequest, "select 1-5000 items")
		return
	}
	if req.Files == nil {
		req.Files = []int64{}
	}
	if req.Folders == nil {
		req.Folders = []int64{}
	}
	ref, err := models.FolderRef(c.DB, uid, req.To)
	if err != nil {
		utils.WriteErr(w, http.StatusNotFound, err.Error())
		return
	}
	if req.To != 0 && len(req.Folders) > 0 {
		// Walk up from the target: if a selected folder is on the way, the move would make a loop.
		var loops int
		if err := c.DB.QueryRow(`WITH RECURSIVE up(id) AS (
				SELECT $1::bigint UNION SELECT parent_id FROM folders JOIN up ON folders.id = up.id WHERE parent_id IS NOT NULL
			) SELECT count(*) FROM up WHERE id = ANY($2)`, req.To, req.Folders).Scan(&loops); err != nil {
			utils.ServerErr(w, http.StatusInternalServerError, err)
			return
		}
		if loops > 0 {
			utils.WriteErr(w, http.StatusBadRequest, "cannot move a folder into itself or one of its subfolders")
			return
		}
	}
	tx, err := c.DB.BeginTx(r.Context(), nil)
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()
	files, err := tx.Exec(`UPDATE files SET folder_id = $1 WHERE user_id = $2 AND id = ANY($3)`, ref, uid, req.Files)
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	folders, err := tx.Exec(`UPDATE folders SET parent_id = $1 WHERE user_id = $2 AND id = ANY($3)`, ref, uid, req.Folders)
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := tx.Commit(); err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	nf, _ := files.RowsAffected()
	nd, _ := folders.RowsAffected()
	utils.WriteJSON(w, http.StatusOK, map[string]int64{"files": nf, "folders": nd})
}

// POST /api/delete {"files":[ids],"folders":[ids]} — deletes the files and the folders with
// everything inside them (subfolders and their files). Telegram messages go first, 100 at a
// time; rows are removed only for messages that are gone, so a failure never orphans bytes.
func (c Controller) BulkDelete(w http.ResponseWriter, r *http.Request, uid int64) {
	var req struct {
		Files   []int64 `json:"files"`
		Folders []int64 `json:"folders"`
	}
	if !utils.ReadJSON(w, r, &req) {
		return
	}
	if len(req.Files)+len(req.Folders) == 0 || len(req.Files)+len(req.Folders) > 5000 {
		utils.WriteErr(w, http.StatusBadRequest, "select 1-5000 items")
		return
	}
	if req.Files == nil {
		req.Files = []int64{}
	}
	if req.Folders == nil {
		req.Folders = []int64{}
	}

	// The selected folders and every folder below them, limited to this user's own.
	var folders []int64
	rows, err := c.DB.Query(`WITH RECURSIVE t(id) AS (
			SELECT id FROM folders WHERE user_id = $1 AND id = ANY($2)
			UNION SELECT f.id FROM folders f JOIN t ON f.parent_id = t.id
		) SELECT id FROM t`, uid, req.Folders)
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		folders = append(folders, id)
	}
	rows.Close()
	if folders == nil {
		folders = []int64{}
	}

	type victim struct {
		id      int64
		msg     int
		missing bool
	}
	var files []victim
	rows, err = c.DB.Query(`SELECT id, msg_id, missing_at IS NOT NULL FROM files WHERE user_id = $1 AND (id = ANY($2) OR folder_id = ANY($3))`,
		uid, req.Files, folders)
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	for rows.Next() {
		var v victim
		rows.Scan(&v.id, &v.msg, &v.missing)
		files = append(files, v)
	}
	rows.Close()

	for start := 0; start < len(files); start += 100 {
		batch := files[start:min(start+100, len(files))]
		ids, msgs := make([]int64, len(batch)), make([]int, 0, len(batch))
		for i, v := range batch {
			ids[i] = v.id
			if !v.missing { // already gone from Telegram: only the record is left
				msgs = append(msgs, v.msg)
			}
		}
		if err := c.deleteMessages(r.Context(), msgs); err != nil {
			log.Printf("bulk delete: Telegram failed after %d of %d files: %v", start, len(files), err)
			utils.WriteErr(w, http.StatusBadGateway, fmt.Sprintf("deleted %d of %d files, then Telegram failed; try again", start, len(files)))
			return
		}
		c.DB.Exec(`DELETE FROM files WHERE id = ANY($1)`, ids)
		for _, id := range ids {
			os.Remove(ThumbPath(c.ThumbDir, id))
		}
	}
	// One statement: parent and child folders go together, so the parent_id check passes.
	if _, err := c.DB.Exec(`DELETE FROM folders WHERE user_id = $1 AND id = ANY($2)`, uid, folders); err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	utils.WriteJSON(w, http.StatusOK, map[string]int{"files": len(files), "folders": len(folders)})
}

// GET /api/media?offset=&limit= — photos and videos across all folders, newest first (gallery view).
func (c Controller) Media(w http.ResponseWriter, r *http.Request, uid int64) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	files, err := models.QueryFiles(c.DB, `SELECT `+models.FileColumns+` FROM files
		WHERE user_id = $1 AND (mime LIKE 'image/%' OR mime LIKE 'video/%') AND mime <> 'image/svg+xml'
		ORDER BY coalesce(nullif(taken_at, 0), created_at) DESC, id DESC LIMIT $2 OFFSET $3`, uid, limit, max(offset, 0))
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	utils.WriteJSON(w, http.StatusOK, map[string]any{"files": files, "has_more": len(files) == limit})
}

// BackfillTakenAt reads the EXIF capture date of JPEGs uploaded before taken_at existed.
// Only the first MiB of each file is fetched from Telegram. Runs once in the background at startup.
func (c Controller) BackfillTakenAt() {
	rows, err := c.DB.Query(`SELECT id, msg_id FROM files WHERE taken_at IS NULL AND mime = 'image/jpeg'`)
	if err != nil {
		return
	}
	type todo struct{ id, msg int64 }
	var list []todo
	for rows.Next() {
		var t todo
		rows.Scan(&t.id, &t.msg)
		list = append(list, t)
	}
	rows.Close()
	for _, t := range list {
		rs, err := c.TG.Open(c.Ctx, int(t.msg), 1)
		if err != nil {
			if c.Ctx.Err() != nil {
				return
			}
			continue // try again next start
		}
		var taken int64 // 0 = checked, no date
		if when := thumb.TakenAt(rs); !when.IsZero() {
			taken = when.Unix()
		}
		c.DB.Exec(`UPDATE files SET taken_at = $1 WHERE id = $2`, taken, t.id)
	}
}

// GET /api/usage — how much is stored, for the sidebar.
func (c Controller) Usage(w http.ResponseWriter, r *http.Request, uid int64) {
	var n, bytes int64
	c.DB.QueryRow(`SELECT count(*), coalesce(sum(size), 0)::bigint FROM files WHERE user_id = $1`, uid).Scan(&n, &bytes)
	utils.WriteJSON(w, http.StatusOK, map[string]int64{"files": n, "bytes": bytes})
}

// inlineSafe: types a browser can show without running scripts. HTML, SVG etc. always download.
func inlineSafe(m string) bool {
	return m == "application/pdf" || strings.HasPrefix(m, "text/plain") || strings.HasPrefix(m, "video/") ||
		strings.HasPrefix(m, "audio/") || (strings.HasPrefix(m, "image/") && !strings.Contains(m, "svg"))
}

// rename and/or move. folder_id 0 moves to root; omit a field to leave it unchanged.
func (c Controller) Update(w http.ResponseWriter, r *http.Request, uid int64) {
	f, _, ok := c.getFile(w, r, uid)
	if !ok {
		return
	}
	var req struct {
		Name     *string `json:"name"`
		FolderID *int64  `json:"folder_id"`
	}
	if !utils.ReadJSON(w, r, &req) {
		return
	}
	if req.Name != nil {
		name, ok := utils.CleanName(*req.Name)
		if !ok {
			utils.WriteErr(w, http.StatusBadRequest, "name must be 1-255 chars")
			return
		}
		c.DB.Exec(`UPDATE files SET name = $1 WHERE id = $2`, name, f.ID)
	}
	if req.FolderID != nil {
		ref, err := models.FolderRef(c.DB, uid, *req.FolderID)
		if err != nil {
			utils.WriteErr(w, http.StatusNotFound, err.Error())
			return
		}
		c.DB.Exec(`UPDATE files SET folder_id = $1 WHERE id = $2`, ref, f.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c Controller) Delete(w http.ResponseWriter, r *http.Request, uid int64) {
	f, msgID, ok := c.getFile(w, r, uid)
	if !ok {
		return
	}
	// Telegram first: if it fails the row stays, so nothing is lost track of.
	// A file already gone from Telegram only has its record left to remove.
	if err := c.deleteMessages(r.Context(), deletable(f.Missing, msgID)); err != nil {
		utils.ServerErr(w, http.StatusBadGateway, err)
		return
	}
	c.DB.Exec(`DELETE FROM files WHERE id = $1`, f.ID)
	os.Remove(ThumbPath(c.ThumbDir, f.ID))
	w.WriteHeader(http.StatusNoContent)
}
