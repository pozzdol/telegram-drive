# Federation phase 1: implementation plan

Work in progress on branch `federation`. The protocol is in [federation.md](federation.md); this
file tracks how phase 1 is being built and what is left. Federation stays off unless
`FEDERATION=on`, so the branch can merge before it is finished.

## Done

- `app/utils/fed/fed.go`: the server's Ed25519 key (`data/federation.key`, made on first start),
  discovery (`Resolve`: `https://{host}/.well-known/telegram-drive`, then `{api}/fed/v1/server`,
  cached 1 h), RFC 9421 signing and checking with one fixed profile (`@method`, `@target-uri`,
  `content-digest` when there is a body, `created`, 5 min window, replay cache), an HTTP client that
  refuses private addresses unless `FED_ALLOW_PRIVATE=1` and never follows redirects, `Call` for
  signed JSON requests, `ParseAddress`, `AllowedHost` (`FED_ALLOWED_HOSTS`).
- Config: `FEDERATION=on`, `PUBLIC_API_URL` (required when on), `FED_ALLOWED_HOSTS`,
  `FED_ALLOW_PRIVATE`. The address host is the host of `ALLOWED_ORIGIN` (the UI).
- `app.App.Fed` (`*fed.Node`, nil when off), created in `server.go`.
- Tables in `app/models/db.go`: `federated_shares`, `fed_grants`, `fed_subscribers`,
  `fed_notify_tokens` (owner side); `remote_shares`, `remote_grants` (recipient side); `fed_blocked`.

## Next steps, in order

### 1. Discovery endpoints
- API: `GET /fed/v1/server` and `GET /.well-known/telegram-drive` → `Fed.ServerDoc()` (404 when off).
- UI: a Vite plugin (next to `securityHeaders` in `web/vite.config.js`) writes
  `dist/.well-known/telegram-drive` = `{"software":"telegram-drive","api":VITE_API_URL}`, so the
  UI domain in addresses points at the API. Check Cloudflare Pages serves the dot directory.

### 2. Owner side: remote people as guests
Reuse the public-link guest machinery instead of new handlers:
- Extend `models.Guest` with `Remote bool` (label "budi@host" instead of "(tamu)" in `LogAccess` and
  comment notifications) and `Fed string` (federated share id, for quotas).
- `fedAuth` middleware for `/fed/v1/shares/{share}/…`: load the share (state `active`), check
  `Authorization: Bearer <secret>` against `secret_hash`, `Fed.Verify` with the share's
  `remote_host`, read `Fed-Actor` (must be the recipient, or have a `fed_grants` row and the same
  host), role = the lower of share role and grant role. Build
  `Guest{Session: "fed:"+share+":"+actor, ItemID: item, Role, Name: actor, Remote: true, Fed: share}`
  and call the handler with `models.GuestUID`, exactly like `auth.SecureOrGuest`.
- Mount the existing handlers under that prefix (path parameter names stay `{id}`):
  `GET list` (folder.List), `GET files/{id}`, `GET|PUT files/{id}/thumb`, `POST files/{id}/check`,
  `PATCH files/{id}`, `POST folders`, `PATCH folders/{id}`, `POST uploads`, `PUT|GET uploads/{id}`,
  `POST delete`, `POST move`, `GET|POST files/{id}/comments`, `DELETE comments/{id}`.
- Upload quota: in `upload.Create`, when `g.Fed != ""` charge `federated_shares`
  (`max_file`, `upload_limit`, `uploaded`) instead of `links`.
- Control endpoints from R (signed, Bearer): `POST /fed/v1/shares/{id}/accept|decline|leave`,
  `PUT|DELETE /fed/v1/shares/{id}/grants/{actor}`, `POST|DELETE /fed/v1/shares/{id}/notify-link`.
  Decline and leave delete the share; accept sets `active`.

### 3. Owner side: UI API (logged-in users, editor role on the item)
- `POST /api/fed/shares {item_id, address, role, allow_copy}`: `ParseAddress`, refuse our own host,
  `Resolve`, insert `federated_shares` (random 32-byte secret, store SHA-256), send
  `POST {R}/fed/v1/invitations` (body of docs/federation.md 5.1). On failure delete the row, 502.
- `GET /api/shares/{id}` also returns `remote: [{id, address, role, state, allow_copy, grants:[…]}]`.
- `PATCH /api/fed/shares/{id} {role?, allow_copy?}` → update, then `POST {R}/…/update`.
- `DELETE /api/fed/shares/{id}` → revoke: delete row, `POST {R}/…/revoke` (retry later if R is down;
  a deleted row already rejects the secret).
- `DELETE /api/fed/shares/{id}/grants/{actor}` → delete grant, `POST {R}/…/update {"remove":[actor]}`.
- Purging an item from the trash also deletes its `federated_shares` (and so grants, subscribers).
- Write every change to `access_log`.

