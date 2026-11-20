#!/usr/bin/env bash
# Telegram Drive installer: one command on a fresh Linux server (Debian/Ubuntu for the automatic
# PostgreSQL and HTTPS parts; any systemd Linux when you bring your own database and proxy).
#
#   curl -fsSL https://raw.githubusercontent.com/pozzdol/telegram-drive/main/install.sh | sudo bash
#
# It asks for what .env needs, sets up the database (SQLite, or PostgreSQL), downloads the release binary
# (UI included), logs in to Telegram (you type the code it sends), creates the first admin,
# and starts a systemd service. Run it again to update an existing install.
#
# Every answer can be given as an environment variable instead (for scripts):
#   TD_API_ID TD_API_HASH TD_PHONE TD_CHANNEL_ID TD_DATABASE_URL TD_DOMAIN TD_PORT
#   TD_BOT_TOKEN TD_BOT_USERNAME TD_ADMIN TD_ADMIN_PASSWORD
# TD_DRY_RUN=1 prints the system changes instead of making them.
set -euo pipefail

REPO=${TD_REPO:-pozzdol/telegram-drive}
APP=${TD_DIR:-/opt/telegram-drive}
USER_NAME=${TD_USER:-telegram-drive}
SERVICE=telegram-drive
RELEASE=https://github.com/$REPO/releases/download/edge

bold() { printf '\033[1m%s\033[0m\n' "$*"; }
info() { printf '\033[36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33m!!\033[0m  %s\n' "$*" >&2; }
die() { printf '\033[31mxx\033[0m  %s\n' "$*" >&2; exit 1; }
run() { if [ "${TD_DRY_RUN:-}" = 1 ]; then printf '   [dry run] %s\n' "$*"; else "$@"; fi; }

# Questions go to the terminal even when this script arrives through a pipe (curl | bash).
TTY=/dev/tty
{ : <"$TTY"; } 2>/dev/null || TTY=/dev/stdin
ask() { # ask VAR "question" [default]
  local var=$1 q=$2 def=${3:-} ans
  if [ -n "${!var+x}" ]; then return; fi # given as an environment variable, even if empty
  if [ -n "$def" ]; then printf '%s [%s]: ' "$q" "$def" >&2; else printf '%s: ' "$q" >&2; fi
  IFS= read -r ans <"$TTY" || true
  printf -v "$var" '%s' "${ans:-$def}"
}
ask_secret() { # ask_secret VAR "question"
  local var=$1 q=$2 ans
  if [ -n "${!var+x}" ]; then return; fi
  printf '%s: ' "$q" >&2
  IFS= read -rs ans <"$TTY" || true
  printf '\n' >&2
  printf -v "$var" '%s' "$ans"
}
yes_no() { # yes_no "question" default(y/n)
  local a; unset a
  ask a "$1 (y/n)" "$2"
  [[ $a =~ ^[YyJj] ]]
}

# ---- checks ----
[ "$(id -u)" = 0 ] || [ "${TD_DRY_RUN:-}" = 1 ] || die "Run as root: curl -fsSL … | sudo bash"
[ "$(uname -s)" = Linux ] || die "Linux only."
command -v systemctl >/dev/null || die "systemd is required."
for c in curl sha256sum; do command -v $c >/dev/null || die "Missing $c."; done
case $(uname -m) in x86_64) ARCH=amd64 ;; aarch64 | arm64) ARCH=arm64 ;; *) die "Unsupported CPU $(uname -m)." ;; esac
HAS_APT=; command -v apt-get >/dev/null && HAS_APT=1

download_binary() {
  info "Downloading the latest Telegram Drive ($ARCH)"
  local tmp; tmp=$(mktemp -d)
  curl -fsSL -o "$tmp/telegram-drive-linux-$ARCH" "$RELEASE/telegram-drive-linux-$ARCH"
  curl -fsSL -o "$tmp/telegram-drive-linux-$ARCH.sha256" "$RELEASE/telegram-drive-linux-$ARCH.sha256"
  (cd "$tmp" && sha256sum -c --quiet "telegram-drive-linux-$ARCH.sha256") || die "Checksum mismatch; try again."
  run install -m 755 "$tmp/telegram-drive-linux-$ARCH" "$APP/telegram-drive.new"
  run mv "$APP/telegram-drive.new" "$APP/telegram-drive"
  rm -rf "$tmp"
}

wait_healthy() {
  local port=$1
  [ "${TD_DRY_RUN:-}" = 1 ] && return 0
  for _ in $(seq 30); do
    curl -fsS "http://127.0.0.1:$port/api/health" >/dev/null 2>&1 && return 0
    sleep 1
  done
  journalctl -u $SERVICE -n 30 --no-pager >&2 || true
  die "The service did not start; logs above."
}

# ---- update ----
if [ -x "$APP/telegram-drive" ] && [ -f "$APP/.env" ]; then
  bold "Telegram Drive is already installed in $APP."
  if yes_no "Update it to the latest version" y; then
    download_binary
    run systemctl restart $SERVICE
    wait_healthy "$(sed -n 's/^PORT=//p' "$APP/.env" | tail -1)"
    info "Updated."
  fi
  exit 0
