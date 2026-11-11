#!/usr/bin/env bash
# listmonk-plus staging: ONE-TIME setup on the server.
#
#   sudo mkdir -p /srv/listmonk-plus && sudo chown "$USER" /srv/listmonk-plus
#   git clone -b sequences https://github.com/martin-messier/listmonk-plus.git /opt/listmonk-plus
#   /opt/listmonk-plus/plus/deploy/staging/setup.sh lists.example.com
#
# Generates /srv/listmonk-plus/.env with random passwords (never printed),
# builds and starts the stack, then points Listmonk's root URL and SMTP at the
# domain and at Mailpit. Safe to re-run: existing secrets/settings are kept.
set -euo pipefail

DOMAIN="${1:?usage: setup.sh <domain>}"
HERE="$(cd "$(dirname "$0")" && pwd)"
STATE=/srv/listmonk-plus
ENV="$STATE/.env"
ADMIN_USER="${ADMIN_USER:-$(whoami)}"

mkdir -p "$STATE/backups"
chmod 700 "$STATE"

rand() { LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c "$1"; }

if [ ! -f "$ENV" ]; then
  umask 077
  MAILPIT_PASSWORD="$(rand 24)"
  MAILPIT_HASH="$(docker run --rm caddy:2.11 caddy hash-password --plaintext "$MAILPIT_PASSWORD")"
  cat > "$ENV" <<EOF
# listmonk-plus staging secrets. Keep a copy in your password manager.
DOMAIN=$DOMAIN
POSTGRES_PASSWORD=$(rand 32)

# Listmonk Super Admin (created on the first start only).
LISTMONK_ADMIN_USER=$ADMIN_USER
LISTMONK_ADMIN_PASSWORD=$(rand 24)

# Mailpit web UI at https://$DOMAIN/mailpit/ (HTTP basic auth).
MAILPIT_USER=$ADMIN_USER
MAILPIT_PASSWORD=$MAILPIT_PASSWORD
MAILPIT_HASH='$MAILPIT_HASH'
EOF
  echo "Created $ENV (passwords not shown; read them with: cat $ENV)"
else
  echo "Keeping existing $ENV"
fi

"$HERE/deploy.sh"

dc() { docker compose -f "$HERE/docker-compose.yml" --env-file "$ENV" "$@"; }

echo "Configuring root URL, From address and SMTP (Mailpit)..."
dc exec -T db psql -U listmonk -d listmonk -v ON_ERROR_STOP=1 -q <<SQL
UPDATE settings SET value = to_jsonb('https://$DOMAIN'::TEXT) WHERE key = 'app.root_url';
UPDATE settings SET value = '["listmonk-plus staging <noreply@$DOMAIN>"]'::JSONB
  WHERE key = 'app.from_email' AND value::TEXT LIKE '%yoursite.com%';
UPDATE settings SET value = '[{
  "uuid": "6a4d2f4e-6b3e-4c55-9a3e-6d1f0b6f9a01", "name": "mailpit", "enabled": true,
  "host": "mailpit", "port": 1025, "hello_hostname": "", "auth_protocol": "none",
  "username": "", "password": "", "tls_type": "none", "tls_skip_verify": false,
  "max_conns": 10, "idle_timeout": "15s", "wait_timeout": "5s",
  "max_msg_retries": 2, "msg_retry_delay": "10ms",
  "email_headers": [], "from_addresses": [], "exclude_from_default": false
}]'::JSONB WHERE key = 'smtp';
SQL
dc restart app >/dev/null
"$HERE/deploy.sh" --wait-only

# Nightly database backup at 03:15 UTC (before the 04:00 auto-reboot window).
CRON=/etc/cron.d/listmonk-plus-backup
if [ ! -f "$CRON" ]; then
  echo "15 3 * * * $(whoami) $HERE/backup.sh >> $STATE/backups/backup.log 2>&1" | sudo tee "$CRON" >/dev/null
  sudo chmod 644 "$CRON"
  echo "Installed nightly backup: $CRON"
fi

echo
echo "Done. Admin:   https://$DOMAIN/admin  (user: $ADMIN_USER, password in $ENV)"
echo "      Mailpit: https://$DOMAIN/mailpit/"
