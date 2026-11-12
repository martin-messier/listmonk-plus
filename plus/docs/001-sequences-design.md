# 001 — Sequences (design)

Status: **draft v3 — decisions D1–D5 incorporated** · Base: Listmonk v7.0.0-beta.2 (`9a4fdcbb`) · 2026-11-11

## 1. Goal

Send a series of emails to a subscriber over time ("Welcome" now, "Getting started" 10 minutes
later, "Case study" 2 days later…). People enter a sequence **automatically when they subscribe
to a list** (the trigger) or **manually** (bulk-enroll existing list members, or one person).

**In scope (v1)**
- A sequence belongs to one list (its audience). Subscribing to that list is a trigger.
- **One sequence per list** (D5), so a new subscriber never starts two sequences from one list.
- Manual enrollment: all/filtered existing members of the list, or individual subscribers.
- Per-sequence re-entry rules (re-subscribe after leaving; subscribe again while in progress).
- Ordered steps, each with a delay in **minutes, hours, or days** and an email.
- A **"send me the next email now"** link that skips the remaining wait.
- Stops automatically on unsubscribe, blocklist, or subscriber deletion.
- Admin screens to build sequences, enroll people, and see progress.

**Out of scope (v1, planned later)**
- Branching / conditions, other triggers (tags, attributes, clicks, API events).
- Open/click analytics for sequence emails (§9).
- Send windows / per-subscriber time zones.
- Cross-sequence rules ("not while in sequence X", minimum gap between sequence emails).

## 2. Fork principles (to keep upstream merges painless)

1. **New code lives in new places**: Go and SQL (embedded) in `internal/plus/…`,
   templates in `static/admin/views/plus-*.html`, docs in `plus/docs/`.
2. **New tables are prefixed `plus_`.** We never alter upstream tables.
3. **Own migration track.** Upstream records migrations by Listmonk version (`v7.0.0`…) in
   `cmd/upgrade.go`. Adding entries there would collide with upstream's future versions.
   Instead: a `plus_migrations` table + our own numbered migrations (`plus-0001`…), run right
   after upstream's `upgrade()`.
4. **Few, small touch points in upstream files** (start the worker, register routes, menu item,
   permissions, the "subscribed again" hook). Every one is listed in
   `plus/UPSTREAM-TOUCHPOINTS.md` so we know exactly what to re-check on each upstream merge.

## 3. Capturing subscription events → `plus_events`

### 3.1 Status changes: database trigger

A subscription can be created or changed in many code paths (public form, admin/API,
add-to-list single and bulk-by-query, double opt-in confirmation, CSV import). All of them write
to `subscriber_lists`, so a Postgres trigger there catches every path, including future ones:

```
subscriber_lists  ──AFTER INSERT / UPDATE OF status──▶  plus_events (append-only)
```

| Situation | Event type |
|---|---|
| Row inserted with status `unconfirmed` or `confirmed` | `subscription.created` |
| Status changed **from `unsubscribed`** to `unconfirmed`/`confirmed` | `subscription.created` (a re-subscribe) |
| Status changed to `confirmed` (or inserted as `confirmed`) | `subscription.confirmed` |
| Status changed to `unsubscribed` | `subscription.unsubscribed` |
| Row deleted (removed from list) | `subscription.removed` |

The trigger only fires when the **status actually changes** (`OLD.status IS DISTINCT FROM NEW.status`).

### 3.2 "Subscribed again while already subscribed": explicit hook

When an already-subscribed person submits the signup form again, the status doesn't change, so
the trigger above sees nothing. We can't detect it from the table either: Listmonk rewrites the
same rows when an admin simply saves a subscriber's edit form, so the two are indistinguishable
in the database. Therefore a **small hook in the public subscription handler**
(`cmd/public.go` → `processSubForm`, shared by `/subscription/form` and
`/api/public/subscription`) emits `subscription.repeated` when the person was already actively
subscribed to the list. The admin API's "create subscriber" rejects existing emails, so it
needs no hook. CSV import and admin edits deliberately do *not* emit it.

*Implemented in M1; verified by `plus/scripts/test-events.py`.*

`plus_events` is generic (type + subscriber + list + JSON payload). It is the foundation of the
future trigger engine: every future trigger is just another event type.

