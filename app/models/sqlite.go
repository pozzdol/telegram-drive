package models

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"regexp"
	"strings"
	"sync"

	sqlite "modernc.org/sqlite"
)

// SQLite support. The code writes PostgreSQL; for SQLite a thin driver ("sqlite-td") rewrites each
// statement before it reaches modernc.org/sqlite, so every controller keeps one version of its SQL:
//
//	$1                      -> ?1
//	x = ANY($1::uuid[])     -> x IN (SELECT value FROM json_each(?1))   (the []string arg becomes JSON)
//	x <> ALL($1::uuid[])    -> x NOT IN (SELECT value FROM json_each(?1))
//	::uuid ::bigint ::text  -> (removed: SQLite is dynamically typed)
//	extract(epoch FROM now()) -> unixepoch()
//	IS NOT DISTINCT FROM    -> IS
//	ILIKE                   -> LIKE   (SQLite's LIKE ignores ASCII case)
//
// Only text outside single-quoted literals is rewritten. Statements outside this subset
// (LATERAL, DISTINCT ON, …) must not be used in shared code.

var (
	reAny      = regexp.MustCompile(`=\s*ANY\(\s*\$(\d+)(?:::[a-z]+\[\])?\s*\)`)
	reAll      = regexp.MustCompile(`<>\s*ALL\(\s*\$(\d+)(?:::[a-z]+\[\])?\s*\)`)
	reEpoch    = regexp.MustCompile(`(?i)extract\(epoch FROM now\(\)\)`)
	reCast     = regexp.MustCompile(`::(uuid|bigint|text|int|integer|boolean)(\[\])?`)
	reParam    = regexp.MustCompile(`\$(\d+)`)
	reDistinct = regexp.MustCompile(`(?i)IS NOT DISTINCT FROM`)
	reILike    = regexp.MustCompile(`\bILIKE\b`)
)

var rewritten sync.Map // query -> rewritten query

func toSQLite(q string) string {
	if v, ok := rewritten.Load(q); ok {
		return v.(string)
	}
	var b strings.Builder
	for i, part := range strings.Split(q, "'") {
		if i%2 == 1 { // inside a string literal
			b.WriteString("'" + part + "'")
			continue
		}
		part = reAny.ReplaceAllString(part, "IN (SELECT value FROM json_each(?$1))")
		part = reAll.ReplaceAllString(part, "NOT IN (SELECT value FROM json_each(?$1))")
		part = reEpoch.ReplaceAllString(part, "unixepoch()")
		part = reCast.ReplaceAllString(part, "")
		part = reDistinct.ReplaceAllString(part, "IS")
		part = reILike.ReplaceAllString(part, "LIKE")
		part = reParam.ReplaceAllString(part, "?$1")
		b.WriteString(part)
	}
	out := b.String()
	rewritten.Store(q, out)
	return out
}

type sqliteDriver struct{ base *sqlite.Driver }

func (d *sqliteDriver) Open(name string) (driver.Conn, error) {
	c, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &sqliteConn{c}, nil
}

// sqliteConn passes everything to modernc's connection after rewriting the SQL.
type sqliteConn struct{ driver.Conn }

func (c *sqliteConn) Prepare(q string) (driver.Stmt, error) { return c.Conn.Prepare(toSQLite(q)) }

func (c *sqliteConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, toSQLite(q))
}

func (c *sqliteConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, toSQLite(q), args)
}

func (c *sqliteConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, toSQLite(q), args)
}

