// Package file: download (with Range), rename/move and delete files.
package file

import (
	"bytes"
	"image/jpeg"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	err := c.DB.QueryRow(`SELECT id, folder_id, name, size, mime, created_at, msg_id FROM files WHERE id = ? AND user_id = ?`,
		id, uid).Scan(&f.ID, &f.FolderID, &f.Name, &f.Size, &f.Mime, &f.CreatedAt, &msgID)
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
		utils.WriteErr(w, http.StatusBadGateway, err.Error())
		return
	}
	disp := "attachment"
	if r.URL.Query().Get("inline") == "1" && inlineSafe(f.Mime) {
		disp = "inline"
	}
	w.Header().Set("Content-Type", f.Mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": f.Name}))
	http.ServeContent(w, r, f.Name, time.Unix(f.CreatedAt, 0), rs)
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
			utils.WriteErr(w, http.StatusBadGateway, "preview failed: "+err.Error())
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
		utils.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		utils.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/media?offset=&limit= — photos and videos across all folders, newest first (gallery view).
func (c Controller) Media(w http.ResponseWriter, r *http.Request, uid int64) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	files, err := models.QueryFiles(c.DB, `SELECT `+models.FileColumns+` FROM files
		WHERE user_id = ? AND (mime LIKE 'image/%' OR mime LIKE 'video/%') AND mime <> 'image/svg+xml'
		ORDER BY coalesce(nullif(taken_at, 0), created_at) DESC, id DESC LIMIT ? OFFSET ?`, uid, limit, max(offset, 0))
	if err != nil {
		utils.WriteErr(w, http.StatusInternalServerError, err.Error())
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
		c.DB.Exec(`UPDATE files SET taken_at = ? WHERE id = ?`, taken, t.id)
	}
}

// GET /api/usage — how much is stored, for the sidebar.
func (c Controller) Usage(w http.ResponseWriter, r *http.Request, uid int64) {
	var n, bytes int64
	c.DB.QueryRow(`SELECT count(*), coalesce(sum(size), 0) FROM files WHERE user_id = ?`, uid).Scan(&n, &bytes)
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
		c.DB.Exec(`UPDATE files SET name = ? WHERE id = ?`, name, f.ID)
	}
	if req.FolderID != nil {
		ref, err := models.FolderRef(c.DB, uid, *req.FolderID)
		if err != nil {
			utils.WriteErr(w, http.StatusNotFound, err.Error())
			return
		}
		c.DB.Exec(`UPDATE files SET folder_id = ? WHERE id = ?`, ref, f.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c Controller) Delete(w http.ResponseWriter, r *http.Request, uid int64) {
	f, msgID, ok := c.getFile(w, r, uid)
	if !ok {
		return
	}
	// Telegram first: if it fails the row stays, so nothing is lost track of.
	if err := c.TG.Delete(r.Context(), msgID); err != nil {
		utils.WriteErr(w, http.StatusBadGateway, err.Error())
		return
	}
	c.DB.Exec(`DELETE FROM files WHERE id = ?`, f.ID)
	os.Remove(ThumbPath(c.ThumbDir, f.ID))
	w.WriteHeader(http.StatusNoContent)
}