### 3.3 When does a subscription count as "subscribed"?

Listmonk stores new subscriptions as `unconfirmed` **even on single opt-in lists** (unless
"preconfirm" is used), so it depends on the list type:

| List opt-in | Trigger fires on |
|---|---|
| Single | `subscription.created` (unconfirmed or confirmed) |
| Double | `subscription.confirmed` only — never email people who didn't confirm |

## 4. Entering a sequence

### 4.1 Ways in

| Source | How |
|---|---|
| `trigger` | Subscribing to the sequence's list (§3.3), if the sequence's trigger is enabled |
| `manual-bulk` | Admin: "Enroll existing members" — all eligible members of the list, optionally narrowed with Listmonk's subscriber search/filter |
| `manual` | Admin: enroll one subscriber (sequence page or subscriber page) |
| `api` | `POST /api/plus/sequences/:id/enrollments` |

Each enrollment records its `source`. All sources obey the same eligibility and re-entry rules.

**Eligible** = subscriber `enabled`, and subscription to the list is not `unsubscribed`;
on double opt-in lists it must be `confirmed`. Ineligible people are skipped and reported
("142 enrolled, 3 skipped: unconfirmed").

### 4.2 Re-entry rules (per sequence)

| Setting | Options | Default |
|---|---|---|
| **Someone who already finished or left this sequence enters again** (re-subscribes, or is enrolled again) | *Ignore* · *Start again from step 1* | Ignore |
| **Someone currently in this sequence subscribes again / is enrolled again** | *Continue where they are* · *Restart from step 1* | Continue |

"Restart" closes the current enrollment (`cancelled`, reason `restarted`) and opens a new one,
so the history of every pass is kept.

## 5. Data model

```
plus_events
  id BIGSERIAL PK, type TEXT, subscriber_id INT NULL, list_id INT NULL,
  payload JSONB, created_at TIMESTAMPTZ, processed_at TIMESTAMPTZ NULL
  index on (created_at) WHERE processed_at IS NULL

plus_sequences
  id SERIAL PK, uuid UUID, name TEXT, status ('draft'|'active'|'paused'),
  list_id INT → lists ON DELETE CASCADE,
  trigger_enabled BOOL DEFAULT true,
  -- D5: at most one automated sequence per list (drafts don't count):
  -- UNIQUE (list_id) WHERE trigger_enabled AND status IN ('active','paused')
  reentry_after_exit ('ignore'|'restart') DEFAULT 'ignore',
  reentry_while_active ('continue'|'restart') DEFAULT 'continue',
  from_email TEXT NULL, created_at, updated_at

plus_sequence_steps
  id SERIAL PK, uuid UUID, sequence_id → plus_sequences ON DELETE CASCADE,
  position INT,
  delay INTERVAL          -- wait after the previous step was sent (step 1: after enrollment).
                          -- Stored as an interval; the UI offers minutes / hours / days. 0 = immediately.
  template_id INT → templates (type 'tx'),
  subject TEXT NULL,      -- overrides the template's subject if set
  skip_landing_url TEXT NULL  -- where the "next email now" link sends people (§7); NULL = built-in page
  UNIQUE (sequence_id, position)

plus_enrollments
  id BIGSERIAL PK, uuid UUID, sequence_id → plus_sequences ON DELETE CASCADE,
  subscriber_id → subscribers ON DELETE CASCADE,
  source ('trigger'|'manual'|'manual-bulk'|'api'),
  status ('active'|'completed'|'cancelled'),
  next_step INT, next_send_at TIMESTAMPTZ NULL,
  cancel_reason TEXT NULL, enrolled_at, updated_at
  UNIQUE (sequence_id, subscriber_id) WHERE status = 'active'   -- at most one active pass
  index on (next_send_at) WHERE status = 'active'

plus_sends
  id BIGSERIAL PK, uuid UUID,            -- uuid is used in the "next email now" link
  enrollment_id → plus_enrollments ON DELETE CASCADE,
  step_id → plus_sequence_steps ON DELETE SET NULL, subscriber_id,
  status ('queued'|'failed'|'skipped'), error TEXT NULL, attempts INT,
  created_at, skip_clicked_at TIMESTAMPTZ NULL
  UNIQUE (enrollment_id, step_id)        -- "never send the same step twice in one pass"
```

