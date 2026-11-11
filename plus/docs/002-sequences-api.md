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
only change while `draft`. A list can have only one `active`/`paused` sequence with `trigger_enabled`
(**409** otherwise, naming the existing one).

## Steps

| Method | Path | Body / notes |
|---|---|---|
| POST | `/api/plus/sequences/:id/steps` | `{template_id, delay, subject?, skip_landing_url?, position?}`. While `active`: append only. |
| PUT | `/api/plus/sequences/:id/steps/:stepID` | Any field; `position` (reorder) only while not `active`. |
| DELETE | `/api/plus/sequences/:id/steps/:stepID` | Only while not `active`; later steps move up. |

- `template_id`: a **transactional (tx)** template.
- `delay`: wait after the previous step (step 1: after enrollment). Postgres interval text:
  `"0"`, `"10 minutes"`, `"2 hours"`, `"3 days"`, `"1 day 12 hours"`. Returned as `delay` and `delay_seconds`.
- `subject`: overrides the template's subject (may use template expressions).
- `skip_landing_url`: where people land after pressing "send me the next email now" in **this
  step's** email. Empty = a built-in confirmation page.

## Enrollments

| Method | Path | Body / notes |
|---|---|---|
| GET | `/api/plus/sequences/:id/enrollments?status=&page=&per_page=` | Newest first; `per_page` ≤ 500 |
| POST | `/api/plus/sequences/:id/enrollments` | `{subscriber_ids: [..]}` and/or `{subscriber_emails: [..]}`, **or** `{all: true}` (every member of the list). Optional: `source` (`manual` \| `manual-bulk` \| `api`), `exclude_other_sequences` (skip people active in another sequence), `dry_run` (preview: nothing is saved, response has `dry_run: true`) |
| DELETE | `/api/plus/sequences/:id/enrollments/:enrID` | Cancel an active enrollment |

Enroll response: `{enrolled, restarted, skipped: {reason: count}}`. Skip reasons: `not_found`,
`not_on_list`, `unsubscribed`, `unconfirmed` (double opt-in lists), `subscriber_disabled`,
`subscriber_blocklisted`, `already_active`, `already_completed_or_left`, `in_other_sequence`. Needs the sequence to be
`active` or `paused`.

## Template function

In any tx template used by a step:

```html
<a href="{{ SequenceNextURL }}">Can't wait? Get the next email now</a>
```

Outside a sequence (normal tx sends, previews) it renders as an empty string.

## Public pages

`GET /plus/seq/next/:sendUUID` shows a confirmation button (GET never changes anything, because
email scanners pre-fetch links). `POST` to the same URL skips the current wait.

## Worker

Runs inside Listmonk. Interval: `plus.worker_interval` (default `15s`; env
`LISTMONK_plus__worker_interval`). Also woken immediately by manual enrollment, "next now" presses,
resuming a sequence, and repeated subscriptions.

Failed sends (render or queue errors) are retried after 5 min and 30 min; after 3 attempts the step
is skipped (status `failed`) and the sequence continues.
