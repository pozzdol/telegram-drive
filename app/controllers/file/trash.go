package file

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"telegram-drive/app/models"
	"telegram-drive/app/utils"
)

// itemsReq is a selection of files and folders from the UI.
type itemsReq struct {
	Files   []string `json:"files"`
	Folders []string `json:"folders"`
}

func readItems(w http.ResponseWriter, r *http.Request) (itemsReq, bool) {
	var req itemsReq
	if !utils.ReadJSON(w, r, &req) {
		return req, false
	}
	if n := len(req.Files) + len(req.Folders); n == 0 || n > 5000 {
		utils.WriteErr(w, http.StatusBadRequest, "select 1-5000 items")
		return req, false
	}
	if !models.ValidIDs(req.Files) || !models.ValidIDs(req.Folders) {
		utils.WriteErr(w, http.StatusBadRequest, "invalid id")
		return req, false
	}
	if req.Files == nil {
		req.Files = []string{}
	}
	if req.Folders == nil {
		req.Folders = []string{}
	}
	return req, true
}

// POST /api/delete {"files":[ids],"folders":[ids]} — moves the items to the trash, folders
// with everything inside them. Nothing leaves Telegram until the trash is purged.
func (c Controller) BulkTrash(w http.ResponseWriter, r *http.Request, uid string) {
	req, ok := readItems(w, r)
	if !ok {
		return
	}
	// Deleting needs editor rights; items the caller may not delete are left alone.
	editable := func(kind string, ids []string) []string {
		ok := []string{}
		for _, id := range ids {
			if role, err := models.Access(r.Context(), c.DB, uid, kind, id); err == nil && role >= models.Editor {
				ok = append(ok, id)
			}
		}
		return ok
	}
	nf, nd, err := models.Trash(r.Context(), c.DB, models.Actor(r.Context(), uid), editable("file", req.Files), editable("folder", req.Folders))
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	utils.WriteJSON(w, http.StatusOK, map[string]int64{"files": nf, "folders": nd})
}

type trashItem struct {
	Type      string `json:"type"` // "file" or "folder"
	ID        string `json:"id"`   // also the trash group id, for restore and purge
	Name      string `json:"name"`
	Size      int64  `json:"size"` // a folder: total size of the files trashed with it
	Mime      string `json:"mime"`
	CreatedAt int64  `json:"created_at"`
	TrashedAt int64  `json:"trashed_at"`
	Missing   bool   `json:"missing"`
	Files     int64  `json:"files"` // a folder: how many files were trashed with it
}

// GET /api/trash — the items the user deleted (not what was inside them), newest first.
func (c Controller) TrashList(w http.ResponseWriter, r *http.Request, uid string) {
	rows, err := c.DB.Query(`
		SELECT 'folder', f.id, f.name, coalesce(x.size, 0)::bigint, '', f.created_at, f.trashed_at, false, coalesce(x.n, 0)
		FROM folders f LEFT JOIN LATERAL (SELECT sum(size) AS size, count(*) AS n FROM files WHERE trash_id = f.id) x ON true
		WHERE f.user_id = $1 AND f.id = f.trash_id
		UNION ALL
		SELECT 'file', id, name, size, mime, created_at, trashed_at, missing_at IS NOT NULL, 0
		FROM files WHERE user_id = $1 AND id = trash_id
		ORDER BY 7 DESC, 2 DESC`, uid)
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	items := []trashItem{}
	for rows.Next() {
		var t trashItem
		if err := rows.Scan(&t.Type, &t.ID, &t.Name, &t.Size, &t.Mime, &t.CreatedAt, &t.TrashedAt, &t.Missing, &t.Files); err != nil {
			utils.ServerErr(w, http.StatusInternalServerError, err)
			return
		}
		items = append(items, t)
	}
	utils.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "days": models.TrashDays})
}

// readGroups reads {"ids":[...]} (trash group ids) or {"all":true}: the caller's own items, and
// with byMe also the items the caller deleted from someone else's folder (to undo a delete).
func (c Controller) readGroups(w http.ResponseWriter, r *http.Request, uid string, byMe bool) ([]string, bool) {
	var req struct {
		IDs []string `json:"ids"`
		All bool     `json:"all"`
	}
	if !utils.ReadJSON(w, r, &req) {
		return nil, false
	}
	if req.All {
		ids, err := c.groups(`SELECT trash_id FROM folders WHERE user_id = $1 AND id = trash_id
			UNION SELECT trash_id FROM files WHERE user_id = $1 AND id = trash_id`, uid)
		if err != nil {
			utils.ServerErr(w, http.StatusInternalServerError, err)
			return nil, false
		}
		return ids, true
	}
	if len(req.IDs) == 0 || len(req.IDs) > 5000 || !models.ValidIDs(req.IDs) {
		utils.WriteErr(w, http.StatusBadRequest, "select 1-5000 items")
		return nil, false
	}
	// Only the caller's own groups.
	ids, err := c.groups(`SELECT trash_id FROM folders WHERE (user_id = $1 OR (trashed_by = $1 AND $3)) AND id = trash_id AND id = ANY($2::uuid[])
		UNION SELECT trash_id FROM files WHERE (user_id = $1 OR (trashed_by = $1 AND $3)) AND id = trash_id AND id = ANY($2::uuid[])`, uid, req.IDs, byMe)
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return nil, false
	}
	return ids, true
}

