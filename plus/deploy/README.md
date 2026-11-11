# Deploying listmonk-plus

## Staging (`plus/deploy/staging/`)

A self-contained Docker Compose stack: **Listmonk (this fork, built from source)**, **PostgreSQL 18**,
**Mailpit** (catches every outgoing email, so nothing reaches real inboxes) and **Caddy** (HTTPS via
Let's Encrypt). Only Caddy publishes ports (80/443): Docker bypasses ufw for published ports.

| Path on the server | What |
|---|---|
| `/opt/listmonk-plus` | Git checkout (branch `sequences`) |
| `/srv/listmonk-plus/.env` | Secrets (DB, admin, Mailpit passwords), mode 600. **Not in git.** |
| `/srv/listmonk-plus/backups/` | Nightly `pg_dump` (03:15 UTC, 14 days kept) + `backup.log` |
| `/etc/cron.d/listmonk-plus-backup` | The backup schedule |

### First-time setup

```bash
sudo mkdir -p /opt/listmonk-plus /srv/listmonk-plus && sudo chown "$USER" /opt/listmonk-plus /srv/listmonk-plus
git clone -b sequences https://github.com/martin-messier/listmonk-plus.git /opt/listmonk-plus
/opt/listmonk-plus/plus/deploy/staging/setup.sh lists.example.com   # DNS A record must point here first
```

### Update to the latest code

```bash
/opt/listmonk-plus/plus/deploy/staging/deploy.sh
```

### Everyday commands

```bash
cd /opt/listmonk-plus/plus/deploy/staging
alias dc='docker compose --env-file /srv/listmonk-plus/.env'
dc ps                      # status
dc logs -f app             # Listmonk logs
cat /srv/listmonk-plus/.env   # passwords
./backup.sh                # backup now
```

Restore a backup: see the header of `backup.sh`.

### Building the image elsewhere

```bash
docker build -f plus/deploy/Dockerfile --build-arg COMMIT=$(git rev-parse --short HEAD) -t listmonk-plus .
```
