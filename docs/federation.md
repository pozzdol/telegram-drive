# Federation: sharing between Telegram Drive servers

Status: **draft, not implemented.** This document describes how two self-hosted Telegram Drive
servers share files and folders with each other. Comments and changes are welcome before any code
is written.

## 1. Goals

- A user on one server shares a file or folder with a user on another server, by address:
  `budi@drive.example.org`.
- The recipient opens, downloads, and (with the editor role) adds to and changes the shared item from
  their own server, as if it were shared inside one server. Both sides see the same contents.
- The recipient may keep an **offline copy**: their server stores the shared files in its own Telegram
  channel and keeps them up to date, so they stay available when the owner's server is down.
- Edits made while the owner's server is unreachable are queued and applied later. When two edits
  collide, both versions are kept; nothing is silently lost.
- Large files (up to Telegram's 2 GB) transfer in resumable chunks and stream with Range requests.
- Any server can send an invitation, but nothing appears until the recipient accepts it. A recipient
  can block a server.

### Non-goals (for now)

- Interoperability with Nextcloud/ownCloud. Names and flows follow
  [Open Cloud Mesh](https://github.com/cs3org/OCM-API) where that costs nothing, so an OCM adapter can
  be added later (see section 14).
- Full two-way replication where both servers own a copy. The owner's server is always the single
  source of truth; the offline copy is a cache that survives outages.
- Sharing with people who have no account on any server. Public links already cover that.

## 2. Terms

| Term | Meaning |
| --- | --- |
| **Owner server** (O) | The server where the shared item lives, in its owner's Telegram channel. |
| **Recipient server** (R) | The server of the person the item is shared with. |
| **Address** | `username@host`, where `host` is the domain people open in the browser (the UI). |
| **Federated share** | One item shared from one owner to one remote address, with a role. |
| **Share secret** | A random 256-bit token O gives R for one share; R presents it on every data request. |
| **Offline copy** | R's own copy of the shared files, stored in R's Telegram channel. |
| **Outbox** | R's queue of edits made while O was unreachable. |

## 3. Discovery

A server is found from the host part of an address.

```
GET https://{host}/.well-known/telegram-drive
```

```json
{
  "software": "telegram-drive",
  "versions": ["1"],
  "api": "https://drive-api.example.org",
  "key": { "id": "https://drive-api.example.org/fed/v1/key#2026-11", "alg": "ed25519", "public": "<base64>" }
}
```

- The UI host serves this file. On Cloudflare Pages it is a static file, generated at build time from
  `VITE_API_URL` and the API's public key (fetched once at deploy time). The API also serves the same
  document at `GET {api}/fed/v1/server`, which is the authoritative copy of the key.
- `api` may be on another host (our default layout has `drive.` for the UI and `drive-api.` for the API).
- Results are cached for 1 hour. A server that answers with another `software` or no common version is
  refused.

## 4. Server identity and request signing

- At first start every server makes an **Ed25519 key pair**, kept in `data/federation.key` (mode 600,
  never committed, alongside `telegram.session`). The public key is published in discovery.
- Every server-to-server request is signed with
  [HTTP Message Signatures (RFC 9421)](https://www.rfc-editor.org/rfc/rfc9421), covering `@method`,
  `@target-uri`, `content-digest` (RFC 9530, SHA-256) when there is a body, and `created`.
  `keyid` is the key's `id` from discovery.
- The receiver fetches the sender's key through discovery of the sender's host, which must match the
  host named in the request (for example the `owner` address of an invitation).
- Requests older than 5 minutes are rejected, and the signature value is remembered for 10 minutes to
  stop replays.
- **Key rotation:** discovery may list `previous_keys` (id, public key, valid until). A server that
  sees an unknown key id for a host it already knows re-fetches discovery once; if the old key is not
  listed as rotated, the admin is warned and the server is treated as untrusted until they confirm.

## 5. Sharing lifecycle

### 5.1 Inviting

1. In the share dialog the owner types `budi@drive.example.org` and a role (viewer, commenter or
   editor). O runs discovery for `drive.example.org`.
2. O stores a `federated_shares` row (state `pending`, the hash of a new share secret) and sends:

```
POST {R api}/fed/v1/invitations        (signed by O)
```
```json
{
  "share_id": "0192…",
  "recipient": "budi",
  "owner": { "address": "fikri@drive.fkriachmd.qzz.io", "name": "fikri" },
  "item": { "type": "folder", "name": "Liburan 2026", "size": 734003200, "files": 214 },
  "role": "editor",
  "secret": "<base64url, 32 bytes>",
  "expires_at": null,
  "offline_copy": true
}
```

`share_id` is O's id for the share (UUIDv7); `recipient` is the username on R. `offline_copy` says
whether the owner allows an offline copy (section 8); it is on unless the owner turns it off.

3. R answers `202 Accepted` whether or not `budi` exists, so addresses cannot be probed. An
   invitation to an unknown user, or from a blocked server, is dropped silently.
4. R shows Budi a notice: *fikri@drive.fkriachmd.qzz.io wants to share "Liburan 2026" with you*, with
   **Accept**, **Decline** and **Block server**.

Limits on R: 20 invitations per sending server per hour, 200 pending invitations per user.

### 5.2 Accepting, declining, leaving

```
POST {O api}/fed/v1/shares/{share_id}/accept     (signed by R, Authorization: Bearer <secret>)
POST {O api}/fed/v1/shares/{share_id}/decline
POST {O api}/fed/v1/shares/{share_id}/leave      // recipient removes it later
```

Accepting moves the share to `active` on both sides; the item appears in Budi's "Shared with me",
marked with the owner's server. Declining or leaving deletes the share on O.

### 5.3 Owner changes and revoking

```
POST {R api}/fed/v1/shares/{share_id}/update     (signed by O)  { "role": "viewer", "offline_copy": false }
POST {R api}/fed/v1/shares/{share_id}/revoke     (signed by O)
```

- A role change takes effect on O at once; the update only tells R so its UI can follow.
- Turning `offline_copy` off makes R stop copying and move its copy to the recipient's trash. O also
  refuses the bulk reads a copy needs (see section 8), so a recipient's server that ignores the
  update cannot keep copying. Files already copied before that cannot be pulled back; the share
  dialog says so.
- After revoke, O rejects the secret. R removes the share. **An offline copy is not deleted**: it
  becomes an ordinary folder in Budi's drive, named "Liburan 2026 (salinan dari fikri@…)". The
  owner's share dialog warns about this before revoking a share whose recipient keeps an offline copy.

If a notification to R fails, O retries with backoff for 3 days. The data endpoints are always the
truth: a revoked secret stops working regardless of whether R heard about it.

## 6. Data access

R reaches the shared item through O's federation API. Every request carries
`Authorization: Bearer <share secret>` **and** R's signature, so a leaked secret alone is not enough.
The secret is scoped to one share; on O it acts like a guest of a public link today: a principal with
the share's role on the shared item and everything inside it (`models.Access` already handles this).
The remote user's address is passed in `Fed-Actor: budi@drive.example.org` and recorded as the author
of what they add, the way guest names are recorded now.

| Method | Path on O (`/fed/v1/shares/{id}/…`) | Role | Same as today's |
| --- | --- | --- | --- |
| GET | `list?folder=` | viewer | `GET /api/list` |
| GET | `files/{file}` (Range) | viewer | `GET /api/files/{id}` |
| GET | `files/{file}/thumb` | viewer | `GET /api/files/{id}/thumb` |
| GET | `files/{file}/comments` · POST | viewer · commenter | comments API |
| POST | `folders` | editor | `POST /api/folders` |
| POST · PUT · GET | `uploads`, `uploads/{u}` | editor | chunked upload API |
| PATCH | `items/{item}` `{name?, parent?}` | editor | rename / move |
| POST | `delete` `{files, folders}` | editor | `POST /api/delete` (to O's trash) |
| GET | `changes?since=` | viewer | new, see section 7 |

- **The browser never talks to O.** Budi's browser calls R as it does now; R proxies to O. The secret
  stays on R's server, R's page needs no new CSP origins, and R can fall back to the offline copy.
- Streaming: R forwards `Range` and streams O's response body to the browser without buffering, so a
  2 GB video plays and seeks while it is still on O.
- Upload from Budi: R receives the chunks as today and forwards each one to O's upload endpoint, so
  only one chunk is held at a time. Chunks keep Cloudflare's 100 MB limit in mind (50 MB, as now).
- Owner-side quotas apply: O's link-style limits (`max_file`, `upload_limit`) also exist per federated
  share and are set in the share dialog.

### 6.1 Re-sharing on the recipient's server

A recipient with the editor role may share a remote item further with accounts on **their own server**
(R), with a role no higher than their own. R does this locally: the new people reach the item through
the same share and secret, and R sends each request with their own address in `Fed-Actor`, so O
records who really did what.

R reports these grants to O so the owner always sees and controls who has access:

```
PUT    {O api}/fed/v1/shares/{id}/grants/{actor}    (signed by R)  { "role": "viewer", "by": "budi@…" }
DELETE {O api}/fed/v1/shares/{id}/grants/{actor}
```

- O lists them in the owner's share dialog under the recipient: "ani@drive.example.org (via budi)".
- O enforces them: a request whose `Fed-Actor` has no grant (and is not the recipient) is refused, and
  each actor is held to their own role. The owner can remove a single re-shared person, which O passes
  on to R with `POST {R api}/fed/v1/shares/{id}/update {"remove": ["ani@…"]}`.
- Revoking the recipient's share removes everyone who got it through them.
- Passing a remote item on to a **third server** is not part of this version: R would have to issue
  its own secrets and answer for O. The recipient can ask the owner to invite that person directly.

## 7. Change feed

R needs to learn what changed, both for its cached view and for the offline copy. O keeps an
append-only log per shared root.

```
GET {O api}/fed/v1/shares/{id}/changes?since={cursor}&limit=500
```
```json
{
  "cursor": "18233",
  "more": false,
  "changes": [
    { "seq": 18231, "op": "upsert", "type": "file", "id": "0192…", "parent": "0192…", "name": "akad.mp4",
      "size": 1934022011, "mime": "video/mp4", "created_at": 1795140000, "taken_at": null },
    { "seq": 18232, "op": "move",   "type": "folder", "id": "0192…", "parent": "0192…", "name": "Resepsi" },
    { "seq": 18233, "op": "delete", "type": "file", "id": "0192…" }
  ]
}
```

- `since` empty returns a full snapshot as `upsert`s, then the cursor.
- **File contents never change** in Telegram Drive: a file is uploaded once and only renamed, moved or
  deleted. So a file id always means the same bytes, and the feed never has to describe content edits.
  This keeps the offline copy and conflict handling simple.
- O pushes a hint when the log grows: `POST {R api}/fed/v1/shares/{id}/notify {"cursor": "18233"}`
  (signed, coalesced to at most one per 10 s). R also polls every 5 minutes, so missed hints only delay.
- `delete` covers moving to O's trash and purging. A restore from O's trash comes back as `upsert`.
- Log entries older than 90 days may be pruned; a cursor older than that gets `410 Gone`, and R starts
  over from a snapshot.

## 8. Offline copy

Budi turns on **Keep offline copy** for an accepted share, unless the owner turned offline copies off
for it. The owner can allow or forbid it per share, at any time (default: allowed). R marks the reads
it makes for the copy with `Fed-Purpose: offline-copy`; O answers them `403` when copies are off.
This is a guard against honest servers, not a lock: anyone who can view a file can still download it.

1. R reads the change feed from the start and, for each file, downloads it from O in 50 MB Range
   chunks (resumable after errors) and uploads it to R's own Telegram channel.
2. R records `mirror_files(share, remote_id, local_msg_id, size)`. Folder structure and names come
   from R's cache of the feed, not from separate local folders, so the copy always mirrors O's layout.
3. While O answers, R serves lists from O and bytes from the offline copy when it has them (saves O's
   bandwidth), otherwise from O. When O does not answer for 10 s, R serves both from its cache and copy
   and shows "Server pemilik sedang tidak bisa dihubungi, kamu melihat salinan offline".
4. **Deletes:** a `delete` in the feed moves R's copy of that file into **Budi's trash** for 30 days
   (the "follows to the other side's trash" rule). Restoring it there turns it into an ordinary file in
   Budi's drive; it does not undo the delete on O.
5. **Quota:** the copy counts against R's storage. Budi sees its size before turning it on, and a
   share can be capped (for example "only files up to 2 GB, at most 100 GB").
6. Turning the copy off moves the copied files to Budi's trash.

## 9. Editing while the owner is offline

With an offline copy and the editor role, Budi can keep working when O is down. R writes each change
to an **outbox** with the feed cursor it was based on, and applies it to its cache so Budi sees it.

When O answers again, R replays the outbox in order. Because contents never change (section 7), only
these collisions can happen, and each keeps everything:

| Budi did (offline) | Meanwhile on O | Result |
| --- | --- | --- |
| Uploaded `a.jpg` | Someone uploaded another `a.jpg` | Both kept; Budi's becomes `a (konflik dari drive.example.org).jpg` |
| Renamed to `x` | Another item got the name `x` | Same suffix rule |
| Renamed or moved item | Item was deleted | Budi's change is dropped; the item stays in Budi's trash from the copy, and Budi is told |
| Moved item into folder F | F was deleted | Item goes to the shared root instead |
| Deleted item | Item was renamed or moved | Delete still applies (to O's trash, recoverable) |
| Created folder `F` | Another folder `F` appeared | Both kept, with the suffix rule |

Uploads made offline are stored in R's Telegram first (so they are safe) and copied to O on replay.
Each outbox entry has an idempotency key, so a replay interrupted halfway can safely run again.

## 10. Notifications

The **owner's server** sends notifications about its items (new comments, and later other events)
to everyone with access, remote people included, through its own bot.

A bot can only write to someone who pressed Start on it, and O does not know the remote person's
Telegram account. So the remote person links themselves to O's bot once per share:

1. Budi opens the shared item on R and chooses **Aktifkan notifikasi**. R asks O:
   `POST {O api}/fed/v1/shares/{id}/notify-link` (signed, with the secret and `Fed-Actor`).
2. O answers with a one-time deep link to its bot, valid 15 minutes:
   `https://t.me/{O bot}?start=fed_<random>`.
3. Budi opens it in Telegram and presses Start. O's server, which now reads its bot's updates
   (long polling `getUpdates`, so no public webhook is needed), matches the start token and stores
   `fed_subscribers(share, actor, chat_id)`.
4. From then on O's bot messages Budi about that share. Budi turns it off with `/stop` in the bot,
   from R's UI (which calls `DELETE …/notify-link`), or it ends when the share ends.

Messages link back to Budi's own server (`https://{R host}/?remote=…`), which R reports at step 1,
so Budi opens the item where he normally works. O never learns Budi's Telegram account except the
chat id he chose to give its bot.

## 11. Security

- **Signatures and secrets** (sections 4 and 6). O stores only a SHA-256 hash of each share secret;
  R must store it in plain form and keeps it in the database next to the share.
- **SSRF:** a server only calls `https://` URLs from discovery; it resolves the host itself and
  refuses loopback, private, link-local and multicast addresses (an admin allow-list exists for LAN
  setups); redirects are not followed; responses have size limits (discovery 16 KB, JSON 4 MB).
- **Abuse:** invitation and request rate limits per remote server, a per-user and server-wide block
  list, and the accept step. Blocking a server declines its pending invitations and revokes nothing on
  other servers.
- **Isolation:** a share secret only reaches the shared item and what is inside it, through the same
  `models.Access` checks as everything else. Federation endpoints never accept user sessions, and user
  endpoints never accept share secrets.
- **Audit:** invitations, accepts, revokes, role changes and offline-copy toggles go to `access_log`.
- **Admin switch:** `FEDERATION=off|invite-only|on` in `.env`; `off` by default on new installs.

## 12. Data model

On every server (each table is used in one direction):

```sql
-- Outgoing: items this server's users share with remote people.
CREATE TABLE federated_shares (
  id           uuid PRIMARY KEY,
  item_id      uuid NOT NULL,
  item_type    text NOT NULL,
  owner_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  remote       text NOT NULL,           -- budi@drive.example.org
  role         text NOT NULL,
  secret_hash  text NOT NULL,
  state        text NOT NULL,           -- pending, active, declined
  allow_copy   boolean NOT NULL DEFAULT true,  -- owner allows an offline copy
  offline_copy boolean NOT NULL DEFAULT false, -- reported by R, for the revoke warning
  max_file     bigint, upload_limit bigint, uploaded bigint NOT NULL DEFAULT 0,
  created_at   bigint NOT NULL
);
CREATE TABLE fed_grants (share uuid NOT NULL REFERENCES federated_shares(id) ON DELETE CASCADE,
  actor text NOT NULL, role text NOT NULL, granted_by text NOT NULL, PRIMARY KEY (share, actor));
CREATE TABLE fed_subscribers (share uuid NOT NULL REFERENCES federated_shares(id) ON DELETE CASCADE,
  actor text NOT NULL, chat_id bigint NOT NULL, PRIMARY KEY (share, actor));
CREATE TABLE fed_changes (seq bigserial PRIMARY KEY, root uuid NOT NULL, op text NOT NULL,
  item_type text NOT NULL, item_id uuid NOT NULL, parent uuid, name text, size bigint, mime text, at bigint NOT NULL);

-- Incoming: items remote people shared with this server's users.
CREATE TABLE remote_shares (
  id uuid PRIMARY KEY,                  -- local id
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  owner text NOT NULL, owner_api text NOT NULL, share_id text NOT NULL, secret text NOT NULL,
  item_type text NOT NULL, name text NOT NULL, role text NOT NULL,
  state text NOT NULL,                  -- pending, active, revoked
  cursor text, offline_copy boolean NOT NULL DEFAULT false,
  UNIQUE (owner_api, share_id)
);
CREATE TABLE remote_items (share uuid NOT NULL, remote_id text NOT NULL, item_type text NOT NULL,
  parent text, name text NOT NULL, size bigint, mime text, msg_id bigint, -- msg_id: offline copy in our channel
  PRIMARY KEY (share, remote_id));
CREATE TABLE fed_outbox (id bigserial PRIMARY KEY, share uuid NOT NULL, op jsonb NOT NULL,
  base_cursor text, idempotency_key text NOT NULL UNIQUE, tries int NOT NULL DEFAULT 0);
CREATE TABLE fed_blocked (user_id uuid, host text NOT NULL, UNIQUE (user_id, host)); -- user_id NULL = server-wide
```

## 13. UI

- **Share dialog:** the people field accepts `name@host` besides local accounts. Remote people show
  their host and a "menunggu diterima" (pending) state, with a switch "Izinkan salinan offline". People
  a recipient re-shared with appear under them ("via budi") and can be removed one by one. The revoke
  dialog warns about offline copies.
- **Notices:** pending invitations at the top of "Dibagikan dengan saya", with Accept, Decline and
  Block server.
- **Shared with me:** remote items show the owner's address and a server icon; their menu has
  "Jaga salinan offline" with the estimated size (hidden when the owner forbids it), "Aktifkan
  notifikasi" (the bot deep link of section 10), "Bagikan" to accounts on this server (editors), and
  "Keluar dari share".
- **Status:** when the owner's server is unreachable, a banner says the copy is offline; queued edits
  show as "menunggu dikirim" until replayed; conflict copies carry a badge.

## 14. Later: Open Cloud Mesh

The flows above map onto OCM so an adapter can let Nextcloud users share with us and back:

| This spec | OCM |
| --- | --- |
| `/.well-known/telegram-drive` | `/.well-known/ocm` (`resourceTypes`, `protocols`) |
| `POST /fed/v1/invitations` | `POST /ocm/shares` (`shareWith`, `owner`, `sharedSecret`) |
| accept / decline / revoke / update | `POST /ocm/notifications` (`SHARE_ACCEPTED`, `SHARE_DECLINED`, `SHARE_UNSHARED`) |
| Data API (section 6) | OCM's `webdav` protocol: we would need a WebDAV endpoint per share |
| RFC 9421 signatures | OCM's optional HTTP signatures (same RFC in recent OCM versions) |

The WebDAV endpoint is the large piece and is why OCM is left for later.

## 15. Phases

1. **Federation core:** discovery, keys and signatures, invitations with accept/decline/block,
   proxied data access (list, stream, upload, edit, delete, comments), revoke and leave, admin switch,
   re-sharing on the recipient's server (grants), notifications from the owner's bot.
2. **Offline copy:** change feed, background copy with resumable transfers, offline reads,
   deletes into the recipient's trash, size caps, the owner's "allow offline copy" switch.
3. **Offline edits:** outbox, replay with idempotency keys, conflict rules of section 9.

## 16. Decisions

Settled with the maintainer before implementation:

1. **Offline copies can be forbidden per share** by the owner (default: allowed). See section 8.
2. **Notifications come from the server that owns the item**, through its own bot, to remote people
   who linked themselves to that bot. See section 10.
3. **Recipients may re-share** a remote item with accounts on their own server, up to their own role;
   the owner sees and can remove those people. See section 6.1.
