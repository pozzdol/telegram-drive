# Document editor: Markdown and text, edited together

Status: **stages 1-3 implemented (versions, new documents, the rich editor, editing together); stage 4 (images) is a draft.** This document describes editing `.md` and `.txt` files inside
Telegram Drive, by several people at once, with autosave and version history. Comments and changes are
welcome before any code is written.

## 1. Goals

- Open a `.md` or `.txt` file and edit it in place, in a word-processor view (WYSIWYG): headings,
  bold, lists and tables look like the result, not like Markdown syntax.
- **See source** shows the raw Markdown and lets you edit it directly.
- Several people edit the same document at the same time and see each other's cursors and names,
  as in Google Docs. Typing never blocks and nobody's text is lost.
- Changes save themselves. Every save is a version; old versions can be viewed, named, downloaded and
  restored.
- **New → Document** creates an empty Markdown or text file in the current folder.
- Pasted or dropped images go into a hidden folder next to the document, so the `.md` file stays
  readable in any other Markdown tool.

### Non-goals (for now)

- Other text formats (JSON, CSV, source code). They keep the current read-only preview.
- Comments anchored to a range of text. File comments (the existing panel) stay as they are.
- Suggestion mode, offline editing, exporting to PDF or Word.
- Running more than one server process behind a load balancer (the live hub lives in one process).

## 2. Decisions

| Question | Decision |
| --- | --- |
| Which files | `.md`/`.markdown` and `.txt`, up to 1 MB. Bigger files open read-only. |
| Editor | WYSIWYG, plus **See source** for raw Markdown. |
| Saving | Automatic, with version history. |
| Several editors | Real-time, with cursors and names. |
| Who can edit | Owner, accounts with the editor role, and link guests with the editor role (shown by the name they typed). Commenters and viewers get the existing read-only preview. |
| Markdown formatting | The editor may normalise style when it saves (`*` lists become `-`, spacing tidied). The content stays the same. |
| Images | Stored in a hidden folder `<document name>.assets` beside the document. |
| Version retention | Unnamed versions: 30 days, at most 100 per file. Named versions: kept until deleted. |

## 3. How it works

```
 browser A ─┐                       ┌──────────── telegram-drive ─────────────┐
            │  WebSocket            │  live hub (one per open document)        │
 browser B ─┼── /api/files/{id}/live ─▶  relays edits, stores them in the DB  │
            │                       │                                          │
 browser C ─┘  PUT .../content      │  saves Markdown to Telegram as a new     │
  (the saver)  every few seconds ──▶│  version; the old message becomes a      │
                                    │  row in file_versions                    │
                                    └──────────────────────────────────────────┘
```

