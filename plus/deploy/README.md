# Deploying listmonk-plus

## Production (`plus/deploy/production/`)

The stack running at https://lists.martinmessier.ca. A self-contained Docker Compose stack: **Listmonk (this fork, built from source)**, **PostgreSQL 18**,
**Mailpit** (catches every outgoing email until a sending service such as SES/Postmark/Brevo is
configured, so nothing reaches real inboxes yet) and **Caddy** (HTTPS via
Let's Encrypt). Only Caddy publishes ports (80/443): Docker bypasses ufw for published ports.

| Path on the server | What |
|---|---|
| `/opt/listmonk-plus` | Git checkout (branch `main`; production only runs merged work) |
| `/srv/listmonk-plus/.env` | Secrets (DB, admin, Mailpit passwords), mode 600. **Not in git.** |
| `/srv/listmonk-plus/backups/` | Nightly `pg_dump` (03:15 UTC, 14 days kept) + `backup.log` |
| `/etc/cron.d/listmonk-plus-backup` | The backup schedule |

### First-time setup

```bash
sudo mkdir -p /opt/listmonk-plus /srv/listmonk-plus && sudo chown "$USER" /opt/listmonk-plus /srv/listmonk-plus
git clone -b main https://github.com/martin-messier/listmonk-plus.git /opt/listmonk-plus
/opt/listmonk-plus/plus/deploy/production/setup.sh lists.example.com   # DNS A record must point here first
```

### Update to the latest merged code (`main`)

```bash
/opt/listmonk-plus/plus/deploy/production/deploy.sh
```

### Everyday commands

```bash
cd /opt/listmonk-plus/plus/deploy/production
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

### Going live with real email

1. Choose a sending service and verify `martinmessier.ca` there (SPF, DKIM, DMARC DNS records).
2. In Listmonk → Settings → SMTP, add the provider and disable the "mailpit" server.
3. Remove the `mailpit` service and the `/mailpit*` block from the Caddyfile, then `deploy.sh`.

### Off-server backups

The nightly dumps stay on the VPS. Also enable Hostinger's automatic VPS backups (hPanel → VPS →
Backups) so a copy exists outside the machine.
