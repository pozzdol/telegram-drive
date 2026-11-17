# telegram-drive

A self-hosted cloud drive that stores your files in a private Telegram channel. Folders, names and users live in PostgreSQL; the bytes live in Telegram.

It has a web UI (in Indonesian) that should feel familiar if you use Google Drive, plus a Google Photos-style gallery.

- **Drive.** Folders, list and grid views, sort, search, rename, move, delete, drag and drop upload, right-click menus. Select with Ctrl/Cmd+A, Ctrl/Cmd+click, Shift+click, by dragging a box with the mouse, or by long-pressing on a phone, then move or delete them all at once (folders together with everything inside).
- **Photos.** Every photo and video in one timeline, grouped by the day it was taken (EXIF date for JPEGs), with a full-screen viewer, swipe and keyboard navigation.
- **Previews.** Image thumbnails made on the server; video thumbnails made in the browser, so no ffmpeg is needed.
- **Documents.** Markdown renders in the viewer (headings, tables, task lists, code blocks), sanitised with DOMPurify; other text files (code, logs, JSON, CSV) show as plain text. Big files load only their first 512 KB.
- **Big files.** Chunked, resumable uploads up to 2 GB per file (4 GB with Telegram Premium). Downloads support Range requests, so videos stream and seek.
- **Family accounts.** Each user has a private drive. There is no public sign-up: the first user becomes admin and adds the others from the admin page.
- **Log in with Telegram** (optional). Each user links their Telegram account once from the account menu; Telegram cannot create new accounts.
- **Easy to host.** The UI is static files (Cloudflare Pages, Vercel, Netlify). The backend is a single Go binary with no CGO, behind Cloudflare Tunnel or any reverse proxy.

