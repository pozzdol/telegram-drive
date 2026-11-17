#!/usr/bin/env bash
# Update the server to the latest main: pull the repo (for the systemd unit), download the
# binary CI built for that commit from the "edge" GitHub release, then restart the service.
# Run on the server as root:  sudo /opt/telegram-drive/src/deploy/deploy.sh
# First time:  sudo git clone https://github.com/pozzdol/telegram-drive.git /opt/telegram-drive/src
# Needs git and curl. BUILD_FROM_SOURCE=1 compiles here instead (needs Go and more than 3 GB of RAM).
set -euo pipefail

APP=${APP:-/opt/telegram-drive}
SRC=$APP/src
BRANCH=${BRANCH:-main}
REPO=${REPO:-pozzdol/telegram-drive}
PORT=$(sed -n 's/^PORT=//p' "$APP/.env" 2>/dev/null | tail -1)

git -C "$SRC" fetch --quiet origin "$BRANCH"
git -C "$SRC" reset --quiet --hard "origin/$BRANCH"
HEAD=$(git -C "$SRC" rev-parse HEAD)
echo "deploying $(git -C "$SRC" log -1 --format='%h %s')"

if [ "${BUILD_FROM_SOURCE:-}" = 1 ]; then
  export PATH=$PATH:/usr/local/go/bin
  (cd "$SRC" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$APP/telegram-drive.new" .)
else
  case $(uname -m) in x86_64) ARCH=amd64 ;; aarch64 | arm64) ARCH=arm64 ;; *) echo "unsupported CPU $(uname -m)" >&2; exit 1 ;; esac
  URL=https://github.com/$REPO/releases/download/edge
  BUILT=$(curl -fsSL "$URL/commit.txt")
  if [ "$BUILT" != "$HEAD" ]; then
    echo "CI has not published a binary for $HEAD yet (edge is ${BUILT:0:7}). Wait for the GitHub Actions run, then retry." >&2
    exit 1
  fi
  TMP=$(mktemp -d)
  trap 'rm -rf "$TMP"' EXIT
  curl -fsSL -o "$TMP/telegram-drive-linux-$ARCH" "$URL/telegram-drive-linux-$ARCH"
  curl -fsSL -o "$TMP/telegram-drive-linux-$ARCH.sha256" "$URL/telegram-drive-linux-$ARCH.sha256"
  (cd "$TMP" && sha256sum -c --quiet "telegram-drive-linux-$ARCH.sha256")
  install -m 755 "$TMP/telegram-drive-linux-$ARCH" "$APP/telegram-drive.new"
fi
mv "$APP/telegram-drive.new" "$APP/telegram-drive" # atomic: the running process keeps the old file

if ! cmp -s "$SRC/deploy/telegram-drive.service" /etc/systemd/system/telegram-drive.service; then
  cp "$SRC/deploy/telegram-drive.service" /etc/systemd/system/telegram-drive.service
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
