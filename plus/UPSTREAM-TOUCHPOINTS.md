# Upstream touch points

Every place where listmonk-plus modifies an **upstream** Listmonk file. Everything else we add
lives in our own files (`internal/plus/`, `plus/`). On each upstream merge, re-check each entry:
does the surrounding upstream code still look the same, and does the hook still make sense?

All hooks are marked in code with a `listmonk-plus` comment: `git grep -n "listmonk-plus" -- cmd internal ':!internal/plus'`

| # | File | What | Why | Since |
|---|---|---|---|---|
| 1 | `cmd/main.go` (imports) | Import `internal/plus` | — | M1 |
| 2 | `cmd/main.go` `init()`, after the upgrade / `checkUpgrade` block | `plus.Migrate(db, lo)` | Apply our migrations (own `plus_migrations` track) and re-install triggers on every start | M1 |
| 3 | `cmd/main.go` `App` struct + `app := &App{…}` | `plus *plus.Plus` field, `plus.New(plus.Opt{…})` (db, core, manager, root URL, from email, `plus.worker_interval`) | Make plus available to handlers | M1, M2 |
| 4 | `cmd/public.go` `processSubForm()`, existing-subscriber branch | `a.plus.ActiveListIDs(...)` before `UpdateSubscriberWithLists`, `a.plus.RecordRepeated(...)` after success | Emit `subscription.repeated` (status doesn't change, so the DB trigger can't see it). Covers `/subscription/form` and `/api/public/subscription` | M1 |
| 5 | `cmd/main.go` after `app` is created | `go app.plus.Run()` | Start the sequence worker | M2 |
| 6 | `cmd/main.go` reload closer, **before `mgr.Close()`** | `app.plus.Close()` | The worker pushes into the manager's queue; pushing after it's closed would panic. Order matters. | M2 |
| 7 | `cmd/handlers.go`, authenticated group, before "API endpoints" | `a.plus.RegisterAPI(g, pm)` | `/api/plus/*` admin API | M2 |
| 8 | `cmd/handlers.go`, public group, after `/robots.txt` | `a.plus.RegisterPublic(g)` | `/plus/seq/next/:uuid` page | M2 |
| 9 | `internal/manager/manager.go` `makeGnericFuncMap()` (+ import) | `maps.Copy(funcs, tplfuncs.Placeholders())` | Lets tx templates using `{{ SequenceNextURL }}` be saved/previewed; real values are injected at send time | M2 |
| 10 | `permissions.json` | `sequences` group: `sequences:get`, `sequences:manage` | Role-based access to the API | M2 |

## Database objects on upstream tables

| Object | On | Defined in | Notes |
|---|---|---|---|
| Triggers `plus_sub_lists_insert/update/delete` | `subscriber_lists` | `internal/plus/sql/triggers.sql` | Re-applied on every start. If upstream renames/restructures `subscriber_lists` or its `status` values, update the trigger functions. |
| Foreign keys from `plus_*` tables | `lists`, `subscribers`, `templates` | `internal/plus/sql/migrations/` | Upstream `--install` (non-idempotent) drops these tables with CASCADE, which also drops our FKs. Only relevant for full reinstalls. |

## Merge checklist

1. `git fetch upstream --tags` and merge the new release tag into a branch.
2. Resolve conflicts; re-check every row above.
3. Build, run `go test ./...`, start with `plus/scripts/dev-restart.sh`.
4. Run `plus/scripts/test-events.py` and, with `PLUS_WORKER_INTERVAL=2s plus/scripts/dev-restart.sh`,
   `plus/scripts/test-sequences.py` (all must pass).
5. Never push upstream `v*` tags to our repo (they'd trigger upstream's release workflow if Actions were enabled).