Built with Go, [gotd/td](https://github.com/gotd/td) (MTProto, logged in as a user account), PostgreSQL ([pgx](https://github.com/jackc/pgx)), React and Vite.

> Telegram is not a backup service. Keep copies of anything you cannot afford to lose, and read Telegram's terms of service before storing large amounts of data.

## Requirements

- Go (version in `go.mod`) and Node.js 22 or newer.
- PostgreSQL 13 or newer, with an empty database for the drive. Tables are created at startup.
- A Telegram account, plus an `api_id` and `api_hash` from <https://my.telegram.org> (API development tools).
- A **private channel** owned by that account. Its id looks like `-1001234567890`; you can see it, for example, by forwarding a channel message to a bot such as @userinfobot.

## Quick start (local)

```bash
git clone https://github.com/pozzdol/telegram-drive.git
cd telegram-drive
cp .env.example .env        # fill in DATABASE_URL and the Telegram values
go run .                    # first run asks for the Telegram login code (and 2FA password)
```

In a second terminal:

```bash
npm --prefix web install
npm --prefix web run dev    # http://localhost:5173, /api is proxied to the backend
```

If the backend does not listen on `5758`, start the UI with `API_PROXY=http://localhost:<port> npm --prefix web run dev`.

Create the first account (it becomes the admin):

```bash
curl -X POST localhost:5758/api/auth/register -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"choose-a-long-password"}'
```

Then sign in in the UI. Add more people from the avatar menu, under **Kelola pengguna**.

### Files you must keep private

- `.env`: your Telegram API credentials.
- `data/telegram.session`: full access to the Telegram account. Never commit or share it.
- The PostgreSQL database: folders, file names and users. Back it up (`pg_dump`): without it the files are still in the channel, but you lose their names and folders.

`data/uploads/` holds chunks while an upload is in progress and `data/thumbs/` holds previews. Both are rebuilt as needed.

## Project layout

```
server.go                       entry point: config, DB, Telegram login, HTTP server
config/config.go                .env and environment variables
app/app.go                      App struct shared by all controllers (DB, Telegram, settings)
app/routes/routes.go            URL to controller mapping, plus CORS
app/controllers/auth/           register, login, logout, sessions, password hashing
app/controllers/user/           admin: family accounts
app/controllers/folder/         list, search, create, rename/move, delete folders
app/controllers/file/           download (Range), previews, gallery, rename/move, delete files
app/controllers/upload/         chunked uploads and background send to Telegram
app/models/                     PostgreSQL schema, row structs, shared queries
app/utils/tgclient/             Telegram client (MTProto) and parallel chunk reader
app/utils/thumb/                image thumbnails, EXIF orientation and date
web/                            React UI (Vite)
deploy/                         systemd unit and deploy script
```

## Deploy

```
Browser -> https://drive.example.com  (static UI: Cloudflare Pages, Vercel, ...)
        -> https://api.example.com    (Cloudflare Tunnel or reverse proxy -> Go backend)
```

Both must be subdomains of **the same domain**. The login cookie uses `SameSite=Strict`, so it is not sent if the UI runs on `*.pages.dev`.

### Backend on a Linux server

No Go is needed on the server: every push to `main` makes CI build Linux binaries (amd64 and arm64) and publish them to the [`edge` release](https://github.com/pozzdol/telegram-drive/releases/tag/edge). The deploy script downloads the one built for the commit it checks out and verifies its checksum. Compiling on the server needs more than 3 GB of RAM because of the Telegram client library.

```bash
# once, as root (needs git and curl)
git clone https://github.com/pozzdol/telegram-drive.git /opt/telegram-drive/src
cp /opt/telegram-drive/src/.env.example /opt/telegram-drive/.env    # fill it in
mkdir -p /opt/telegram-drive/data && chown -R ubuntu:ubuntu /opt/telegram-drive/.env /opt/telegram-drive/data
```

Set `DATABASE_URL` and `ALLOWED_ORIGIN=https://drive.example.com` in `.env`. The unit in `deploy/telegram-drive.service` runs the binary as `ubuntu` and listens on `127.0.0.1` only; edit it if your user differs.

Every deploy, including the first, is one command. It pulls `main`, downloads that commit's binary, installs the unit and restarts the service. Run it after the GitHub Actions run for your push has finished:

```bash
sudo /opt/telegram-drive/src/deploy/deploy.sh
```

The first start needs a Telegram login, which the service cannot type in. Either copy an existing `data/telegram.session`, or stop the service and run the binary once by hand, then stop it with Ctrl-C when it prints `listening on`:

```bash
sudo systemctl stop telegram-drive
cd /opt/telegram-drive && sudo -u ubuntu ./telegram-drive
sudo systemctl start telegram-drive
```

To compile on the server instead (more than 3 GB of RAM, Go in `/usr/local/go`), run the script with `BUILD_FROM_SOURCE=1`.

Never run the same `telegram.session` in two places at once. Telegram may revoke it (`AUTH_KEY_DUPLICATED`).

### Expose the API

With **Cloudflare Tunnel**: in the Cloudflare dashboard go to Zero Trust, Networks, Tunnels. Create a tunnel (or pick one already on the server), then add a public hostname `api.example.com` pointing to `http://127.0.0.1:5758`. No port needs to be opened.

Any reverse proxy (nginx, Caddy) works too. Pass `X-Forwarded-Proto: https` so the login cookie is marked `Secure`, and allow request bodies of at least 50 MB.

### Log in with Telegram (optional)

1. In Telegram, ask @BotFather for `/newbot`, then `/setdomain` with the UI's domain (e.g. `drive.example.com`).
2. Put `TELEGRAM_BOT_TOKEN` and `TELEGRAM_BOT_USERNAME` in the server's `.env` and restart the service.
3. Each user signs in with their password once and picks **Hubungkan Telegram** in the avatar menu. After that the login page's **Masuk dengan Telegram** button works for them.

The server checks Telegram's HMAC signature with the bot token and accepts login data for one hour.

### UI

In Cloudflare Pages, connect this repository and use:

- Root directory: `web`
- Build command: `npm run build`
- Output directory: `dist`
- Environment variable: `VITE_API_URL=https://api.example.com`

Add the custom domain `drive.example.com` to the Pages project. On Vercel the settings are the same: Root Directory `web`, framework Vite, plus the same environment variable.

## Uploading large files

Cloudflare rejects request bodies over 100 MB and cuts off responses after 100 s. Uploads therefore go in 50 MB chunks, and the server sends the file to Telegram in the background:

1. `POST /api/uploads {name,size,mime,folder_id}` returns `{id, chunk_size}`.
2. `PUT /api/uploads/{id}?offset=N` with the raw bytes, in order. If the connection drops, `GET /api/uploads/{id}` returns `received` so the client can continue from there.
3. After the last chunk the status becomes `sending`. Poll `GET /api/uploads/{id}` until it is `done` (with `file_id`) or `error`.

If the server restarts while sending, the upload is resumed automatically at startup.

## API

Authentication uses the `token` cookie set by login, or `Authorization: Bearer <token>`.

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
| GET | `/api/list?folder_id=` | | `{path, folders, files}`; empty or `0` means root |
| POST | `/api/folders` | `{"name","parent_id"}` | |
| PATCH | `/api/folders/{id}` | `{"name"?, "parent_id"?}` | Rename and/or move; `parent_id: 0` means root |
| DELETE | `/api/folders/{id}` | | Empty folders only, otherwise `409` |
| POST | `/api/uploads` | `{"name","size","mime","folder_id"}` | Starts a chunked upload |
| PUT | `/api/uploads/{id}?offset=` | raw bytes | At most `chunk_size` per request |
| GET | `/api/uploads/{id}` | | Upload status |
| GET | `/api/files/{id}` | | Download with Range support; `?inline=1` for image, video, audio, PDF or text |
| GET | `/api/files/{id}/thumb` | | 480 px JPEG preview of an image (JPEG/PNG/GIF/WebP), built once and cached in `data/thumbs/`. For a video: the stored preview, or `404` if none yet |
| PUT | `/api/files/{id}/thumb` | JPEG body (max 1 MB, 1024 px) | Video only. The UI grabs a frame in the browser (`web/src/videoThumb.js`), so the server needs no ffmpeg. Made right after upload, or the first time an older video appears in the UI |
| GET | `/api/media?offset=&limit=` | | All photos and videos, newest first (Photos view) |
| GET | `/api/search?q=` | | Folders and files whose name contains `q` |
| GET | `/api/usage` | | Number of files and total bytes stored |
| PATCH | `/api/files/{id}` | `{"name"?, "folder_id"?}` | Rename and/or move |
| DELETE | `/api/files/{id}` | | Also deletes the message in the channel (skipped when it is already gone) |
| POST | `/api/files/{id}/check` | | Asks Telegram whether the file's message still exists: `{missing}` |
| POST | `/api/move` | `{"files":[ids],"folders":[ids],"to":id}` | Bulk move into one folder (`0` = root), in one transaction; `400` if a folder would go into itself or its subfolder |
| POST | `/api/delete` | `{"files":[ids],"folders":[ids]}` | Bulk delete. Folders go with everything inside them; Telegram messages are deleted first, 100 per call |

## Contributing

Issues and pull requests are welcome. Before opening a PR:

```bash
gofmt -l .                  # must print nothing
go vet ./...
# API tests need a throwaway PostgreSQL database; they drop everything in its public schema.
TEST_DATABASE_URL=postgres://user:pass@localhost:5432/telegram_drive_test?sslmode=disable go test ./...
npm --prefix web run build
```

CI runs the same checks on every push and pull request. Keep changes small and focused, and never include `.env`, `data/` or a Telegram session in a commit.

## License

[MIT](LICENSE)

## Files deleted in Telegram

If a file's message is deleted in the channel itself, the drive keeps its record and marks it **Hilang di Telegram**. The server notices when the file is opened (the API answers `410 Gone`) and by checking every file against Telegram at startup and every 6 hours, 100 messages per request. Opening a missing file explains what happened and offers **Hapus dari aplikasi**, which removes only the record. Telegram has no trash for channel messages, so the content itself cannot be recovered by the drive.

## Known limits

- Downloads fetch 8 chunks of 1 MiB in parallel, and uploads to Telegram use 8 threads. Speed is then limited by the server's own connection: on a ~3.6 MB/s home line, a 1 GB file takes about 5 minutes each way. On a VPS with a faster line it gets much quicker.
- During an upload, the server needs free disk space at least as large as the file (`data/uploads/`).
- Files are limited to 2 GB (4 GB with Premium). There is no automatic `FLOOD_WAIT` handling, and no login rate limiting.
- Cloudflare's terms discourage serving large amounts of video or file traffic through its CDN and Tunnel. For heavy personal use this is usually fine, but it is a risk.
