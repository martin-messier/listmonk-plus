#!/usr/bin/env bash
# listmonk-plus production: ONE-TIME setup on the server.
#
#   sudo mkdir -p /srv/listmonk-plus && sudo chown "$USER" /srv/listmonk-plus
#   git clone -b main https://github.com/martin-messier/listmonk-plus.git /opt/listmonk-plus
#   /opt/listmonk-plus/plus/deploy/production/setup.sh lists.example.com
#
# Generates /srv/listmonk-plus/.env with random passwords (never printed),
# builds and starts the stack, and points Listmonk's root URL at the domain.
# Safe to re-run: existing secrets and settings (incl. SMTP) are kept.
set -euo pipefail

DOMAIN="${1:?usage: setup.sh <domain>}"
HERE="$(cd "$(dirname "$0")" && pwd)"
STATE=/srv/listmonk-plus
ENV="$STATE/.env"
ADMIN_USER="${ADMIN_USER:-$(whoami)}"

mkdir -p "$STATE/backups"
chmod 700 "$STATE"

# Random alphanumeric string. Every command in the pipe reads its whole input,
# so nothing gets SIGPIPE: with `set -o pipefail`, `tr </dev/urandom | head`
# made the script exit silently at the first password.
rand() { openssl rand -base64 96 | tr -dc 'A-Za-z0-9' | cut -c1-"$1"; }

if [ ! -f "$ENV" ]; then
  umask 077
  cat > "$ENV" <<EOF
# listmonk-plus production secrets. Keep a copy in your password manager.
DOMAIN=$DOMAIN
POSTGRES_PASSWORD=$(rand 32)

# Listmonk Super Admin (created on the first start only).
LISTMONK_ADMIN_USER=$ADMIN_USER
LISTMONK_ADMIN_PASSWORD=$(rand 24)
EOF
  echo "Created $ENV (passwords not shown; read them with: cat $ENV)"
else
  echo "Keeping existing $ENV"
fi

"$HERE/deploy.sh"

dc() { docker compose -f "$HERE/docker-compose.yml" --env-file "$ENV" "$@"; }

echo "Configuring root URL and From address (only if still Listmonk's defaults)..."
dc exec -T db psql -U listmonk -d listmonk -v ON_ERROR_STOP=1 -q <<SQL
UPDATE settings SET value = to_jsonb('https://$DOMAIN'::TEXT) WHERE key = 'app.root_url';
UPDATE settings SET value = '["Listmonk Plus <noreply@$DOMAIN>"]'::JSONB
  WHERE key = 'app.from_email' AND value::TEXT LIKE '%yoursite.com%';
SQL
# Email sending (SMTP) is configured in Listmonk → Settings → SMTP (Amazon SES),
# never by this script: re-running it must not touch a working configuration.
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
echo "Next: configure email sending in Settings → SMTP (see plus/deploy/README.md)."
