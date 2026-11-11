# listmonk-plus

A fork of [Listmonk](https://github.com/knadh/listmonk) (base: v7.0.0-beta.2) adding email
sequences and, over time, a trigger engine.

| Path | What |
|---|---|
| `plus/docs/` | Design documents (start with `001-sequences-design.md`) |
| `plus/UPSTREAM-TOUCHPOINTS.md` | Every change we make to upstream files + merge checklist |
| `plus/scripts/` | Dev helpers and end-to-end tests |
| `internal/plus/` | Go code and SQL (migrations, triggers) |

## Local development

Requires Docker (OrbStack on macOS).

```bash
make build-dev-docker                                  # once, or when dev/app.Dockerfile changes
docker compose -f dev/docker-compose.yml up -d --wait  # start Postgres, Mailhog, Adminer, dev container
plus/scripts/dev-restart.sh                            # (re)start Listmonk after code changes
```

| URL | |
|---|---|
| http://localhost:9000/admin | Listmonk admin |
| http://localhost:8025 | Mailhog (catches all outgoing email) |
| http://localhost:8070 | Adminer (DB browser; server `db`, user/pass/db `listmonk-dev`) |

Logs: `docker compose -f dev/docker-compose.yml exec dev tail -f /tmp/listmonk-run.log`

## Tests

```bash
docker compose -f dev/docker-compose.yml exec dev go test ./...
LISTMONK_API_USER=... LISTMONK_API_TOKEN=... python3 -I plus/scripts/test-events.py
```

`test-events.py` needs an API user (Admin → Users → New → type "API", role Super Admin).

## Remotes

- `origin`: our private repo.
- `upstream`: https://github.com/knadh/listmonk (fetch new releases from here; see the merge
  checklist in `UPSTREAM-TOUCHPOINTS.md`).