## 6. The worker

One background goroutine, started from `cmd/main.go` next to the campaign manager
(`go mgr.Run()`). It ticks every **15 s** (configurable), and can also be woken instantly
(e.g. by a "next email now" click). So a 10-minute delay is honoured to within ~15 s.

**a) Consume events** (batch, `FOR UPDATE SKIP LOCKED`):
- `created` / `confirmed` / `repeated` → if the list has an **active, trigger-enabled**
  sequence (at most one, D5) and its rule matches (§3.3), apply the enrollment logic (§4) with
  source `trigger`.
- `unsubscribed` / `removed` → cancel active enrollments in sequences on that list.
- Mark the event processed.

**b) Send due steps** (`status='active' AND next_send_at <= now`, batch, `SKIP LOCKED`):
1. Re-check eligibility right before sending (§4.1) and that the sequence is still `active`.
   If not → cancel (with reason) and move on.
2. Insert the `plus_sends` row first (UNIQUE key ⇒ no double sends, even after a crash).
3. Render the tx template for the subscriber (with the extra functions in §7–8) and push it via
   `manager.PushMessage` — the same path `/api/tx` uses (`cmd/tx.go`). We extract that
   render-and-push logic into a small reusable helper.
4. Advance: `next_step++`, `next_send_at = now + next.delay`, or `status='completed'`.

`SKIP LOCKED` makes running two Listmonk instances later safe without changes.
On push failure: record `failed`, retry with backoff (5 min, 30 min, 2 h), then give up on that
step and continue with the next.

**Paused sequence**: nobody moves; on resume, overdue steps go out at the next tick.
**Editing a live sequence**: changing a delay/template affects steps not yet sent. Adding steps at
the end extends the journey for `active` enrollments only; people who already `completed` stay
completed. Reordering/deleting steps only while paused or draft.

## 7. "Send me the next email now" link

A template function, usable in any step's template:

```
<a href="{{ SequenceNextURL }}">Can't wait? Get the next lesson now</a>
```

- The URL contains the **`plus_sends.uuid`** of the email it appears in (random, unguessable):
  `https://<root>/plus/seq/next/<send-uuid>`.
