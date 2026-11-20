// Package models: table schema, row structs and queries shared by several controllers.
package models

import (
	"context"
	"database/sql"
	"errors"
	"github.com/google/uuid"
)

type Folder struct {
	ID        string  `json:"id"`
	ParentID  *string `json:"parent_id"`
	Name      string  `json:"name"`
	CreatedAt int64   `json:"created_at"`
	Shared    bool    `json:"shared"`          // shared with at least one other account
	Owner     string  `json:"owner,omitempty"` // in "Shared with me": who it belongs to
	Role      string  `json:"role,omitempty"`  // in "Shared with me": what the caller may do
}

type File struct {
	ID        string  `json:"id"`
	FolderID  *string `json:"folder_id"`
	Name      string  `json:"name"`
	Size      int64   `json:"size"`
	Mime      string  `json:"mime"`
	CreatedAt int64   `json:"created_at"`
	TakenAt   *int64  `json:"taken_at"`        // when a photo was taken (EXIF), null if unknown
	Missing   bool    `json:"missing"`         // the message was deleted in Telegram; only the record is left
	Shared    bool    `json:"shared"`          // shared with at least one other account
	Owner     string  `json:"owner,omitempty"` // in "Shared with me": who it belongs to
	Role      string  `json:"role,omitempty"`  // in "Shared with me": what the caller may do
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
const FileColumns = `id, folder_id, name, size, mime, created_at, taken_at, missing_at IS NOT NULL,
	EXISTS (SELECT 1 FROM shares WHERE shares.item_id = files.id)`

// FolderColumns is the SELECT list matching Folder, for QueryFolders.
const FolderColumns = `id, parent_id, name, created_at, EXISTS (SELECT 1 FROM shares WHERE shares.item_id = folders.id)`

// QueryFolders runs a query selecting FolderColumns.
func QueryFolders(db *sql.DB, query string, args ...any) ([]Folder, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Folder{}
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.ID, &f.ParentID, &f.Name, &f.CreatedAt, &f.Shared); err != nil {
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
		if err := rows.Scan(&f.ID, &f.FolderID, &f.Name, &f.Size, &f.Mime, &f.CreatedAt, &f.TakenAt, &f.Missing, &f.Shared); err != nil {
			return nil, err
		}
		if f.TakenAt != nil && *f.TakenAt == 0 {
			f.TakenAt = nil
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FolderRef turns a client folder id into a SQL value: "" means uid's own root (NULL); anything
// else must be a folder, not in the trash, where uid has at least role min.
func FolderRef(ctx context.Context, db *sql.DB, uid, id string, min Role) (any, error) {
	if id == "" {
		if GuestFrom(ctx) != nil {
			return nil, ErrFolderNotFound // a guest has no root of their own
		}
		return nil, nil
	}
	if r, err := Access(ctx, db, uid, "folder", id); err != nil || r < min {
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
