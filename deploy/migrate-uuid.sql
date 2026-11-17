-- One-time migration: numeric ids (bigint) -> UUIDv7, for databases created before the switch.
-- Run once, with the service stopped, after a backup:
--   pg_dump "$DATABASE_URL" > backup.sql
--   psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f deploy/migrate-uuid.sql
-- Everything happens in one transaction: on any error nothing changes.
-- Afterwards: everyone logs in again (sessions are cleared), unfinished uploads are dropped,
-- and image previews are rebuilt on first view (delete data/thumbs/*, they are named by id).

BEGIN;

DO $$ BEGIN
  IF (SELECT data_type FROM information_schema.columns WHERE table_name = 'users' AND column_name = 'id') = 'uuid' THEN
    RAISE EXCEPTION 'already migrated: users.id is uuid';
  END IF;
  IF EXISTS (SELECT 1 FROM uploads WHERE status IN ('receiving', 'sending')) THEN
    RAISE EXCEPTION 'uploads still in progress: let them finish (or fail) first';
  END IF;
END $$;

-- UUIDv7 from a creation time (seconds) plus the old id, so the new ids keep the old order.
CREATE FUNCTION pg_temp.uuid7(created bigint, old bigint) RETURNS uuid LANGUAGE sql VOLATILE AS $$
  SELECT encode(set_bit(set_bit(overlay(uuid_send(gen_random_uuid())
    PLACING substring(int8send(created * 1000 + old % 1000) FROM 3) FROM 1 FOR 6), 52, 1), 53, 1), 'hex')::uuid
$$;

CREATE TEMP TABLE map_users   AS SELECT id AS old, pg_temp.uuid7(created_at, id) AS new FROM users;
CREATE TEMP TABLE map_folders AS SELECT id AS old, pg_temp.uuid7(created_at, id) AS new FROM folders;
CREATE TEMP TABLE map_files   AS SELECT id AS old, pg_temp.uuid7(created_at, id) AS new FROM files;

ALTER TABLE users RENAME TO users_old;
ALTER TABLE sessions RENAME TO sessions_old;
ALTER TABLE folders RENAME TO folders_old;
ALTER TABLE files RENAME TO files_old;
ALTER TABLE uploads RENAME TO uploads_old;

-- Same tables as app/models/db.go, with uuid ids.
CREATE TABLE users (
	id            uuid PRIMARY KEY,
	username      text NOT NULL UNIQUE,
	password_hash text NOT NULL,
	is_admin      boolean NOT NULL DEFAULT false,
	created_at    bigint NOT NULL DEFAULT extract(epoch FROM now())::bigint,
	telegram_id   bigint UNIQUE
);
CREATE TABLE sessions (
	token_hash text PRIMARY KEY,
	user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at bigint NOT NULL
);
CREATE TABLE folders (
	id         uuid PRIMARY KEY,
	user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	parent_id  uuid REFERENCES folders(id),
	name       text NOT NULL,
	created_at bigint NOT NULL DEFAULT extract(epoch FROM now())::bigint
);
CREATE TABLE files (
	id         uuid PRIMARY KEY,
	user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	folder_id  uuid REFERENCES folders(id),
	name       text NOT NULL,
	size       bigint NOT NULL,
	mime       text NOT NULL,
	msg_id     bigint NOT NULL,
	created_at bigint NOT NULL DEFAULT extract(epoch FROM now())::bigint,
	taken_at   bigint,
	missing_at bigint
);
CREATE TABLE uploads (
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

INSERT INTO users (id, username, password_hash, is_admin, created_at, telegram_id)
	SELECT m.new, u.username, u.password_hash, u.is_admin, u.created_at, u.telegram_id
	FROM users_old u JOIN map_users m ON m.old = u.id;

-- Parents before children is not needed: the parent_id check runs at the end of the statement.
INSERT INTO folders (id, user_id, parent_id, name, created_at)
	SELECT mf.new, mu.new, mp.new, f.name, f.created_at
	FROM folders_old f
	JOIN map_folders mf ON mf.old = f.id
	JOIN map_users mu ON mu.old = f.user_id
	LEFT JOIN map_folders mp ON mp.old = f.parent_id;

INSERT INTO files (id, user_id, folder_id, name, size, mime, msg_id, created_at, taken_at, missing_at)
	SELECT mi.new, mu.new, md.new, f.name, f.size, f.mime, f.msg_id, f.created_at, f.taken_at, f.missing_at
	FROM files_old f
	JOIN map_files mi ON mi.old = f.id
	JOIN map_users mu ON mu.old = f.user_id
	LEFT JOIN map_folders md ON md.old = f.folder_id;

DROP TABLE uploads_old, sessions_old, files_old, folders_old, users_old CASCADE;

CREATE INDEX IF NOT EXISTS sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS folders_parent ON folders(user_id, parent_id);
CREATE INDEX IF NOT EXISTS files_folder ON files(user_id, folder_id);
CREATE INDEX IF NOT EXISTS files_created ON files(user_id, created_at);

-- Row counts should match the ones before the migration.
SELECT 'users' AS t, count(*) FROM users UNION ALL SELECT 'folders', count(*) FROM folders UNION ALL SELECT 'files', count(*) FROM files;

COMMIT;