func (c *sqliteConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

// CheckNamedValue turns the []string arguments of ANY(...) into the JSON json_each reads.
func (c *sqliteConn) CheckNamedValue(nv *driver.NamedValue) error {
	if s, ok := nv.Value.([]string); ok {
		b, _ := json.Marshal(s)
		nv.Value = string(b)
		return nil
	}
	return driver.ErrSkip
}

func init() { sql.Register("sqlite-td", &sqliteDriver{base: &sqlite.Driver{}}) }

// SQLite schema: the same tables as the PostgreSQL schema in db.go, written for a new database
// (SQLite installs never had the old columns, so there is nothing to ALTER).
// sqliteDSN turns DATABASE_URL (sqlite:data/drive.db, sqlite:///var/lib/drive.db) into a
// modernc DSN: foreign keys on (SQLite has them off by default), WAL so reads do not wait for
// writes, a busy timeout, and write transactions that take the lock at BEGIN (no deadlocks
// between two transactions that both read first).
func sqliteDSN(url string) (path, dsn string) {
	path = strings.TrimPrefix(strings.TrimPrefix(url, "sqlite:"), "//")
	return path, "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_txlock=immediate"
}

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS users (
	id            TEXT PRIMARY KEY,
	username      TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	is_admin      INTEGER NOT NULL DEFAULT 0,
	created_at    INTEGER NOT NULL DEFAULT (unixepoch()),
	telegram_id   INTEGER UNIQUE
);
CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_user ON sessions(user_id);
CREATE TABLE IF NOT EXISTS folders (
	id         TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	parent_id  TEXT REFERENCES folders(id),
	name       TEXT NOT NULL,
	created_at INTEGER NOT NULL DEFAULT (unixepoch()),
	trashed_at INTEGER,
	trash_id   TEXT,
	trashed_by TEXT REFERENCES users(id) ON DELETE SET NULL,
	guest_name TEXT
);
CREATE INDEX IF NOT EXISTS folders_parent ON folders(user_id, parent_id);
CREATE INDEX IF NOT EXISTS folders_trash ON folders(trash_id) WHERE trash_id IS NOT NULL;
CREATE TABLE IF NOT EXISTS files (
	id         TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	folder_id  TEXT REFERENCES folders(id),
	name       TEXT NOT NULL,
	size       INTEGER NOT NULL,
	mime       TEXT NOT NULL,
	msg_id     INTEGER NOT NULL,
	created_at INTEGER NOT NULL DEFAULT (unixepoch()),
	taken_at   INTEGER,
	missing_at INTEGER,
	trashed_at INTEGER,
	trash_id   TEXT,
	trashed_by TEXT REFERENCES users(id) ON DELETE SET NULL,
	guest_name TEXT
);
CREATE INDEX IF NOT EXISTS files_folder ON files(user_id, folder_id);
CREATE INDEX IF NOT EXISTS files_created ON files(user_id, created_at);
CREATE INDEX IF NOT EXISTS files_trash ON files(trash_id) WHERE trash_id IS NOT NULL;
CREATE TABLE IF NOT EXISTS uploads (
	id         TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	folder_id  TEXT,
	name       TEXT NOT NULL,
	size       INTEGER NOT NULL,
	mime       TEXT NOT NULL,
	received   INTEGER NOT NULL DEFAULT 0,
	sent       INTEGER NOT NULL DEFAULT 0,
	status     TEXT NOT NULL DEFAULT 'receiving',
	file_id    TEXT,
	error      TEXT,
	created_at INTEGER NOT NULL DEFAULT (unixepoch()),
	guest      TEXT,
	guest_name TEXT,
	link_item  TEXT
);
CREATE TABLE IF NOT EXISTS shares (
	item_id    TEXT NOT NULL,
	item_type  TEXT NOT NULL CHECK (item_type IN ('file', 'folder')),
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	role       TEXT NOT NULL CHECK (role IN ('viewer', 'commenter', 'editor')),
	created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
	created_at INTEGER NOT NULL DEFAULT (unixepoch()),
	PRIMARY KEY (item_id, user_id)
);
CREATE INDEX IF NOT EXISTS shares_user ON shares(user_id);
CREATE TABLE IF NOT EXISTS links (
	item_id       TEXT PRIMARY KEY,
	item_type     TEXT NOT NULL CHECK (item_type IN ('file', 'folder')),
	token         TEXT NOT NULL UNIQUE,
	enabled       INTEGER NOT NULL DEFAULT 0,
	role          TEXT NOT NULL DEFAULT 'viewer' CHECK (role IN ('viewer', 'commenter', 'editor')),
	expires_at    INTEGER,
	password_hash TEXT,
	no_download   INTEGER NOT NULL DEFAULT 0,
	require_name  INTEGER NOT NULL DEFAULT 1,
	max_file      INTEGER NOT NULL DEFAULT 2147483648,
	upload_limit  INTEGER NOT NULL DEFAULT 21474836480,
	uploaded      INTEGER NOT NULL DEFAULT 0,
	created_by    TEXT REFERENCES users(id) ON DELETE SET NULL,
	created_at    INTEGER NOT NULL DEFAULT (unixepoch())
);
CREATE TABLE IF NOT EXISTS guest_sessions (
	token_hash TEXT PRIMARY KEY,
	item_id    TEXT NOT NULL,
	link_token TEXT NOT NULL,
	name       TEXT NOT NULL DEFAULT '',
	expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS guest_sessions_item ON guest_sessions(item_id);
CREATE TABLE IF NOT EXISTS access_log (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	item_id TEXT NOT NULL,
	at      INTEGER NOT NULL DEFAULT (unixepoch()),
	actor   TEXT NOT NULL,
	action  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS access_log_item ON access_log(item_id, id);
CREATE TABLE IF NOT EXISTS comments (
	id         TEXT PRIMARY KEY,
	file_id    TEXT NOT NULL,
	user_id    TEXT REFERENCES users(id) ON DELETE SET NULL,
	guest_name TEXT,
	guest      TEXT,
	body       TEXT NOT NULL,
	created_at INTEGER NOT NULL DEFAULT (unixepoch())
);
CREATE INDEX IF NOT EXISTS comments_file ON comments(file_id, created_at);
`
