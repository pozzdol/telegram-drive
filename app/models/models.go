// Package models: table schema, row structs and queries shared by several controllers.
package models

import (
	"database/sql"
	"errors"
)

type Folder struct {
	ID        int64  `json:"id"`
	ParentID  *int64 `json:"parent_id"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"created_at"`
}

type File struct {
	ID        int64  `json:"id"`
	FolderID  *int64 `json:"folder_id"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Mime      string `json:"mime"`
	CreatedAt int64  `json:"created_at"`
	TakenAt   *int64 `json:"taken_at"` // when a photo was taken (EXIF), null if unknown
}

type Upload struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Received int64   `json:"received"`
	Sent     int64   `json:"sent"`
	Status   string  `json:"status"`
	FileID   *int64  `json:"file_id"`
	Error    *string `json:"error"`
}

var ErrFolderNotFound = errors.New("folder not found")

// FileColumns is the SELECT list matching File; QueryFiles scans rows of exactly these columns.
const FileColumns = `id, folder_id, name, size, mime, created_at, taken_at`

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
		if err := rows.Scan(&f.ID, &f.FolderID, &f.Name, &f.Size, &f.Mime, &f.CreatedAt, &f.TakenAt); err != nil {
			return nil, err
		}
		if f.TakenAt != nil && *f.TakenAt == 0 {
			f.TakenAt = nil
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FolderRef turns a client folder id into a SQL value: 0 means root (NULL),
// anything else must be a folder owned by uid.
func FolderRef(db *sql.DB, uid, id int64) (any, error) {
	if id == 0 {
		return nil, nil
	}
	var x int
	if err := db.QueryRow(`SELECT 1 FROM folders WHERE id = ? AND user_id = ?`, id, uid).Scan(&x); err != nil {
		return nil, ErrFolderNotFound
	}
	return id, nil
}
