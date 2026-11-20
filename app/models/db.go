package models

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

// Folder tree lives only in PostgreSQL; Telegram just stores the bytes.
// Foreign keys refuse to delete a folder that still has subfolders or files.
// Times are unix seconds (bigint), the same values the API returns.
const schema = `
CREATE TABLE IF NOT EXISTS users (
	id            uuid PRIMARY KEY, -- UUIDv7, made by the server (models.NewID)
	username      text NOT NULL UNIQUE,
	password_hash text NOT NULL,
	is_admin      boolean NOT NULL DEFAULT false,
	created_at    bigint NOT NULL DEFAULT extract(epoch FROM now())::bigint
);
CREATE TABLE IF NOT EXISTS sessions (
	token_hash text PRIMARY KEY,
	user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at bigint NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_user ON sessions(user_id);
CREATE TABLE IF NOT EXISTS folders (
	id         uuid PRIMARY KEY,
	user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	parent_id  uuid REFERENCES folders(id),
	name       text NOT NULL,
	created_at bigint NOT NULL DEFAULT extract(epoch FROM now())::bigint
);
CREATE INDEX IF NOT EXISTS folders_parent ON folders(user_id, parent_id);
CREATE TABLE IF NOT EXISTS files (
	id         uuid PRIMARY KEY,
	user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	folder_id  uuid REFERENCES folders(id),
	name       text NOT NULL,
	size       bigint NOT NULL,
	mime       text NOT NULL,
	msg_id     bigint NOT NULL,
	created_at bigint NOT NULL DEFAULT extract(epoch FROM now())::bigint,
	taken_at   bigint -- NULL = not checked yet, 0 = no EXIF date, else unix time the photo was taken
);
CREATE INDEX IF NOT EXISTS files_folder ON files(user_id, folder_id);
CREATE INDEX IF NOT EXISTS files_created ON files(user_id, created_at);
-- Chunked uploads in progress. status: receiving -> sending -> done | error
CREATE TABLE IF NOT EXISTS uploads (
	id         text PRIMARY KEY,
	user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	folder_id  uuid,
	name       text NOT NULL,
	size       bigint NOT NULL,
	mime       text NOT NULL,
	received   bigint NOT NULL DEFAULT 0,
	sent       bigint NOT NULL DEFAULT 0,
	status     text NOT NULL DEFAULT 'receiving',
	file_id    uuid,
	error      text,
	created_at bigint NOT NULL DEFAULT extract(epoch FROM now())::bigint
);
-- Telegram account linked for "Log in with Telegram" (added later, hence ALTER).
ALTER TABLE users ADD COLUMN IF NOT EXISTS telegram_id bigint UNIQUE;
-- Set when the file's message is found deleted in Telegram (unix time); the UI offers to remove the record.
ALTER TABLE files ADD COLUMN IF NOT EXISTS missing_at bigint;
-- Trash. Deleting an item marks it and everything inside it with the same trash_id (the id of
-- the item the user picked), so that whole group is restored or purged together. A group whose
-- trashed_at is 30 days old is purged for good, Telegram messages included.
ALTER TABLE folders ADD COLUMN IF NOT EXISTS trashed_at bigint;
ALTER TABLE folders ADD COLUMN IF NOT EXISTS trash_id uuid;
ALTER TABLE folders ADD COLUMN IF NOT EXISTS trashed_by uuid REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE files ADD COLUMN IF NOT EXISTS trashed_at bigint;
ALTER TABLE files ADD COLUMN IF NOT EXISTS trash_id uuid;
ALTER TABLE files ADD COLUMN IF NOT EXISTS trashed_by uuid REFERENCES users(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS folders_trash ON folders(trash_id) WHERE trash_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS files_trash ON files(trash_id) WHERE trash_id IS NOT NULL;
-- Sharing with other accounts. A share on a folder covers everything inside it.
-- item_id has no foreign key (it is a file or a folder); purging an item removes its shares.
CREATE TABLE IF NOT EXISTS shares (
	item_id    uuid NOT NULL,
	item_type  text NOT NULL CHECK (item_type IN ('file', 'folder')),
	user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE, -- who it is shared with
	role       text NOT NULL CHECK (role IN ('viewer', 'commenter', 'editor')),
	created_by uuid REFERENCES users(id) ON DELETE SET NULL,
	created_at bigint NOT NULL DEFAULT extract(epoch FROM now())::bigint,
	PRIMARY KEY (item_id, user_id)
);
CREATE INDEX IF NOT EXISTS shares_user ON shares(user_id);
-- Public link, one per file or folder. enabled false = "Restricted" (the link stops working).
-- A new token (reset) and a new password log out every guest of the link.
CREATE TABLE IF NOT EXISTS links (
	item_id       uuid PRIMARY KEY,
	item_type     text NOT NULL CHECK (item_type IN ('file', 'folder')),
	token         text NOT NULL UNIQUE,
	enabled       boolean NOT NULL DEFAULT false,
	role          text NOT NULL DEFAULT 'viewer' CHECK (role IN ('viewer', 'commenter', 'editor')),
	expires_at    bigint,          -- NULL = never
	password_hash text,            -- NULL = no password
	no_download   boolean NOT NULL DEFAULT false,
	require_name  boolean NOT NULL DEFAULT true,
	max_file      bigint NOT NULL DEFAULT 2147483648,  -- guest uploads: bytes per file
	upload_limit  bigint NOT NULL DEFAULT 21474836480, -- guest uploads: bytes in total
	uploaded      bigint NOT NULL DEFAULT 0,
	created_by    uuid REFERENCES users(id) ON DELETE SET NULL,
	created_at    bigint NOT NULL DEFAULT extract(epoch FROM now())::bigint
);
CREATE TABLE IF NOT EXISTS guest_sessions (
	token_hash text PRIMARY KEY,
	item_id    uuid NOT NULL,
	link_token text NOT NULL, -- the link it was opened with; a reset link has another token
	name       text NOT NULL DEFAULT '',
	expires_at bigint NOT NULL
);
CREATE INDEX IF NOT EXISTS guest_sessions_item ON guest_sessions(item_id);
-- Who changed access to what: shares and link settings. The owner can always see and undo it.
CREATE TABLE IF NOT EXISTS access_log (
	id      bigserial PRIMARY KEY,
	item_id uuid NOT NULL,
	at      bigint NOT NULL DEFAULT extract(epoch FROM now())::bigint,
	actor   text NOT NULL, -- username, or "<name> (tamu)"
	action  text NOT NULL
);
CREATE INDEX IF NOT EXISTS access_log_item ON access_log(item_id, id);
-- Comments on files. user_id NULL = a guest of a public link (guest_name, and the guest session
-- hash so they can delete their own comment).
CREATE TABLE IF NOT EXISTS comments (
	id         uuid PRIMARY KEY,
	file_id    uuid NOT NULL,
	user_id    uuid REFERENCES users(id) ON DELETE SET NULL,
	guest_name text,
	guest      text,
	body       text NOT NULL,
	created_at bigint NOT NULL DEFAULT extract(epoch FROM now())::bigint
);
CREATE INDEX IF NOT EXISTS comments_file ON comments(file_id, created_at);
-- Federation (docs/federation.md). Outgoing: items shared with people on other servers.
CREATE TABLE IF NOT EXISTS federated_shares (
	id           uuid PRIMARY KEY,
	item_id      uuid NOT NULL,
	item_type    text NOT NULL,
	shared_by    uuid REFERENCES users(id) ON DELETE SET NULL,
	remote       text NOT NULL,            -- budi@drive.example.org
	remote_host  text NOT NULL,
	role         text NOT NULL CHECK (role IN ('viewer', 'commenter', 'editor')),
	secret_hash  text NOT NULL,
	state        text NOT NULL DEFAULT 'pending', -- pending, active
	allow_copy   boolean NOT NULL DEFAULT true,
	max_file     bigint NOT NULL DEFAULT 2147483648,
	upload_limit bigint NOT NULL DEFAULT 21474836480,
	uploaded     bigint NOT NULL DEFAULT 0,
	created_at   bigint NOT NULL DEFAULT extract(epoch FROM now())::bigint,
	UNIQUE (item_id, remote)
);
CREATE TABLE IF NOT EXISTS fed_grants ( -- people the recipient re-shared with on their own server
	share      uuid NOT NULL REFERENCES federated_shares(id) ON DELETE CASCADE,
	actor      text NOT NULL,
	role       text NOT NULL CHECK (role IN ('viewer', 'commenter', 'editor')),
	granted_by text NOT NULL,
	PRIMARY KEY (share, actor)
);
CREATE TABLE IF NOT EXISTS fed_subscribers ( -- remote people who get this server's bot notifications
	share   uuid NOT NULL REFERENCES federated_shares(id) ON DELETE CASCADE,
	actor   text NOT NULL,
	chat_id bigint NOT NULL,
	link    text NOT NULL, -- where the notification sends them (their own server)
	PRIMARY KEY (share, actor)
);
CREATE TABLE IF NOT EXISTS fed_notify_tokens (
	token_hash text PRIMARY KEY,
	share      uuid NOT NULL REFERENCES federated_shares(id) ON DELETE CASCADE,
	actor      text NOT NULL,
	link       text NOT NULL,
	expires_at bigint NOT NULL
);
-- Incoming: items people on other servers shared with users here.
CREATE TABLE IF NOT EXISTS remote_shares (
	id         uuid PRIMARY KEY,
	user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	owner      text NOT NULL,     -- fikri@drive.example.com
	owner_host text NOT NULL,
	owner_api  text NOT NULL,
	share_id   text NOT NULL,     -- the owner server's id
	secret     text NOT NULL,
	item_type  text NOT NULL,
	item_id    text NOT NULL,     -- the shared item's id on the owner server
	name       text NOT NULL,
	size       bigint,
	files      bigint,
	role       text NOT NULL,
	allow_copy boolean NOT NULL DEFAULT true,
	state      text NOT NULL DEFAULT 'pending', -- pending, active
	created_at bigint NOT NULL DEFAULT extract(epoch FROM now())::bigint,
	UNIQUE (owner_api, share_id)
);
CREATE TABLE IF NOT EXISTS remote_grants ( -- re-shared with accounts on this server
	share   uuid NOT NULL REFERENCES remote_shares(id) ON DELETE CASCADE,
	user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	role    text NOT NULL CHECK (role IN ('viewer', 'commenter', 'editor')),
	PRIMARY KEY (share, user_id)
);
CREATE TABLE IF NOT EXISTS fed_blocked (
	user_id uuid REFERENCES users(id) ON DELETE CASCADE, -- NULL: blocked for the whole server
	host    text NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS fed_blocked_uniq ON fed_blocked (coalesce(user_id, '00000000-0000-0000-0000-000000000000'), host);
-- What guests added: their typed name (they have no account), and uploads they started.
ALTER TABLE files ADD COLUMN IF NOT EXISTS guest_name text;
ALTER TABLE folders ADD COLUMN IF NOT EXISTS guest_name text;
ALTER TABLE uploads ADD COLUMN IF NOT EXISTS guest text;      -- guest session token hash
ALTER TABLE uploads ADD COLUMN IF NOT EXISTS guest_name text;
ALTER TABLE uploads ADD COLUMN IF NOT EXISTS link_item uuid;  -- whose link quota it counts against
`

// Open connects to PostgreSQL (DATABASE_URL, e.g. postgres://user:pass@host:5432/db?sslmode=require)
// and creates the tables if they do not exist yet.
func Open(url string) (*sql.DB, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return db, nil
}

func pgCode(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

// IsUniqueViolation reports a duplicate value in a UNIQUE column (e.g. username).
func IsUniqueViolation(err error) bool { return pgCode(err) == "23505" }

// IsForeignKeyViolation reports a row still referenced by another (e.g. a non-empty folder).
func IsForeignKeyViolation(err error) bool { return pgCode(err) == "23503" }