fi

bold "Telegram Drive installer"
echo "Your files go to a private channel in your own Telegram account; this server keeps the folders and accounts."
echo

# ---- Telegram ----
bold "1/4  Telegram account"
echo "Create an app at https://my.telegram.org (API development tools) to get an API ID and hash."
ask TD_API_ID "API ID"
ask TD_API_HASH "API hash"
ask TD_PHONE "Phone number of the Telegram account that stores the files (+62…)"
[[ $TD_API_ID =~ ^[0-9]+$ ]] || die "API ID must be a number."
[ -n "$TD_API_HASH" ] && [ -n "$TD_PHONE" ] || die "API hash and phone are required."
echo "A private channel named \"Telegram Drive\" is created for the files, unless you give one."
ask TD_CHANNEL_ID "Existing channel id (-100…), empty to create one" ""

# ---- database ----
echo
bold "2/4  Database"
if [ -z "${TD_DATABASE_URL:-}" ]; then
  echo "  1) SQLite: one file in $APP/data, nothing else to run (fine for a family)"
  echo "  2) PostgreSQL on this server, installed for you (more users, more traffic)"
  echo "  3) A PostgreSQL I already have (paste its URL)"
  ask DB_CHOICE "Choose" "1"
  if [ "$DB_CHOICE" = 1 ]; then
    TD_DATABASE_URL="sqlite:data/drive.db"
  elif [ "$DB_CHOICE" = 2 ]; then
    [ -n "$HAS_APT" ] || die "Automatic PostgreSQL needs Debian or Ubuntu. Install PostgreSQL yourself and choose 2."
    DB_PASS=$(head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 32)
    TD_DATABASE_URL="postgres://telegram_drive:$DB_PASS@127.0.0.1:5432/telegram_drive?sslmode=disable"
    INSTALL_PG=1
  else
    ask TD_DATABASE_URL "PostgreSQL URL (postgres://user:pass@host:5432/db?sslmode=require)"
  fi
