// Package upload: chunked uploads.
//
// Cloudflare (proxy and Tunnel) rejects request bodies over 100 MB on the
// free plan and times out responses after 100 s, so a big file goes like this:
//
//	POST /api/uploads              {name,size,mime,folder_id} -> {id, chunk_size}
//	PUT  /api/uploads/{id}?offset= raw bytes, in order         -> {received, status}
//	GET  /api/uploads/{id}         poll until status is done (file_id) or error
//
// Chunks are written to data/uploads/<id>; after the last one the file is sent to
// Telegram in the background, because that can take longer than 100 s.
package upload

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"telegram-drive/app"
	"telegram-drive/app/controllers/file"
	"telegram-drive/app/models"
	"telegram-drive/app/utils"
	"telegram-drive/app/utils/tgclient"
	"telegram-drive/app/utils/thumb"
)

type Controller struct{ *app.App }

const uploadChunk = 50 << 20 // well under Cloudflare's 100 MB request limit

func (c Controller) partPath(id string) string { return filepath.Join(c.UploadDir, id) }

func (c Controller) Create(w http.ResponseWriter, r *http.Request, uid int64) {
	var req struct {
		Name     string `json:"name"`
		Size     int64  `json:"size"`
		Mime     string `json:"mime"`
		FolderID int64  `json:"folder_id"`
	}
	if !utils.ReadJSON(w, r, &req) {
		return
	}
	name, ok := utils.CleanName(filepath.Base(req.Name))
	if !ok {
		utils.WriteErr(w, http.StatusBadRequest, "name must be 1-255 chars")
		return
	}
	if req.Size <= 0 {
		utils.WriteErr(w, http.StatusBadRequest, "empty files are not supported")
		return
	}
	if req.Size > c.MaxUpload {
		utils.WriteErr(w, http.StatusRequestEntityTooLarge, "file too large (max "+strconv.FormatInt(c.MaxUpload>>20, 10)+" MiB)")
		return
	}
	ref, err := models.FolderRef(c.DB, uid, req.FolderID)
	if err != nil {
		utils.WriteErr(w, http.StatusNotFound, err.Error())
		return
	}
	if req.Mime == "" || req.Mime == "application/octet-stream" {
		req.Mime = "application/octet-stream"
		if t := mime.TypeByExtension(filepath.Ext(name)); t != "" {
			req.Mime = t
		}
	}
	id := rand.Text()
	f, err := os.OpenFile(c.partPath(id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		utils.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	f.Close()
	if _, err := c.DB.Exec(`INSERT INTO uploads (id, user_id, folder_id, name, size, mime) VALUES (?, ?, ?, ?, ?, ?)`,
		id, uid, ref, name, req.Size, req.Mime); err != nil {
		os.Remove(c.partPath(id))
		utils.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	utils.WriteJSON(w, http.StatusCreated, map[string]any{"id": id, "chunk_size": uploadChunk})
}

func (c Controller) getUpload(uid int64, id string) (u models.Upload, err error) {
	err = c.DB.QueryRow(`SELECT id, name, size, received, sent, status, file_id, error FROM uploads WHERE id = ? AND user_id = ?`,
		id, uid).Scan(&u.ID, &u.Name, &u.Size, &u.Received, &u.Sent, &u.Status, &u.FileID, &u.Error)
	return u, err
}

func (c Controller) Status(w http.ResponseWriter, r *http.Request, uid int64) {
	u, err := c.getUpload(uid, r.PathValue("id"))
	if err != nil {
		utils.WriteErr(w, http.StatusNotFound, "upload not found")
		return
	}
	utils.WriteJSON(w, http.StatusOK, u)
}

// PUT /api/uploads/{id}?offset=N — offset must equal what the server already has, so a client
// that lost a response just asks GET for "received" and continues from there.
func (c Controller) Chunk(w http.ResponseWriter, r *http.Request, uid int64) {
	u, err := c.getUpload(uid, r.PathValue("id"))
	if err != nil {
		utils.WriteErr(w, http.StatusNotFound, "upload not found")
		return
	}
	if u.Status != "receiving" {
		utils.WriteErr(w, http.StatusConflict, "upload already "+u.Status)
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset != u.Received {
		utils.WriteJSON(w, http.StatusConflict, map[string]any{"error": "offset must equal received", "received": u.Received})
		return
	}
	limit := min(uploadChunk, u.Size-offset)
	f, err := os.OpenFile(c.partPath(u.ID), os.O_WRONLY, 0)
	if err != nil {
		utils.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()
	n, err := io.Copy(io.NewOffsetWriter(f, offset), io.LimitReader(r.Body, limit+1))
	if err != nil {
		utils.WriteErr(w, http.StatusBadRequest, "read chunk: "+err.Error())
		return
	}
	if n == 0 || n > limit {
		utils.WriteErr(w, http.StatusBadRequest, "chunk must be 1.."+strconv.FormatInt(limit, 10)+" bytes")
		return
	}
	received := offset + n
	// Conditional update: a duplicate request for the same offset cannot advance twice.
	res, _ := c.DB.Exec(`UPDATE uploads SET received = ? WHERE id = ? AND received = ? AND status = 'receiving'`, received, u.ID, offset)
	if k, _ := res.RowsAffected(); k == 0 {
		utils.WriteErr(w, http.StatusConflict, "concurrent chunk for the same offset")
		return
	}
	status := "receiving"
	if received == u.Size {
		f.Truncate(u.Size)
		c.DB.Exec(`UPDATE uploads SET status = 'sending' WHERE id = ?`, u.ID)
		status = "sending"
		go c.sendUpload(u.ID)
	}
	utils.WriteJSON(w, http.StatusOK, map[string]any{"received": received, "status": status})
}

// sendUpload pushes a fully received upload to Telegram and creates the file row.
func (c Controller) sendUpload(id string) {
	var uid, size int64
	var folderID *int64
	var name, mimeType string
	if err := c.DB.QueryRow(`SELECT user_id, folder_id, name, size, mime FROM uploads WHERE id = ?`, id).
		Scan(&uid, &folderID, &name, &size, &mimeType); err != nil {
		log.Printf("upload %s: %v", id, err)
		return
	}

	lastPct := int64(-1)
	progress := tgclient.ProgressFunc(func(sent int64) {
		if pct := sent * 100 / size; pct != lastPct { // one DB write per percent
			lastPct = pct
			c.DB.Exec(`UPDATE uploads SET sent = ? WHERE id = ?`, sent, id)
		}
	})

	var msgID int
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		var f *os.File
		if f, err = os.Open(c.partPath(id)); err != nil {
			break
		}
		msgID, err = c.TG.Upload(c.Ctx, f, name, size, mimeType, progress)
		f.Close()
		if err == nil || c.Ctx.Err() != nil {
			break
		}
		log.Printf("upload %s attempt %d: %v", id, attempt, err)
		time.Sleep(time.Duration(attempt*10) * time.Second)
	}
	if err == nil {
		var taken any // NULL for non-JPEG; 0 or unix time once checked
		if mimeType == "image/jpeg" {
			taken = int64(0)
			if f, err := os.Open(c.partPath(id)); err == nil {
				if when := thumb.TakenAt(f); !when.IsZero() {
					taken = when.Unix()
				}
				f.Close()
			}
		}
		var res sql.Result
		res, err = c.DB.Exec(`INSERT INTO files (user_id, folder_id, name, size, mime, msg_id, taken_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			uid, folderID, name, size, mimeType, msgID, taken)
		if err != nil {
			c.TG.Delete(context.Background(), msgID) // e.g. folder deleted meanwhile; don't leave an orphan
		} else {
			fileID, _ := res.LastInsertId()
			if thumb.Supported(mimeType) { // the bytes are still on disk: cheaper than fetching them back later
				if f, err := os.Open(c.partPath(id)); err == nil {
					thumb.Make(f, file.ThumbPath(c.ThumbDir, fileID))
					f.Close()
				}
			}
			c.DB.Exec(`UPDATE uploads SET status = 'done', sent = size, file_id = ? WHERE id = ?`, fileID, id)
			os.Remove(c.partPath(id))
			return
		}
	}
	if errors.Is(c.Ctx.Err(), context.Canceled) {
		return // server stopping: keep status 'sending' and the part file, Resume picks it up
	}
	c.DB.Exec(`UPDATE uploads SET status = 'error', error = ? WHERE id = ?`, err.Error(), id)
	os.Remove(c.partPath(id))
}

// Resume runs at startup: re-sends uploads interrupted by a restart and
// drops uploads (and their part files) older than a day.
func (c Controller) Resume() {
	old := time.Now().Add(-24 * time.Hour).Unix()
	rows, _ := c.DB.Query(`SELECT id, status, created_at FROM uploads`)
	var resend, drop []string
	for rows.Next() {
		var id, status string
		var created int64
		rows.Scan(&id, &status, &created)
		if status == "sending" {
			resend = append(resend, id)
		} else if created < old {
			drop = append(drop, id)
		}
	}
	rows.Close()
	for _, id := range drop {
		os.Remove(c.partPath(id))
		c.DB.Exec(`DELETE FROM uploads WHERE id = ?`, id)
	}
	for _, id := range resend {
		go c.sendUpload(id)
	}
}
