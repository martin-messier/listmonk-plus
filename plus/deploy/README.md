# Deploying listmonk-plus

## Production (`plus/deploy/production/`)

The stack running at https://lists.martinmessier.ca. A self-contained Docker Compose stack: **Listmonk (this fork, built from source)**, **PostgreSQL 18**,
and **Caddy** (HTTPS via Let's Encrypt). Email is sent through **Amazon SES** (us-east-1),
configured in Listmonk → Settings → SMTP. Only Caddy publishes ports (80/443): Docker bypasses ufw for published ports.

| Path on the server | What |
|---|---|
| `/opt/listmonk-plus` | Git checkout (branch `main`; production only runs merged work) |
| `/srv/listmonk-plus/.env` | Secrets (DB and initial admin password), mode 600. **Not in git.** |
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

### Email sending (Amazon SES)

- Sending domain: `lists.martinmessier.ca` (SES identity in **us-east-1**, Easy DKIM, custom MAIL FROM
  `bounce.lists.martinmessier.ca`). DNS at WPX: 3 DKIM CNAMEs, MX + SPF on `bounce.lists`,
  DMARC on `_dmarc.lists` (`p=none` to start).
- Listmonk → Settings → SMTP: `email-smtp.us-east-1.amazonaws.com:587`, STARTTLS, LOGIN with the SES
  SMTP credentials (IAM user created under "IAM SMTP credentials"; kept in the password manager).
- Message rate (Settings → Performance) must stay at or below the SES maximum send rate.
- `setup.sh` never changes SMTP settings, so re-running it is safe.

### Off-server backups

The nightly dumps stay on the VPS. Also enable Hostinger's automatic VPS backups (hPanel → VPS →
Backups) so a copy exists outside the machine.
