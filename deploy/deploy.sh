#!/usr/bin/env bash
# Pull the latest code from GitHub, test, build and restart the service.
# Run on the server as root:  sudo /opt/telegram-drive/src/deploy/deploy.sh
# First time:  sudo git clone https://github.com/pozzdol/telegram-drive.git /opt/telegram-drive/src
# Needs Go (https://go.dev/dl) in /usr/local/go or on PATH, and git.
set -euo pipefail

APP=${APP:-/opt/telegram-drive}
SRC=$APP/src
BRANCH=${BRANCH:-main}
PORT=$(sed -n 's/^PORT=//p' "$APP/.env" 2>/dev/null | tail -1)
export PATH=$PATH:/usr/local/go/bin

git -C "$SRC" fetch --quiet origin "$BRANCH"
git -C "$SRC" reset --quiet --hard "origin/$BRANCH"
echo "deploying $(git -C "$SRC" log -1 --format='%h %s')"

cd "$SRC"
go test ./...
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$APP/telegram-drive.new" .
mv "$APP/telegram-drive.new" "$APP/telegram-drive" # atomic: the running process keeps the old file

if ! cmp -s deploy/telegram-drive.service /etc/systemd/system/telegram-drive.service; then
  cp deploy/telegram-drive.service /etc/systemd/system/telegram-drive.service
  systemctl daemon-reload
  systemctl enable telegram-drive
fi
systemctl restart telegram-drive

for _ in $(seq 20); do
  if curl -fsS "http://127.0.0.1:${PORT:-8080}/api/health" >/dev/null; then
    echo "ok: telegram-drive is up"
    exit 0
  fi
  sleep 1
done
echo "telegram-drive did not become healthy; last logs:" >&2
journalctl -u telegram-drive -n 30 --no-pager >&2
exit 1
