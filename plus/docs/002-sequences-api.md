# 002 — Sequences API (milestone 2)

All admin endpoints need an authenticated user (session or `Authorization: token user:token`) with
`sequences:get` (read) or `sequences:manage` (write). Super Admins have both. Responses are wrapped
as `{"data": ...}`; errors as `{"message": "..."}` with 400 / 404 / 409.

## Sequences

| Method | Path | Body / notes |
|---|---|---|
| GET | `/api/plus/sequences` | All sequences, with `stats` (enrollment counts by status) |
| POST | `/api/plus/sequences` | `{name, list_id, trigger_enabled?, reentry_after_exit?, reentry_while_active?, from_email?}` → created as `draft` |
| GET | `/api/plus/sequences/:id` | Sequence with `steps` |
| PUT | `/api/plus/sequences/:id` | Any of the fields above + `status`. Rules below. |
| DELETE | `/api/plus/sequences/:id` | Not while `active` (pause first). Deletes its history too. |

Fields: `trigger_enabled` (default `true`), `reentry_after_exit`: `ignore` \| `restart` (default `ignore`),
`reentry_while_active`: `continue` \| `restart` (default `continue`), `from_email` (empty = Listmonk default).

Status rules: `draft → active` (needs ≥1 step), `active ↔ paused`; never back to `draft`. The list can
only change while `draft`. **A list can have only one sequence** (any status): creating or moving a
sequence onto a list that already has one returns **409**, naming the existing sequence.

Step campaign names follow the form "<sequence> · Step N" and are kept up to date when the sequence
is renamed or its steps are added, moved or deleted. A name changed by hand on the campaign screen is
left alone.

## Steps

Each step owns a **campaign** (its message), created with the step and deleted with it. Edit the
message with Listmonk's campaign API/screen (`/api/campaigns/:campaign_id`); the step API only
handles timing and order.

| Method | Path | Body / notes |
|---|---|---|
| POST | `/api/plus/sequences/:id/steps` | `{delay, subject?, name?, skip_landing_url?, position?}`. Creates a draft campaign on the sequence's list with Listmonk's default template (`subject` defaults to the sequence name, `name` to "<sequence> · Step N"). While `active`: append only. |
| PUT | `/api/plus/sequences/:id/steps/:stepID` | `delay`, `skip_landing_url`, `position` (reorder only while not `active`). |
| DELETE | `/api/plus/sequences/:id/steps/:stepID` | Only while not `active`; deletes the step's campaign (and its stats); later steps move up. |

- `delay`: wait after the previous step (step 1: after enrollment). Postgres interval text:
  `"0"`, `"10 minutes"`, `"2 hours"`, `"3 days"`, `"1 day 12 hours"`. Returned as `delay` and `delay_seconds`.
- Step responses include `campaign_id`, `campaign_uuid`, `campaign_name`, `campaign_subject`, `content_type`, `sent`.
- `skip_landing_url`: where people land after pressing "send me the next email now" in **this
  step's** email. Empty = a built-in confirmation page.

### Step campaigns in Listmonk's campaign API

`PUT /api/campaigns/:id` works (content, subject, from, template, headers, attachments, archive…),
but the lists are always forced to the sequence's list and `send_at` is ignored.
`PUT /api/campaigns/:id/status` and `DELETE /api/campaigns/:id` return **400** for step campaigns.
Step campaigns are excluded from `GET /api/campaigns` and from Campaigns → Analytics.

## Enrollments

| Method | Path | Body / notes |
|---|---|---|
| GET | `/api/plus/sequences/:id/enrollments?status=&page=&per_page=` | Newest first; `per_page` ≤ 500 |
| POST | `/api/plus/sequences/:id/enrollments` | `{subscriber_ids: [..]}` and/or `{subscriber_emails: [..]}`, **or** `{all: true}` (every member of the list). Optional: `source` (`manual` \| `manual-bulk` \| `api`), `exclude_other_sequences` (skip people active in another sequence), `dry_run` (preview: nothing is saved, response has `dry_run: true`) |
| DELETE | `/api/plus/sequences/:id/enrollments/:enrID` | Cancel an active enrollment |
| GET | `/api/plus/sequences/:id/candidates?query=&exclude_other_sequences=&page=&per_page=` | Subscribers that enrolling would actually enroll ("Specific subscribers" picker): eligible on the list, not in this sequence now, never completed it, and — unless re-entry after exit is `restart` — never in it before. `exclude_other_sequences=true` also hides people active in another sequence. Needs `sequences:manage` and a subscriber read permission. |

Enroll response: `{enrolled, restarted, skipped: {reason: count}}`. Skip reasons: `not_found`,
`not_on_list`, `unsubscribed`, `unconfirmed` (double opt-in lists), `subscriber_disabled`,
`subscriber_blocklisted`, `already_active`, `already_completed_or_left`, `in_other_sequence`. Needs the sequence to be
`active` or `paused`.

## Template tags (in a step's campaign)

All of Listmonk's campaign tags work as in any campaign: `{{ UnsubscribeURL }}` (unsubscribes from the
sequence's list — the campaign's list), `{{ TrackView }}`, `@TrackLink`, `{{ MessageURL }}`, … Plus:

```html
<a href="{{ SequenceNextURL }}">Can't wait? Get the next email now</a>   <!-- optional -->
```

It renders `/plus/seq/next/<campaign uuid>/<subscriber uuid>` in every campaign; outside a sequence the
page simply says the link doesn't apply.

## Public pages

GET never changes anything (email scanners pre-fetch links); it shows a confirmation button.

| Path | GET | POST |
|---|---|---|
| `/plus/seq/next/:campaignUUID/:subscriberUUID` | Confirmation button | Skips the current wait |

## Analytics page

`/admin/plus/sequences/analytics?id=&from=YYYY-MM-DD&to=YYYY-MM-DD` (default: last 30 days):
per sequence (entered, completed, stopped, in progress) and per step (sent, opens, clicks, bounces,
unsubscribes). Opens/clicks are Listmonk's campaign tracking of each step's campaign.

## Worker

Runs inside Listmonk. Interval: `plus.worker_interval` (default `15s`; env
`LISTMONK_plus__worker_interval`). Also woken immediately by manual enrollment, "next now" presses,
resuming a sequence, and repeated subscriptions.

Failed sends (render or queue errors) are retried after 5 min and 30 min; after 3 attempts the step
is skipped (status `failed`) and the sequence continues.
