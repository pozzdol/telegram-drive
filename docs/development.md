# Development

## Run it locally

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
app/controllers/share/          sharing with accounts, public links, access log
app/controllers/comment/        comments on files and owner notifications
app/models/                     PostgreSQL schema, row structs, shared queries
app/utils/tgclient/             Telegram client (MTProto) and parallel chunk reader
app/utils/thumb/                image thumbnails, EXIF orientation and date
app/utils/tgbot/                Telegram Bot API messages (notifications)
webui/                          the built UI, embedded in release binaries (-tags embedui)
install.sh                      one-command installer
web/                            React UI (Vite)
deploy/                         systemd unit and deploy script for the split setup
docs/                           API, self-hosting, sharing, federation draft
```


## Before a pull request

Issues and pull requests are welcome. Before opening a PR:

```bash
gofmt -l .                  # must print nothing
go vet ./...
# API tests need a throwaway PostgreSQL database; they drop everything in its public schema.
TEST_DATABASE_URL=postgres://user:pass@localhost:5432/telegram_drive_test?sslmode=disable go test ./...
npm --prefix web run build
```

CI runs the same checks on every push and pull request. Keep changes small and focused, and never include `.env`, `data/` or a Telegram session in a commit.

