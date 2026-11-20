// Package models: table schema, row structs and queries shared by several controllers.
package models

import (
	"database/sql"
	"errors"
	"github.com/google/uuid"
)

type Folder struct {
	ID        string  `json:"id"`
	ParentID  *string `json:"parent_id"`
	Name      string  `json:"name"`
	CreatedAt int64   `json:"created_at"`
}

type File struct {
	ID        string  `json:"id"`
	FolderID  *string `json:"folder_id"`
	Name      string  `json:"name"`
	Size      int64   `json:"size"`
	Mime      string  `json:"mime"`
	CreatedAt int64   `json:"created_at"`
	TakenAt   *int64  `json:"taken_at"` // when a photo was taken (EXIF), null if unknown
	Missing   bool    `json:"missing"`  // the message was deleted in Telegram; only the record is left
}

type Upload struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Received int64   `json:"received"`
	Sent     int64   `json:"sent"`
	Status   string  `json:"status"`
	FileID   *string `json:"file_id"`
	Error    *string `json:"error"`
}

var ErrFolderNotFound = errors.New("folder not found")

// FileColumns is the SELECT list matching File; QueryFiles scans rows of exactly these columns.
const FileColumns = `id, folder_id, name, size, mime, created_at, taken_at, missing_at IS NOT NULL`

// QueryFolders runs a query selecting id, parent_id, name, created_at.
func QueryFolders(db *sql.DB, query string, args ...any) ([]Folder, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Folder{}
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.ID, &f.ParentID, &f.Name, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// QueryFiles runs a query selecting FileColumns.
func QueryFiles(db *sql.DB, query string, args ...any) ([]File, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []File{}
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.ID, &f.FolderID, &f.Name, &f.Size, &f.Mime, &f.CreatedAt, &f.TakenAt, &f.Missing); err != nil {
			return nil, err
		}
		if f.TakenAt != nil && *f.TakenAt == 0 {
			f.TakenAt = nil
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FolderRef turns a client folder id into a SQL value: "" means root (NULL),
// anything else must be a folder owned by uid and not in the trash.
func FolderRef(db *sql.DB, uid, id string) (any, error) {
	if id == "" {
		return nil, nil
	}
	if !ValidID(id) {
		return nil, ErrFolderNotFound
	}
	var x int
	if err := db.QueryRow(`SELECT 1 FROM folders WHERE id = $1 AND user_id = $2 AND trashed_at IS NULL`, id, uid).Scan(&x); err != nil {
		return nil, ErrFolderNotFound
	}
	return id, nil
}

// IDs are UUIDv7: random enough that nobody can guess or count them, and ordered by creation
// time, so new rows land at the end of the primary-key index.
func NewID() string { return uuid.Must(uuid.NewV7()).String() }

// ValidID reports whether s is a UUID, so malformed ids get a 400/404 instead of a database error.
func ValidID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil && len(s) == 36
}

// ValidIDs checks a list of ids from a request body.
func ValidIDs(ids []string) bool {
	for _, id := range ids {
		if !ValidID(id) {
			return false
		}
	}
	return true
}