fi
[[ $TD_DATABASE_URL =~ ^(postgres(ql)?://|sqlite:) ]] || die "DATABASE_URL must start with postgres:// or sqlite:"

# ---- web address ----
echo
bold "3/4  Web address"
echo "With a domain pointing at this server, Caddy is installed and gets an HTTPS certificate."
echo "Without one, the drive answers on http://<server-ip>:<port> (no HTTPS: only for a home network)."
ask TD_DOMAIN "Domain (e.g. drive.example.com), empty for none" ""
ask TD_PORT "Port for the app" "5758"
if [ -n "$TD_DOMAIN" ]; then
  [ -n "$HAS_APT" ] || die "Automatic HTTPS needs Debian or Ubuntu. Leave the domain empty and put your own proxy in front."
  HOST=127.0.0.1
else
  HOST=0.0.0.0
fi
echo "Optional: \"Log in with Telegram\" needs a bot from @BotFather (/setdomain to your domain)."
ask TD_BOT_TOKEN "Bot token, empty to skip" ""
[ -z "$TD_BOT_TOKEN" ] || ask TD_BOT_USERNAME "Bot username (without @)"

# ---- first admin ----
echo
bold "4/4  Your account"
ask TD_ADMIN "Admin username" "admin"
while [ -z "${TD_ADMIN_PASSWORD:-}" ]; do
  ask_secret P1 "Password (at least 8 characters)"
  ask_secret P2 "Repeat password"
  if [ "$P1" != "$P2" ]; then warn "Passwords differ."; elif [ ${#P1} -lt 8 ]; then warn "Too short."; else TD_ADMIN_PASSWORD=$P1; fi
  unset P1 P2
done

# ---- install ----
echo
info "Installing"
if [ -n "${INSTALL_PG:-}" ]; then
  info "PostgreSQL"
  run apt-get update -qq
  run env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq postgresql >/dev/null
  run systemctl enable --now postgresql
  if [ "${TD_DRY_RUN:-}" != 1 ]; then
    runuser -u postgres -- psql -qtc "SELECT 1 FROM pg_roles WHERE rolname = 'telegram_drive'" | grep -q 1 &&
      runuser -u postgres -- psql -qc "ALTER ROLE telegram_drive WITH LOGIN PASSWORD '$DB_PASS'" ||
      runuser -u postgres -- psql -qc "CREATE ROLE telegram_drive WITH LOGIN PASSWORD '$DB_PASS'"
    runuser -u postgres -- psql -qtc "SELECT 1 FROM pg_database WHERE datname = 'telegram_drive'" | grep -q 1 ||
      runuser -u postgres -- createdb -O telegram_drive telegram_drive
  fi
fi

id "$USER_NAME" >/dev/null 2>&1 || run useradd --system --home-dir "$APP" --shell /usr/sbin/nologin "$USER_NAME"
run mkdir -p "$APP/data"

ORIGIN=
[ -z "$TD_DOMAIN" ] || ORIGIN="https://$TD_DOMAIN"
ENV_FILE=$(mktemp)
cat >"$ENV_FILE" <<EOF
# Written by install.sh. Restart after changes: sudo systemctl restart $SERVICE
TELEGRAM_API_ID=$TD_API_ID
TELEGRAM_API_HASH=$TD_API_HASH
TELEGRAM_PHONE=$TD_PHONE
TELEGRAM_CHANNEL_ID=$TD_CHANNEL_ID
DATABASE_URL=$TD_DATABASE_URL
HOST=$HOST
PORT=$TD_PORT
DATA_DIR=data
MAX_UPLOAD_MB=2000
ALLOWED_ORIGIN=$ORIGIN
TELEGRAM_BOT_TOKEN=$TD_BOT_TOKEN
TELEGRAM_BOT_USERNAME=${TD_BOT_USERNAME:-}
EOF
run install -m 600 "$ENV_FILE" "$APP/.env"
rm -f "$ENV_FILE"
download_binary
run chown -R "$USER_NAME:" "$APP"

info "Telegram login: Telegram sends a code to your app; type it here."
if [ "${TD_DRY_RUN:-}" = 1 ]; then echo "   [dry run] $APP/telegram-drive login"; else
  runuser -u "$USER_NAME" -- sh -c "cd '$APP' && ./telegram-drive login" <"$TTY" || die "Telegram login failed."
fi

info "Creating admin \"$TD_ADMIN\""
if [ "${TD_DRY_RUN:-}" = 1 ]; then echo "   [dry run] $APP/telegram-drive admin $TD_ADMIN"; else
  printf '%s\n' "$TD_ADMIN_PASSWORD" | runuser -u "$USER_NAME" -- sh -c "cd '$APP' && ./telegram-drive admin \"\$0\"" "$TD_ADMIN"
fi

info "systemd service"
UNIT=$(mktemp)
cat >"$UNIT" <<EOF
[Unit]
Description=Telegram Drive
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
User=$USER_NAME
WorkingDirectory=$APP
ExecStart=$APP/telegram-drive
Restart=always
RestartSec=3
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=$APP/data
PrivateTmp=true
ProtectHome=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
ProtectClock=true
ProtectHostname=true
RestrictNamespaces=true
RestrictRealtime=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
CapabilityBoundingSet=
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallFilter=~@privileged @resources
UMask=0077

[Install]
WantedBy=multi-user.target
EOF
run install -m 644 "$UNIT" /etc/systemd/system/$SERVICE.service
rm -f "$UNIT"
run systemctl daemon-reload
run systemctl enable --now $SERVICE
wait_healthy "$TD_PORT"

if [ -n "$TD_DOMAIN" ]; then
  info "HTTPS with Caddy for $TD_DOMAIN"
  HAD_CADDY=; command -v caddy >/dev/null && HAD_CADDY=1
  if [ -z "$HAD_CADDY" ]; then
    run apt-get install -y -qq debian-keyring debian-archive-keyring apt-transport-https gnupg >/dev/null
    if [ "${TD_DRY_RUN:-}" != 1 ]; then
      curl -fsSL https://dl.cloudsmith.io/public/caddy/stable/gpg.key | gpg --dearmor --yes -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
      curl -fsSL https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt >/etc/apt/sources.list.d/caddy-stable.list
    fi
    run apt-get update -qq
    run apt-get install -y -qq caddy >/dev/null
  fi
  CADDY=$(mktemp)
  cat >"$CADDY" <<EOF
$TD_DOMAIN {
	encode gzip
	reverse_proxy 127.0.0.1:$TD_PORT {
		flush_interval -1
	}
}
EOF
  if [ -n "$HAD_CADDY" ]; then # someone else's Caddyfile: do not overwrite it
    warn "Caddy was already installed. Add this to /etc/caddy/Caddyfile, then: systemctl reload caddy"
    cat "$CADDY"
  else
    run install -m 644 "$CADDY" /etc/caddy/Caddyfile
    run systemctl reload-or-restart caddy
  fi
  rm -f "$CADDY"
  URL="https://$TD_DOMAIN"
else
  IP=$(hostname -I 2>/dev/null | awk '{print $1}') # the server's own address; behind NAT use the public one
  IP=${IP:-<server-ip>}
  URL="http://$IP:$TD_PORT"
  warn "No HTTPS: passwords travel unencrypted. Fine at home; for the internet use a domain or a tunnel."
  warn "If a firewall is on, open port $TD_PORT (e.g. ufw allow $TD_PORT/tcp)."
fi

echo
bold "Done. Open $URL and log in as \"$TD_ADMIN\"."
echo "Settings: $APP/.env   Logs: journalctl -u $SERVICE -f   Update: run this installer again."
if [[ $TD_DATABASE_URL == sqlite:* ]]; then
  echo "Back up $APP/data: it holds the Telegram session and the database (drive.db). Together they are your drive."
else
  echo "Back up $APP/data (Telegram session) and the PostgreSQL database; together they are your drive."
fi
