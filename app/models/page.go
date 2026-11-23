package models

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Lists come in pages, like Google Drive's pageToken: the client sends back the token of the
// previous page and gets the next one. Tokens point at the last item sent (keyset pagination),
// so a page is as fast at the end of a big folder as at the start, and items added or removed
// meanwhile do not shift what comes next the way OFFSET would.

const (
	PageSize    = 50
	MaxPageSize = 500
)

// Cursor is what a page token holds: which list was being read (folders "d" first, then files
// "f", or "m" for the photo gallery), the sort, and the last item sent.
type Cursor struct {
	Phase string `json:"p"`
	Sort  string `json:"s"`
	ID    string `json:"id"`
}

func (c Cursor) Token() string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ParseToken reads a page token; an empty token is the first page. A token from another sort
// starts over, so changing the sort never mixes two orders.
func ParseToken(tok, sort string) Cursor {
	var c Cursor
	b, err := base64.RawURLEncoding.DecodeString(tok)
	if tok == "" || err != nil || json.Unmarshal(b, &c) != nil || c.Sort != sort ||
		!(ValidID(c.ID) || (c.Phase == "f" && c.ID == "")) {
		return Cursor{Sort: sort}
	}
	return c
}

// Sorts lists may ask for. Folders have no size, so "size" sorts them by name.
var sorts = map[string]struct{ key, order string }{
	"name": {"lower(name), name, id", "lower(name), name, id"},
	"date": {"created_at, id", "created_at DESC, id DESC"},
	"size": {"size, id", "size DESC, id DESC"},
}

// SortName validates a sort from the client ("name" when unknown).
func SortName(s string) string {
	if _, ok := sorts[s]; ok {
		return s
	}
	return "name"
}

// page builds "SELECT cols FROM table WHERE where [after the cursor] ORDER BY … LIMIT n" for
// keyset pagination. The cursor's row is read back in a subquery, so the token only carries an id.
// ponytail: if that row is purged between two pages the next page comes back empty; the user
// reloads. Carry the key values in the token if that ever matters.
func page(table, cols, where string, args []any, sort, after string, limit int) (string, []any) {
	s := sorts[sort]
	if table == "folders" && sort == "size" {
		s = sorts["name"]
	}
	q := fmt.Sprintf("SELECT %s FROM %s WHERE %s", cols, table, where)
	if after != "" {
		op := ">"
		if strings.Contains(s.order, "DESC") {
			op = "<"
		}
		args = append(args, after)
		q += fmt.Sprintf(" AND (%s) %s (SELECT %s FROM %s WHERE id = $%d::uuid)", s.key, op, s.key, table, len(args))
	}
	args = append(args, limit)
	q += fmt.Sprintf(" ORDER BY %s LIMIT $%d", s.order, len(args))
	return q, args
}

// NotInAssets leaves out the pictures of documents (files in an assets folder): lists, search
// and Photos show the document, not its pictures.
const NotInAssets = `(folder_id IS NULL OR folder_id NOT IN (SELECT id FROM folders WHERE assets_of IS NOT NULL))`

// ListPage returns one page of folders then files matching the two WHERE clauses (which share
// args), and the token for the next page ("" at the end).
func ListPage(db *sql.DB, folderWhere, fileWhere string, args []any, sort, token string, limit int) ([]Folder, []File, string, error) {
	sort = SortName(sort)
	if limit <= 0 || limit > MaxPageSize {
		limit = PageSize
	}
	cur := ParseToken(token, sort)
	folders, files := []Folder{}, []File{}
	var err error
	if cur.Phase != "f" {
		after := ""
		if cur.Phase == "d" {
			after = cur.ID
		}
		q, a := page("folders", FolderColumns, "("+folderWhere+") AND assets_of IS NULL", append([]any{}, args...), sort, after, limit+1)
		if folders, err = QueryFolders(db, q, a...); err != nil {
			return nil, nil, "", err
		}
		if len(folders) > limit {
			folders = folders[:limit]
			return folders, files, Cursor{Phase: "d", Sort: sort, ID: folders[limit-1].ID}.Token(), nil
		}
		cur = Cursor{} // folders done: files from the start
	}
	room := limit - len(folders)
	if room == 0 { // the page ended exactly with the last folder: files start on the next one
		return folders, files, Cursor{Phase: "f", Sort: sort}.Token(), nil
	}
	q, a := page("files", FileColumns, "("+fileWhere+") AND "+NotInAssets, append([]any{}, args...), sort, cur.ID, room+1)
	if files, err = QueryFiles(db, q, a...); err != nil {
		return nil, nil, "", err
	}
	next := ""
	if len(files) > room {
		files = files[:room]
		next = Cursor{Phase: "f", Sort: sort, ID: files[room-1].ID}.Token()
	}
	return folders, files, next, nil
}