- The editor is [TipTap](https://tiptap.dev) (ProseMirror). Real-time editing uses
  [Yjs](https://yjs.dev), a CRDT: each browser holds a copy of the document and Yjs merges
  everyone's changes, in any order, to the same result.
- **The server does not run Yjs.** It relays binary updates between browsers and appends them to the
  database, so a browser that joins late, or reconnects, gets everything it missed. Merging happens in
  the browsers. This keeps the Go side small and free of a CRDT library.
- Telegram cannot change a file in place. A save uploads the Markdown as a new message; the file row
  points at it, and the previous message is kept as a version.
- Between saves the latest text is safe in the database (every update is stored as it arrives), so a
  closed tab or a crashed browser loses nothing.

## 4. Data model

New columns and tables. Both the PostgreSQL schema and the SQLite schema get them.

```sql
ALTER TABLE files   ADD COLUMN updated_at  bigint;          -- last content change; null = never edited
ALTER TABLE folders ADD COLUMN assets_of   uuid;            -- stage 4: set = hidden assets folder of this file

CREATE TABLE file_versions (
  id          uuid PRIMARY KEY,
  file_id     uuid NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  msg_id      bigint NOT NULL,          -- the Telegram message with this version's bytes; 0 = empty
  size        bigint NOT NULL,
  created_at  bigint NOT NULL,          -- when this version started
  saved_at    bigint NOT NULL,          -- its last save
  saved_by    uuid,                     -- account, or null for a guest
  guest_name  text,                     -- guest, or null
  label       text                      -- set = named version, never removed automatically
);
CREATE INDEX file_versions_file ON file_versions(file_id, created_at);

CREATE TABLE doc_live (
  file_id     uuid PRIMARY KEY REFERENCES files(id) ON DELETE CASCADE,
  base_msg_id bigint NOT NULL,          -- the saved version the live state started from
  state       bytea NOT NULL,           -- compacted Yjs state
  seq         bigint NOT NULL DEFAULT 0,-- last update number handed out
  state_seq   bigint NOT NULL DEFAULT 0,-- updates up to this one are folded into state
  saved_seq   bigint NOT NULL DEFAULT 0,-- last update included in a Telegram save
  updated_at  bigint NOT NULL
);

CREATE TABLE doc_updates (
  file_id uuid NOT NULL REFERENCES doc_live(file_id) ON DELETE CASCADE,
  seq     bigint NOT NULL,
  data    bytea NOT NULL,               -- one Yjs update, as sent by a browser
  PRIMARY KEY (file_id, seq)
);
```

- `files.msg_id` stays the current version, so download, preview, sharing and the trash keep working
  unchanged. The newest `file_versions` row is the current version too (same `msg_id`). A file never
  edited has no rows; its first save records the upload as the first version.
- A new empty document has `size = 0` and `msg_id = 0`: Telegram cannot store an empty file. The
  first save gives it a message. Download of a 0-byte file returns an empty body without asking
  Telegram.
- Folders with `assets_of` set are left out of every listing, search, Photos and the folder picker.
  They still count for access checks (inherited from the document's folder like any other folder).

## 5. Live protocol

### Connecting

1. `POST /api/files/{id}/live/ticket` (needs the editor role) returns a one-time ticket valid for
   60 seconds. A ticket is needed because a browser cannot set headers on a WebSocket, and the session
   cookie is not sent when the UI and the API are on different sites.
2. `GET /api/files/{id}/live?t=<ticket>` upgrades to a WebSocket. The server checks the ticket and the
   `Origin` header (same rule as `sameHost`). Uses `github.com/coder/websocket`, already in `go.sum`.

### Messages

Binary frames, first byte is the type.

| Type | Direction | Body |
| --- | --- | --- |
| `hello` | server → client | JSON: `{state, updates[], seq, base_msg_id, you, saver, peers[]}`. Empty `state` means not seeded yet. |
| `seed` | client → server | Yjs state built from the saved file. Accepted only from the first client while no `doc_live` row exists; later seeds get `hello` again. |
| `update` | both | One Yjs update. The server assigns `seq`, stores it in `doc_updates`, and forwards it to the others. |
| `awareness` | both | Cursor, selection, name, colour. Forwarded, never stored. |
| `saver` | server → client | Who saves now (see section 6). Sent when the saver joins or leaves. |
| `compact` | client → server | `{seq, state}` from the saver: a full state that includes every update up to `seq`. The server replaces `state` and deletes those updates. Sent after a save or every 500 updates. |
| `reset` | server → client | The file changed outside the editor (version restored, file replaced, raw source applied by an old client). Clients reload from `hello`. |

- The hub for a document lives in memory while someone has it open and holds a mutex, so updates get
  their `seq` in order. `// ponytail: one process; a shared bus (Postgres LISTEN/NOTIFY) if it ever runs as several`.
- Limits: 256 KB per update, 20 people per document, 50 updates per second per connection; a
  client over a limit is disconnected with a reason the UI shows.
- Updates are not validated beyond size: Yjs updates are opaque to the server. A malicious editor
  can damage a document, as they can today by uploading over it; version history is the protection.

### Seeding

The first browser to open a document with no `doc_live` row downloads the file, parses the
Markdown into the editor, and sends `seed`. If two browsers race, the server keeps the first seed and
answers the second with `hello`, so both end up on the same state.

### As built

- A save from the live saver carries `X-Live-Seq`; any other save (the plain-text editor, a restore,
  an API client) resets the live state, and everyone editing loads the file again.
- A version records who saved it. In a live session that is the saver, whoever typed.
- If the WebSocket cannot be opened after a few tries, the editor edits alone with the autosave of
  stage 2 and says so.
- The embedded UI allows `ws(s)://<its host>` in `connect-src`; a split deployment's `_headers` allows
  the API host as `wss://`.

## 6. Saving and versions

### Who saves

The server names one connected editor the **saver** (the longest-connected one). Only the saver
renders Markdown and uploads it. When the saver leaves, the next one takes over.

### When

- 5 seconds after the last change, and at least every 60 seconds while typing continues.
- When the saver closes the tab (`pagehide`, sent with `fetch(..., {keepalive: true})`).
- If everyone leaves before a save, the updates are still in `doc_updates`. The next person who
  opens the document becomes the saver and saves at once. A small banner on the file
  ("unsaved changes from the last session") shows that the Telegram copy is behind.

### The save request

`PUT /api/files/{id}/content` with the Markdown as the body, and `X-Rev` (the version the client
edited: the download answers with it in an `X-Rev` header). Stage 3 adds `X-Seq` (the last update the
text includes).

1. Reject with 409 (and the current rev) if `X-Rev` is not the file's current version: the file changed underneath;
   the client gets `reset`.
2. Upload the bytes to Telegram as a new message.
3. In one transaction: decide on a version row (below), point `files` at the new message, set
   `size`, `updated_at`, `updated_by`; set `doc_live.base_msg_id` and `saved_seq`.
4. Delete Telegram messages that no longer belong to any version, after the commit.

### Grouping saves into versions

Autosave every few seconds would create thousands of versions, so saves are grouped as Google Docs
does:

- If the current version was last saved less than **10 minutes** ago, by the same person, and is not
  named, the save updates it: its old message is deleted, no new version row.
- Otherwise the save adds a new row, and the previous one stays as an older version.
- A restore always adds a new version.

### History panel

A side panel in the editor, like the comments panel.

- Versions grouped by day: time, who edited, size. The current version is at the top.
- Selecting a version shows it read-only (the same renderer as the preview).
- **Restore** makes that version's content the current version (a new save, so the restore itself can
  be undone), and sends `reset` to everyone editing.
- **Name this version** sets `label`; named versions are never removed automatically.
- **Download** this version.

### Retention

A watcher, like `WatchTrash`, runs every hour: it deletes unnamed versions older than 30 days, and
the oldest unnamed versions beyond 100 per file, then their Telegram messages.

Trash and purge: trashing a document trashes its assets folder in the same group. Purging a
document deletes its versions, `doc_live`, and the assets folder, with all their Telegram messages.

## 7. Editor

### Markdown support

GitHub-flavoured Markdown: headings, bold, italic, strikethrough, inline code, code blocks with a
language, bulleted, numbered and task lists, quotes, links, images, tables, horizontal rules.

The toolbar has these, plus undo and redo (Yjs undo: only your own changes are undone). Common
shortcuts work, and Markdown typed by hand converts as you type (`## ` makes a heading,
`- ` a list).

Things the WYSIWYG view cannot keep exactly:

- **Front matter** (`---` YAML at the top) is kept aside and written back unchanged.
- **Raw HTML, footnotes, reference-style links** and anything else outside the list above: the file
  opens read-only with a notice, *"This file uses Markdown the editor cannot keep. Edit it in
  See source."*
- Style differences (`*` vs `-`, `__` vs `**`, line wrapping) are normalised on the first save.

### See source

A toggle in the toolbar opens the Markdown as plain text in a textarea.

- Read-only while others are editing live, with a note saying so. When you are alone, you can edit it,
  and **Apply** replaces the document in one step.
- `// ponytail: not a second live editor (CodeMirror + y-codemirror) — add if editing raw Markdown
  together turns out to matter`.

### Text files

`.txt` files, and Markdown the rich editor cannot keep, open in a plain-text editor with the same
autosave (an **Edit** button on the preview).

### People

- Each person gets a colour; their cursor and selection show with their name.
- Avatars of everyone in the document sit in the header; hovering shows the name, guests marked
  *(tamu)*.
- A small status line: *Tersimpan*, *Menyimpan…*, *Offline — perubahan disimpan saat tersambung lagi*.

### Images

- Paste, drop, or the toolbar image button uploads the image (normal chunked upload) into the hidden
  folder `<document name>.assets`, created on first use with `assets_of` = the document. The
  Markdown gets `![](catatan.md.assets/foto.png)`.
- Name clashes get a suffix (`foto (2).png`).
- Renaming the document does **not** rename the folder: links keep working, and the folder is hidden
  anyway. Moving the document moves its assets folder with it.
- The editor resolves relative image paths through `GET /api/files/{id}/assets` (name → file id) and
  shows them from the normal download URL, with the guest token where needed.
- Link guests' image uploads count towards the link's upload quota, like any guest upload.

### New document

**New → Dokumen Markdown** and **New → Dokumen teks** in the folder's New menu create
`Dokumen tanpa judul.md` (or `.txt`) with `size = 0` and open it in the editor with the name selected.

### Loading

The editor and Yjs load as a separate chunk only when a document is opened, so the drive itself does
not get heavier. Libraries are pinned to exact versions at least two weeks old.

## 8. API summary

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `POST` | `/api/documents` | editor | New empty `.md`/`.txt` (`{folder_id, kind, name}`) |
| `POST` | `/api/files/{id}/live/ticket` | editor | One-time WebSocket ticket |
| `GET` | `/api/files/{id}/live?t=` | editor | Live editing WebSocket |
| `PUT` | `/api/files/{id}/content` | editor | Save a new version |
| `GET` | `/api/files/{id}/versions` | viewer | List versions |
| `GET` | `/api/files/{id}/versions/{vid}` | viewer | Bytes of one version (respects no-download) |
| `PATCH` | `/api/files/{id}/versions/{vid}` | editor | Name or unname a version |
| `POST` | `/api/files/{id}/versions/{vid}/restore` | editor | Restore a version |
| `DELETE` | `/api/files/{id}/versions/{vid}` | owner | Delete a version |
| `GET` | `/api/files/{id}/assets` | viewer | Image name → file id for the editor |

All take the guest header like the other `guestOK` routes. Every save, restore and version delete
goes to `access_log`.

## 9. Security

- The editor role is checked when the ticket is issued **and** on every save, so a share removed while
  someone is typing ends their session: the hub drops connections whose access was revoked
  (re-checked every 30 seconds and on every save).
- Markdown is rendered through the editor's schema, never as HTML, so a document cannot run scripts.
  The read-only preview keeps using DOMPurify.
- WebSocket: `Origin` check, one-time tickets, size and rate limits as in section 5.
- Image `src` values only resolve to files in the document's own assets folder or to `https:` URLs;
  other schemes are dropped.

## 10. Stages

1. **Versions.** `file_versions`, `PUT /content` with grouping, history panel (view, restore, name,
   download), retention watcher, New → Document with empty files.
2. **Editor, one person.** TipTap with Markdown in and out, toolbar, See source, `.txt` mode,
   autosave through `PUT /content`, read-only fallback for unsupported Markdown.
3. **Real-time.** Tickets, the WebSocket hub, `doc_live`/`doc_updates`, seeding, saver election,
   cursors and avatars, guests, `reset` on outside changes.
4. **Images.** Hidden assets folders, paste and drop upload, path resolving, trash and move rules.

Each stage ships on its own and gets API tests on both databases (`TEST_DB=sqlite` too); stage 3 adds a
test with two WebSocket clients editing at once.

## 11. Open questions

- Should viewers and commenters see the document update live (read-only connection), or is the last
  saved version enough?
- Should a version list show what changed (a diff) or only the full text?
