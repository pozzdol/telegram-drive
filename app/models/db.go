package models

import (
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// Folder tree lives only in SQLite; Telegram just stores the bytes.
// Foreign keys refuse to delete a folder that still has subfolders or files.
const schema = `
CREATE TABLE IF NOT EXISTS users (
	id            INTEGER PRIMARY KEY,
	username      TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	created_at    INTEGER NOT NULL DEFAULT (unixepoch())
);
CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS folders (
	id         INTEGER PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	parent_id  INTEGER REFERENCES folders(id),
	name       TEXT NOT NULL,
	created_at INTEGER NOT NULL DEFAULT (unixepoch())
);
CREATE INDEX IF NOT EXISTS folders_parent ON folders(user_id, parent_id);
CREATE TABLE IF NOT EXISTS files (
	id         INTEGER PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	folder_id  INTEGER REFERENCES folders(id),
	name       TEXT NOT NULL,
	size       INTEGER NOT NULL,
	mime       TEXT NOT NULL,
	msg_id     INTEGER NOT NULL,
	created_at INTEGER NOT NULL DEFAULT (unixepoch())
);
CREATE INDEX IF NOT EXISTS files_folder ON files(user_id, folder_id);
CREATE INDEX IF NOT EXISTS files_created ON files(user_id, created_at);
-- Chunked uploads in progress. status: receiving -> sending -> done | error
CREATE TABLE IF NOT EXISTS uploads (
	id         TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	folder_id  INTEGER,
	name       TEXT NOT NULL,
	size       INTEGER NOT NULL,
	mime       TEXT NOT NULL,
	received   INTEGER NOT NULL DEFAULT 0,
	sent       INTEGER NOT NULL DEFAULT 0,
	status     TEXT NOT NULL DEFAULT 'receiving',
	file_id    INTEGER,
	error      TEXT,
	created_at INTEGER NOT NULL DEFAULT (unixepoch())
);
`

// Open opens (or creates) the SQLite database and its tables.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("create schema: %w", err)
	}
	// Added after the first release. NULL = not checked yet, 0 = no EXIF date, else unix time the photo was taken.
	if _, err := db.Exec(`ALTER TABLE files ADD COLUMN taken_at INTEGER`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return nil, fmt.Errorf("add taken_at: %w", err)
	}
	// Added for family accounts. Admins manage users; there is always at least one (the oldest user).
	if _, err := db.Exec(`ALTER TABLE users ADD COLUMN is_admin INTEGER NOT NULL DEFAULT 0`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return nil, fmt.Errorf("add is_admin: %w", err)
	}
	if _, err := db.Exec(`UPDATE users SET is_admin = 1 WHERE id = (SELECT min(id) FROM users)
		AND NOT EXISTS (SELECT 1 FROM users WHERE is_admin = 1)`); err != nil {
		return nil, fmt.Errorf("ensure admin: %w", err)
	}
	return db, nil
}
