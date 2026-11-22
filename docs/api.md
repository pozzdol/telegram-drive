# API

Everything the web UI does goes through this JSON API. Authentication uses the `token` cookie set by login, or `Authorization: Bearer <token>`; guests of a public link send `X-Guest` (see [sharing](sharing.md)).

## Uploading large files

Cloudflare rejects request bodies over 100 MB and cuts off responses after 100 s. Uploads therefore go in 50 MB chunks, and the server sends the file to Telegram in the background:

1. `POST /api/uploads {name,size,mime,folder_id}` returns `{id, chunk_size}`.
2. `PUT /api/uploads/{id}?offset=N` with the raw bytes, in order. If the connection drops, `GET /api/uploads/{id}` returns `received` so the client can continue from there.
3. After the last chunk the status becomes `sending`. Poll `GET /api/uploads/{id}` until it is `done` (with `file_id`) or `error`.

If the server restarts while sending, the upload is resumed automatically at startup.


## Endpoints

| Method | Path | Body | Notes |
|---|---|---|---|
| POST | `/api/auth/register` | `{"username","password"}` | Only the first user (becomes admin); afterwards returns `403` |
| POST | `/api/auth/login` | `{"username","password"}` | Returns `{"token"}` and sets the cookie |
| POST | `/api/auth/logout` | | |
| GET | `/api/me` | | `{id, username, is_admin, telegram_linked}` |
| GET | `/api/auth/config` | | Public. `{telegram_bot, telegram_bot_id}`, or nulls when Telegram login is off |
| POST | `/api/auth/telegram` | data from Telegram's login popup | Logs in the drive account linked to that Telegram account; `403` if none |
| PUT | `/api/me/telegram` | data from Telegram's login popup | Links that Telegram account to the logged-in user; `409` if linked elsewhere |
| DELETE | `/api/me/telegram` | | Unlinks Telegram; password login keeps working |
| PUT | `/api/me/password` | `{"current","new"}` | Logs out the user's other sessions |
| GET | `/api/admin/users` | | Admin only. Users with file count and bytes used |
| POST | `/api/admin/users` | `{"username","password","is_admin"}` | Admin only |
| PATCH | `/api/admin/users/{id}` | `{"username"?,"password"?,"is_admin"?}` | Admin only. A password reset logs that user out; admins cannot demote themselves |
| DELETE | `/api/admin/users/{id}` | | Admin only. `409` while the user still has files; admins cannot delete themselves |
| GET | `/api/list?folder_id=&sort=&limit=&page_token=` | | `{path, folders, files, next_page_token}`: one page (50 by default, folders first), sorted by `name`, `date` or `size`; send `next_page_token` back for the next page. Empty `folder_id` means root |
| POST | `/api/folders` | `{"name","parent_id"}` | |
| PATCH | `/api/folders/{id}` | `{"name"?, "parent_id"?}` | Rename and/or move; `parent_id: ""` means root |
| POST | `/api/uploads` | `{"name","size","mime","folder_id"}` | Starts a chunked upload |
| PUT | `/api/uploads/{id}?offset=` | raw bytes | At most `chunk_size` per request |
| GET | `/api/uploads/{id}` | | Upload status |
| GET | `/api/files/{id}` | | Download with Range support; `?inline=1` for image, video, audio, PDF or text |
| GET | `/api/files/{id}/thumb` | | 480 px JPEG preview of an image (JPEG/PNG/GIF/WebP), built once and cached in `data/thumbs/`. For a video: the stored preview, or `404` if none yet |
| PUT | `/api/files/{id}/thumb` | JPEG body (max 1 MB, 1024 px) | Video only. The UI grabs a frame in the browser (`web/src/videoThumb.js`), so the server needs no ffmpeg. Made right after upload, or the first time an older video appears in the UI |
| GET | `/api/media?limit=&page_token=` | | All photos and videos, newest first (Photos view), 100 per page |
| GET | `/api/search?q=&sort=&page_token=` | | Folders and files whose name contains `q`, paged like `/api/list` |
| GET | `/api/usage` | | Number of files and total bytes stored |
| PATCH | `/api/files/{id}` | `{"name"?, "folder_id"?}` | Rename and/or move |
| POST | `/api/files/{id}/check` | | Asks Telegram whether the file's message still exists: `{missing}` |
| POST | `/api/documents` | `{"folder_id","kind":"md"\|"txt","name"?}` | New empty document (no Telegram message until its first save) |
| PUT | `/api/files/{id}/content` | the whole text (max 1 MB), header `X-Rev` | Saves a `.md`/`.txt` document (editors). `X-Rev` is the version it was based on, from the download's `X-Rev` header; `409 {rev}` if someone saved meanwhile. Saves by one person within 10 minutes form one version |
| GET | `/api/files/{id}/versions` | | `{versions, role, editable}`, newest (current) first |
| GET | `/api/files/{id}/versions/{vid}` | | Content of one version (`?inline=1` as for the file) |
| PATCH | `/api/files/{id}/versions/{vid}` | `{"label"}` | Names a version (`null` removes the name); named versions are never removed automatically |
| POST | `/api/files/{id}/versions/{vid}/restore` | | Makes that version's content a new current version |
| DELETE | `/api/files/{id}/versions/{vid}` | | Owner only; not the current version |
| POST | `/api/files/{id}/live/ticket` | | Editors. `{ticket}`: one use, valid a minute, for the WebSocket below |
| GET | `/api/files/{id}/live?t=<ticket>` | | WebSocket for editing a document together (Yjs updates relayed and stored). Frames are described in `app/controllers/live/live.go` |
| POST | `/api/move` | `{"files":[ids],"folders":[ids],"to":id}` | Bulk move into one folder (`""` = root), in one transaction; `400` if a folder would go into itself or its subfolder |
| POST | `/api/delete` | `{"files":[ids],"folders":[ids]}` | Move to the trash, folders with everything inside them. Nothing leaves Telegram yet |
| GET | `/api/people` | | Other accounts, to pick whom to share with |
| GET | `/api/shared` | | Files and folders others shared with you, with `owner` and your `role` |
| GET | `/api/shares/{id}` | | Who has access to a file or folder: the owner and everyone it is shared with |
| PUT | `/api/shares/{id}` | `{"user_id", "role"}` | Share (or change the role): `viewer`, `commenter` or `editor`. Owners and editors may share |
| DELETE | `/api/shares/{id}/{user}` | | Take access away; anyone may remove their own |
| GET | `/api/links/{id}` | | The item's public link settings and recent access changes (editors) |
| PUT | `/api/links/{id}` | `{"enabled"?, "role"?, "expires_at"?, "password"?, "no_download"?, "require_name"?, "max_file"?, "upload_limit"?}` | Change the public link; `password: ""` removes it |
| POST | `/api/links/{id}/reset` | | New link address; the old one and its guests stop working |
| GET | `/api/s/{token}` | | Public: what a link opens, and whether it needs a password or a name |
| POST | `/api/s/{token}/session` | `{"password"?, "name"?}` | Public: opens a link; returns a guest token for the `X-Guest` header (or `?g=`) |
| GET | `/api/files/{id}/comments` | | Comments on a file, and whether you may write one |
| POST | `/api/files/{id}/comments` | `{"body"}` | Comment (commenter role or higher, guests too); the owner gets a Telegram message from the bot |
| DELETE | `/api/comments/{id}` | | Delete a comment: its author, or an editor of the file |
| GET | `/api/trash` | | Items in the trash (the ones picked, not their contents), with `days` until they are purged |
| POST | `/api/trash/restore` | `{"ids":[ids]}` | Put items back; one whose folder is gone or still in the trash returns to the root |
| POST | `/api/trash/purge` | `{"ids":[ids]}` or `{"all":true}` | Delete for good: Telegram messages first, 100 per call, then the records. Items older than 30 days are purged automatically |