func (c Controller) groups(query string, args ...any) ([]string, error) {
	rows, err := c.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// POST /api/trash/restore {"ids":[...]} — puts the items back where they were.
func (c Controller) TrashRestore(w http.ResponseWriter, r *http.Request, uid string) {
	ids, ok := c.readGroups(w, r, uid, true)
	if !ok {
		return
	}
	if _, err := models.Restore(r.Context(), c.DB, ids); err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	utils.WriteJSON(w, http.StatusOK, map[string]int{"restored": len(ids)})
}

// POST /api/trash/purge {"ids":[...]} or {"all":true} — deletes for good, Telegram messages too.
func (c Controller) TrashPurge(w http.ResponseWriter, r *http.Request, uid string) {
	ids, ok := c.readGroups(w, r, uid, false)
	if !ok {
		return
	}
	n, err := c.purge(r.Context(), ids)
	if err != nil {
		log.Printf("purge: %v", err)
		utils.WriteErr(w, http.StatusBadGateway, fmt.Sprintf("deleted %d files, then Telegram failed; try again", n))
		return
	}
	utils.WriteJSON(w, http.StatusOK, map[string]int{"purged": len(ids)})
}

// purge deletes trash groups for good. Telegram messages go first, 100 at a time, and a row
// is removed only once its message is gone, so a failure never leaves bytes nobody tracks.
// Returns how many files were deleted.
func (c Controller) purge(ctx context.Context, groups []string) (int, error) {
	if len(groups) == 0 {
		return 0, nil
	}
	type victim struct {
		id      string
		msg     int
		missing bool
	}
	var files []victim
	rows, err := c.DB.Query(`SELECT id, msg_id, missing_at IS NOT NULL FROM files WHERE trash_id = ANY($1::uuid[])`, groups)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var v victim
		rows.Scan(&v.id, &v.msg, &v.missing)
		files = append(files, v)
	}
	rows.Close()

	for start := 0; start < len(files); start += 100 {
		batch := files[start:min(start+100, len(files))]
		ids, msgs := make([]string, len(batch)), make([]int, 0, len(batch))
		for i, v := range batch {
			ids[i] = v.id
			if !v.missing { // already gone from Telegram: only the record is left
				msgs = append(msgs, v.msg)
			}
		}
		if err := c.deleteMessages(ctx, msgs); err != nil {
			return start, err
		}
		if _, err := c.DB.Exec(`DELETE FROM files WHERE id = ANY($1::uuid[])`, ids); err != nil {
			return start, err
		}
		c.DB.Exec(`DELETE FROM shares WHERE item_id = ANY($1::uuid[])`, ids)
		c.DB.Exec(`DELETE FROM comments WHERE file_id = ANY($1::uuid[])`, ids)
		for _, id := range ids {
			os.Remove(ThumbPath(c.ThumbDir, id))
		}
	}

	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return len(files), err
	}
	defer tx.Rollback()
	// Anything else still inside these folders (an item trashed on its own earlier, or an upload
	// that landed meanwhile) moves to the root, so the folders can go.
	for _, q := range []string{
		`UPDATE files SET folder_id = NULL WHERE folder_id IN (SELECT id FROM folders WHERE trash_id = ANY($1::uuid[]))`,
		`UPDATE folders SET parent_id = NULL WHERE parent_id IN (SELECT id FROM folders WHERE trash_id = ANY($1::uuid[]))
			AND (trash_id IS NULL OR trash_id <> ALL($1::uuid[]))`,
		`DELETE FROM shares WHERE item_id IN (SELECT id FROM folders WHERE trash_id = ANY($1::uuid[]))`,
		`DELETE FROM folders WHERE trash_id = ANY($1::uuid[])`, // one statement: parents and children together
	} {
		if _, err := tx.Exec(q, groups); err != nil {
			return len(files), err
		}
	}
	return len(files), tx.Commit()
}

// WatchTrash purges trash groups older than TrashDays, checking every interval.
func (c Controller) WatchTrash(interval time.Duration) {
	select {
	case <-c.Ctx.Done():
		return
	case <-time.After(2 * time.Minute): // let startup (Telegram login, upload resume) settle first
	}
	for {
		cutoff := time.Now().Add(-models.TrashDays * 24 * time.Hour).Unix()
		ids, err := c.groups(`SELECT trash_id FROM folders WHERE id = trash_id AND trashed_at < $1
			UNION SELECT trash_id FROM files WHERE id = trash_id AND trashed_at < $1`, cutoff)
		if err == nil && len(ids) > 0 {
			n, err := c.purge(c.Ctx, ids)
			log.Printf("trash: purged %d expired item(s), %d file(s); err=%v", len(ids), n, err)
		}
		select {
		case <-c.Ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
