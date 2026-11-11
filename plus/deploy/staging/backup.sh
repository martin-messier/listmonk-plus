#!/usr/bin/env bash
# listmonk-plus staging: dump the database to /srv/listmonk-plus/backups and
# keep the last 14 days. Run nightly from /etc/cron.d/listmonk-plus-backup.
#
# Restore (into the running stack; replaces current data):
#   docker compose -f plus/deploy/staging/docker-compose.yml --env-file /srv/listmonk-plus/.env \
#     exec -T db pg_restore -U listmonk -d listmonk --clean --if-exists < /srv/listmonk-plus/backups/<file>.dump
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
DIR=/srv/listmonk-plus/backups
OUT="$DIR/listmonk-$(date -u +%Y%m%d-%H%M%S).dump"

docker compose -f "$HERE/docker-compose.yml" --env-file /srv/listmonk-plus/.env \
  exec -T db pg_dump -U listmonk -d listmonk -Fc > "$OUT.tmp"
mv "$OUT.tmp" "$OUT"
chmod 600 "$OUT"
find "$DIR" -name 'listmonk-*.dump' -mtime +14 -delete
echo "$(date -u +%FT%TZ) backup ok: $OUT ($(du -h "$OUT" | cut -f1))"