- Effect: if the person is still active in that pass **and the email it came from is the most
  recent one sent** (i.e. they're waiting for exactly the next step), set
  `next_send_at = now` and wake the worker → next email within seconds.
- Old links (from earlier emails) and repeated clicks do nothing harmful: you can only skip the
  **current** wait, one step at a time. Clicking in the last email does nothing.
- Records `skip_clicked_at` (future trigger-engine event: `sequence.next_clicked`).
- **Email security scanners problem**: Outlook/Gmail/corporate scanners often "visit" links
  automatically, which would skip waits nobody asked to skip. So the link opens a small page
  with a **"Send me the next email now"** button; only the button press (a POST) triggers it.
  After pressing: a confirmation page, or a redirect to the step's `skip_landing_url`.
- If the template has no `{{ SequenceNextURL }}`, nothing changes for that step.

## 8. Compliance: unsubscribe

Listmonk's `{{ UnsubscribeURL }}` only exists for **campaign** templates, not tx templates.
Sequence emails are marketing, so they must have one. We will:
- ~~Provide `{{ UnsubscribeURL }}` / `{{ ManageURL }}`~~ **Implemented (M4) as
  `{{ SequenceUnsubscribeURL }}`** with its own page (`/plus/seq/unsubscribe/:sendUUID`): Listmonk's
  unsubscribe page is campaign-bound (for non-campaign mail it unsubscribes from *all* lists), and a
  separate name can never clash with campaign templates' `{{ UnsubscribeURL }}`. It unsubscribes from
  the sequence's list only.
- Add `List-Unsubscribe` + `List-Unsubscribe-Post: List-Unsubscribe=One-Click` headers
  (required by Gmail/Yahoo bulk-sender rules).
- Warn in the admin if a step's template lacks `{{ UnsubscribeURL }}`.

Unsubscribing updates `subscriber_lists` → trigger → `subscription.unsubscribed` → enrollment
cancelled. One mechanism, no special cases.

## 9. Tracking (v1: sends only)

Listmonk's opens/clicks tables are keyed to campaigns, and tx emails aren't tracked. v1 records
**what was sent to whom and when** (`plus_sends`) plus "next now" clicks. Opens/clicks for
sequences are a v2 topic and will also become trigger-engine events.

## 10. Admin UI and API

Server-rendered pages in v7's admin style (`static/admin/views/*.html`):
- **Sequences** (new menu item): name, list, status, # active / completed.
- **Sequence editor**: name, list, status, trigger on/off, re-entry rules (§4.2), steps
  (delay = number + minutes/hours/days, template, subject override, landing URL).
- **Enroll** dialog: "all eligible members" or a subscriber search; shows a preview count first,
  including how many of those people are currently active in another sequence (with an option
  to exclude them).
- **Sequence activity**: enrollments by status/source, per-subscriber progress, send log;
  cancel or restart an individual enrollment.
- Subscriber page panel: their sequences, with enroll/cancel actions.

API: `/api/plus/sequences` CRUD, `/api/plus/sequences/:id/steps`,
`/api/plus/sequences/:id/enrollments` (list, create one/bulk, cancel).
Permissions (added to `permissions.json`): `sequences:get`, `sequences:manage`.

## 11. Decisions (agreed 2026-11-11)

| # | Decision |
|---|---|
| D1 | ~~Steps use Listmonk transactional (tx) templates.~~ **Superseded (M5):** each step is a campaign edited on the native campaign screen — see `003-sequence-messages-design.md`. |
| D2 | Subscription is one trigger; existing members can be enrolled manually (bulk or individually). |
| D3 | Per-sequence re-entry rules: after leaving (ignore/restart) and while active (continue/restart). |
| D4 | Send as soon as due. Delays in minutes/hours/days. "Next email now" link to skip the wait. |
| D5 | **One sequence per list**, whatever its status or trigger (clarified 2026-11-11; migration 0005 enforces it with a unique index). A manual-only sequence needs its own list. The enroll dialog flags people already active in another sequence (on another list). |

## 11a. Open questions

| # | Question | Notes |
|---|---|---|
| Q1 | A subscriber on **several lists** can be in several sequences at once (one per list). How do we prevent email overload across lists? | Ideas: sequence priorities, "don't start while in sequence X", a minimum gap between any two sequence emails to the same person, or a daily cap. Not needed for v1; to decide before the trigger engine. |

## 12. Milestones

1. ✅ **Foundation** (done 2026-11-11): `plus_migrations` runner, tables, trigger, `subscription.repeated` hook.
   Verify every subscribe path produces the right events in the dev environment.
2. ✅ **Engine** (done 2026-11-11, API reference: `002-sequences-api.md`): worker (trigger + manual enrollment, sending, re-entry rules, "next now" link),
   API only; tested with minute-scale delays + Mailhog.
3. ✅ **Admin UI** (done 2026-11-11): Sequences menu, list, editor (steps / settings / activity tabs), enroll
   dialog with preview. Deferred: subscriber-page panel, narrowing bulk enrollment with a subscriber
   search, translations (pages are English-only).
4. **Hardening**: ✅ unsubscribe link + one-click headers, editor warning + activation check,
   retries (M2), tests (44 engine + 15 event checks), docs (done 2026-11-11). ⏳ Deploy to production on the VPS (lists.martinmessier.ca), email caught by Mailpit until a sending service is set up.
5. ✅ **Steps are campaigns** (M5, `003-sequence-messages-design.md`): native campaign screen per step,
   opens/clicks per step, Sequences → Analytics, safeguards against broadcasting a step.

## 13. Toward the trigger engine (later)

- More event sources into `plus_events`: tag/attribute changes, link clicks, campaign opens,
  "next now" clicks, `POST /api/plus/events` for external events (e.g. WooCommerce "purchased").
- Sequences get a **trigger definition** (event type + conditions) instead of "list X".
- Steps gain types: send email, wait, wait-until, condition/branch, add/remove list or tag, webhook.
- Cross-sequence rules: exclusions, priorities, minimum gap between sequence emails.
