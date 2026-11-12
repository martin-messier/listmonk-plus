# listmonk-plus

A fork of [Listmonk](https://github.com/knadh/listmonk) (base: v7.0.0-beta.2) adding email
sequences and, over time, a trigger engine.

| Path | What |
|---|---|
| `plus/docs/` | Design (`001-sequences-design.md`) and API reference (`002-sequences-api.md`) |
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

PLUS_WORKER_INTERVAL=2s plus/scripts/dev-restart.sh   # fast worker for the engine test
LISTMONK_API_USER=... LISTMONK_API_TOKEN=... python3 -I plus/scripts/test-sequences.py
```

`test-events.py` needs an API user (Admin → Users → New → type "API", role Super Admin).
Both test scripts delete everything they create when they finish (even after a failure); set
`KEEP_TEST_DATA=1` to keep it for inspection.

## Remotes

- `origin`: our private repo.
- `upstream`: https://github.com/knadh/listmonk (fetch new releases from here; see the merge
  checklist in `UPSTREAM-TOUCHPOINTS.md`).
