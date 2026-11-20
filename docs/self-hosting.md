# Self-hosting by hand

The [installer](../README.md#install) does all of this for you on a Debian or Ubuntu server. This page is for
everything else: another Linux, an existing PostgreSQL or reverse proxy, or the split setup this project
runs in production (UI on Cloudflare Pages, API behind Cloudflare Tunnel).

## Requirements

- Go (version in `go.mod`) and Node.js 22 or newer.
- PostgreSQL 13 or newer, with an empty database for the drive. Tables are created at startup.
- A Telegram account, plus an `api_id` and `api_hash` from <https://my.telegram.org> (API development tools).
- Optional: a **private channel** owned by that account (one is created on first login if `TELEGRAM_CHANNEL_ID` is empty). Its id looks like `-1001234567890`; you can see it, for example, by forwarding a channel message to a bot such as @userinfobot.


## Configuration

All settings live in `.env` next to the binary (see [.env.example](../.env.example)). The binary also has two
helper commands:

```bash
./telegram-drive login          # log in to Telegram once (asks for the code), set up the storage channel
echo 'a-long-password' | ./telegram-drive admin NAME   # create or reset an admin account
```

Release binaries carry the web UI and serve it on the same address as the API, so one process and one
domain are enough. To host the UI elsewhere instead, see [Split deployment](#split-deployment).

## Split deployment

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


## Files you must keep private

- `.env`: your Telegram API credentials.
- `data/telegram.session`: full access to the Telegram account. Never commit or share it.
- The PostgreSQL database: folders, file names and users. Back it up (`pg_dump`): without it the files are still in the channel, but you lose their names and folders.

`data/uploads/` holds chunks while an upload is in progress and `data/thumbs/` holds previews. Both are rebuilt as needed.


## IDs

Users, folders and files are identified by **UUIDv7** (`0190a3b2-…`): nobody can guess or count another family member's files from an id, and because UUIDv7 starts with a timestamp, new rows still land at the end of the primary-key index. The server creates them (`models.NewID`); the root folder is `""` in the API (`folder_id`, `parent_id`, `to`).

Databases created before this change (numeric ids) are converted once with `deploy/migrate-uuid.sql`. Stop the service, back up with `pg_dump`, run the script with `psql -v ON_ERROR_STOP=1 -f`, delete `data/thumbs/*`, then start the new version. The script runs in one transaction and refuses to run twice.


## Files deleted in Telegram

If a file's message is deleted in the channel itself, the drive keeps its record and marks it **Hilang di Telegram**. The server notices when the file is opened (the API answers `410 Gone`) and by checking every file against Telegram at startup and every 6 hours, 100 messages per request. Opening a missing file explains what happened and offers **Hapus dari aplikasi**, which removes only the record. Telegram has no trash for channel messages, so the content itself cannot be recovered by the drive.


## Known limits

- Downloads fetch 8 chunks of 1 MiB in parallel, and uploads to Telegram use 8 threads. Speed is then limited by the server's own connection: on a ~3.6 MB/s home line, a 1 GB file takes about 5 minutes each way. On a VPS with a faster line it gets much quicker.
- During an upload, the server needs free disk space at least as large as the file (`data/uploads/`).
- Files are limited to 2 GB (4 GB with Premium). There is no automatic `FLOOD_WAIT` handling.
- Cloudflare's terms discourage serving large amounts of video or file traffic through its CDN and Tunnel. For heavy personal use this is usually fine, but it is a risk.