### 4. Recipient side
- `POST /fed/v1/invitations` (no login; signed by the owner's server): take the host from
  `owner.address`, `Fed.Verify` with it, check `AllowedHost`, `fed_blocked` (server-wide and per
  user), rate limit 20 per host per hour (in memory, like `auth/ratelimit.go`), 200 pending per user.
  Unknown user or blocked: still answer `202`. Insert `remote_shares` (state `pending`).
- `POST /fed/v1/shares/{share_id}/update|revoke` (signed by the owner's server): find the row by
  `share_id` whose `owner_host` signed it; update role/allow_copy/remove grants, or delete it.
- UI API (logged in):
  - `GET /api/remote`: my rows (pending and active) plus rows re-shared to me (`remote_grants`).
  - `POST /api/remote/{id}/accept|decline|leave` → call the owner, then update or delete the row.
  - `POST /api/remote/block {host}` → `fed_blocked`, decline that host's pending invitations.
  - `ANY /api/remote/{id}/{path...}` → proxy to `{owner_api}/fed/v1/shares/{share_id}/{path}?query`
    with `Authorization: Bearer <secret>`, `Fed-Actor: <me>@<our host>`, signed. Allowed: the user is
    the recipient or has a `remote_grants` row. Path segments limited to `[A-Za-z0-9_-]`, no `..`.
    Request bodies over 1 MB (upload chunks) go to a temp file to compute `Content-Digest`, then
    stream. Responses stream back with status and the headers `Content-Type`, `Content-Length`,
    `Content-Range`, `Accept-Ranges`, `Content-Disposition`, `Content-Security-Policy`,
    `Cache-Control`, `ETag`, `Last-Modified`; forward the browser's `Range`.
  - `POST|DELETE /api/remote/{id}/grants {user_id, role}`: re-share with a local account, role at
    most mine, only if I am an editor; then `PUT|DELETE {owner}/…/grants/{actor}`.
  - `POST /api/remote/{id}/notify` → `POST {owner}/…/notify-link {link: "https://<our host>/?remote=<id>"}`,
    returns the bot deep link.

### 5. Notifications from the owner's bot
- If `BotToken` is set and federation is on, long-poll `getUpdates` in the background
  (`app/utils/tgbot`). `/start fed_<token>` → look up `fed_notify_tokens` (15 min, hash stored) →
  insert `fed_subscribers (share, actor, chat_id, link)`, reply "Notifikasi aktif untuk …".
  `/stop` → delete that chat's subscriptions.
- `comment.notify`: besides the owner, message `fed_subscribers` of every federated share whose item
  is the file or one of its folders (not the comment's own author), linking to their `link`.

### 6. Tests
- Two servers in one test: two `httptest.Server`s, each with its own database. Needs a second test
  database: `TEST_DATABASE_URL_2` (create `telegram_drive_test2` on the dev Postgres; in CI add a
  `createdb` step for `drive_test2`). Both nodes with `Scheme = "http"` and `AllowPrivate = true`.
- Cover: discovery; signature (valid, tampered body, old `created`, replay, wrong key);
  invite → accept → list/download/upload through the proxy; viewer cannot write; re-share grant and a
  local user using it; owner removes the grant; revoke (secret refused, row gone on R); block server;
  `FED_ALLOWED_HOSTS`; SSRF guard refuses `127.0.0.1` when `AllowPrivate` is false.

### 7. UI
- `api.js`: a "remote" mode like the guest one: `setRemote(id)` rewrites `/api/list`, `/api/files…`,
  `/api/folders…`, `/api/uploads…`, `/api/delete`, `/api/move`, `/api/comments…` to
  `/api/remote/{id}/…` (also in `fileURL`, `thumbURL`, `putChunk`, `videoThumb`).
- Route `?remote=<id>&folder=…`: Drive inside the remote item, root crumb = item name, the
  share's role decides the actions (same `editable()` logic as guests).
- "Dibagikan dengan saya": pending invitations on top (Terima, Tolak, Blokir server); remote items
  with the owner's address and a server icon; menu: Bagikan (to local accounts), Aktifkan notifikasi,
  Keluar dari share.
- Share dialog (owner): the people field takes `name@host` (sent to `POST /api/fed/shares`), remote
  rows with host, pending state, role dropdown, "Izinkan salinan offline" switch, re-shared people
  listed "via budi", remove buttons.

### 8. Docs and release
- Update `README.md` (env vars, API table) and docs/federation.md where the build differed
  (for example: no key rotation yet; `invite-only` became `FED_ALLOWED_HOSTS`).
- Production: add `PUBLIC_API_URL` and `FEDERATION=on` to `/opt/telegram-drive/.env` only when the
  owner wants it on; `data/federation.key` must be backed up with `telegram.session`.

## Known shortcuts (decide later)
- No key rotation (a changed key is refused until the cached discovery expires).
- Revoke/update notifications to a server that is down are not retried yet.
- Phase 2 (offline copy, change feed) and phase 3 (offline edits) are not started.
