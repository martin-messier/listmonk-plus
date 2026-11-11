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
| 7 | `cmd/handlers.go`, authenticated group, before "API endpoints" | `a.plus.RegisterAPI(g, pm)` + `a.registerPlusViews(g, pm)` | `/api/plus/*` admin API and `/admin/plus/sequences*` pages | M2, M3 |
| 8 | `cmd/handlers.go`, public group, after `/robots.txt` | `a.plus.RegisterPublic(g)` | `/plus/seq/next/:uuid` page | M2 |
| 9 | `internal/manager/manager.go` `TemplateFuncs()` | `"SequenceNextURL": func(msg *CampaignMessage) string` (campaign template function) | "Send me the next email now" link in sequence messages (campaigns). Replaced M2's generic placeholder, which would have overridden it (generic funcs are merged *after* campaign funcs) | M2, M5 |
| 10 | `permissions.json` | `sequences` group: `sequences:get`, `sequences:manage` | Role-based access to the API | M2 |
| 11 | `static/admin/partials/sidebar.html`, before the Users group | "Sequences" menu: All, Analytics, Create new (`sequences.*` page IDs) | Navigation | M3 |
| 12 | `models/common.go` tag-rewrite regex (TrackView, UnsubscribeURL, …) | `SequenceNextURL` added | `{{ SequenceNextURL }}` → `{{ SequenceNextURL . }}` like other per-message campaign tags | M5 |
| 13 | `queries/campaigns.sql` `query-campaigns`, `delete-campaigns`, `get-campaign-analytics-items` | `AND NOT EXISTS (… plus_sequence_steps …)` | Step campaigns hidden from the Campaigns list, a list's campaigns tab, dashboard recent campaigns, the `/api/campaigns` API (incl. the Campaigns → Analytics picker) and Campaigns → Analytics; bulk delete-by-query skips them | M5 |
| 14 | `queries/misc.sql` `get-dashboard-counts` | Subtract step campaigns (count, draft count, sent messages) from `mat_dashboard_counts` | Dashboard campaign counts show broadcasts only. Done in the query, not the matview, because upstream migrations recreate the matview | M5 |
| 15 | `cmd/campaigns.go` `UpdateCampaignStatus`, `DeleteCampaign` | `if st, ok := a.plus.StepOf(id); ok { return st.StepGuardError(...) }` | A step campaign can never be started/scheduled/paused/cancelled or deleted directly | M5 |
| 16 | `cmd/campaigns.go` `UpdateCampaign` | Force `ListIDs` to the sequence's list, clear `SendAt` | A step campaign always targets the sequence's list and is never scheduled | M5 |
| 17 | `cmd/campaigns.go` `campaignView` struct + `ViewCampaign` (+ import) | `PlusStep *plus.StepRef`, set from `a.plus.StepOf`; page ID `sequences.all` | Lets the campaign screen recognise a step | M5 |
| 18 | `static/admin/views/campaign.html` | Breadcrumb to the sequence, "Sequence step N of M" badge, banner, `$canSend = false` for steps (hides Start/Schedule) | Native campaign screen for step messages | M5 |
| 19 | `static/admin/partials/campaign-form.html` | Lists selector disabled + hint for steps; Schedule card hidden for steps | Same | M5 |

## Our files inside upstream folders

These are new files (no merge conflicts possible), but they rely on upstream conventions that a
release could change: admin templates are auto-loaded from `static/admin/views/*.html`, JS entry
points from `static/admin/assets/js/views/*.js`, and `cmd/plus_views.go` uses `adminView`,
`newAdminView`, `adminPage` and `uriAdmin` from package main.

- `cmd/plus_views.go`
- `static/admin/views/plus-sequences.html`, `static/admin/views/plus-sequence.html`, `static/admin/views/plus-analytics.html`
- `static/admin/assets/js/views/plus-sequences.js` (imports `api`, `urls` from `main.js` and `utils.js`)

## Database objects on upstream tables

| Object | On | Defined in | Notes |
|---|---|---|---|
| Triggers `plus_sub_lists_insert/update/delete` | `subscriber_lists` | `internal/plus/sql/triggers.sql` | Re-applied on every start. If upstream renames/restructures `subscriber_lists` or its `status` values, update the trigger functions. |
| Super Admin role permissions | `roles` (id 1) | migration `0003_super_admin_perms.sql` | Adds `sequences:get/manage`. Templates check stored permissions, so new permissions must be granted explicitly. |
| `plus_sequence_steps.campaign_id` FK (`ON DELETE RESTRICT`) | `campaigns` | migration `0004` | A step's campaign can't be deleted while the step exists. |
| Foreign keys from `plus_*` tables | `lists`, `subscribers`, `campaigns` | `internal/plus/sql/migrations/` | Upstream `--install` (non-idempotent) drops these tables with CASCADE, which also drops our FKs. Only relevant for full reinstalls. |

## Merge checklist

1. `git fetch upstream --tags` and merge the new release tag into a branch.
2. Resolve conflicts; re-check every row above.
3. Build, run `go test ./...`, start with `plus/scripts/dev-restart.sh`.
4. Check in the browser: Sequences pages (list, editor tabs, enroll dialog, analytics) and a step's campaign screen
   (banner, no Start/Schedule, locked lists); the Campaigns list must not show step campaigns.
5. Run `plus/scripts/test-events.py` and, with `PLUS_WORKER_INTERVAL=2s plus/scripts/dev-restart.sh`,
   `plus/scripts/test-sequences.py` (all must pass).
6. Never push upstream `v*` tags to our repo (they'd trigger upstream's release workflow if Actions were enabled).
