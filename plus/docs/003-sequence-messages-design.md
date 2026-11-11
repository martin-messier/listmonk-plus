# 003 — Sequence messages are campaigns (design, milestone 5)

Status: **agreed 2026-11-11** · Supersedes decision **D1** of `001-sequences-design.md`
("steps use transactional templates").

## 1. Goal

Each message of a sequence is edited with Listmonk's **native campaign screen** (tabs View,
Campaign, Content, Attributes, Attachments, Archive). The only difference from a broadcast: it is
sent **automatically, one person at a time**, on the sequence's schedule, instead of to the whole
list at once.

## 2. Model

- Every step owns exactly one **campaign** (`plus_sequence_steps.campaign_id`, unique). The
  campaign is a normal row in `campaigns`, kept in status `draft` forever (Listmonk's campaign
  scheduler only sends `running` campaigns, so it can never broadcast it).
- **Timing** (delay, order) lives in the sequence editor. **Content** (subject, from, template,
  body, alt body, headers, attachments, archive, attributes) lives in the campaign screen.
- The campaign's list is always the sequence's list.
- Transactional-template steps are **removed** (`template_id`, `subject` columns dropped). Existing
  steps were test data and are deleted by the migration (M5-D2).

## 3. Sending

The worker renders the step's campaign for one subscriber exactly like Listmonk's "Send test" does:
`manager.NewCampaignMessage(campaign, subscriber)` on a campaign compiled with
`manager.TemplateFuncs(campaign)`. The compiled campaign is cached per campaign and recompiled when
`campaigns.updated_at` changes. The resulting message carries the same headers as a broadcast
(`X-Listmonk-Campaign`, `X-Listmonk-Subscriber`, `List-Unsubscribe` + one-click when Listmonk's
"unsubscribe header" privacy setting is on, custom campaign headers) and is pushed through
`manager.PushMessage`.

Consequences, all native:
- **Opens / clicks** (`{{ TrackView }}`, `{{ TrackLink }}`) are recorded against the step's campaign.
- **Unsubscribe**: Listmonk's `{{ UnsubscribeURL }}` unsubscribes from the campaign's lists = the
  sequence's list; the `subscriber_lists` trigger then stops the sequence.
  `{{ SequenceUnsubscribeURL }}` and its page are **removed**.
- **Bounces** are attributed to the step's campaign.
- `campaigns.sent` is incremented per message so the View tab shows a real count.

`{{ SequenceNextURL }}` keeps working: it is added to Listmonk's campaign tag rewriting (like
`{{ UnsubscribeURL }}`) and resolves to `/plus/seq/next/<campaign uuid>/<subscriber uuid>`,
which finds that person's active pass whose last sent step is this campaign.

## 4. Visibility (M5-D1)

Step campaigns appear **only under their sequence**:
- Hidden from: Campaigns list, Campaigns → Analytics (and its picker), the dashboard's campaign
  counts and lists, a list's campaigns tab, anywhere else that lists or counts campaigns
  (inventory: `plus/UPSTREAM-TOUCHPOINTS.md`).
- Shown in: the sequence editor (one row per step, "Edit message"), the step's own campaign screen,
  and a new **Sequences → Analytics** page (per sequence and step: sent, opens, clicks, unsubscribes,
  bounces, over a date range).
- The public **Archive** follows the campaign's own Archive tab setting (native behaviour).

## 5. Safeguards (a step must never be broadcast)

- The step's campaign screen shows a banner ("Step 2 of 'Welcome series' — sent automatically"),
  hides Start / Schedule / Pause / Cancel, and locks the Lists field.
- The server refuses status changes (start/schedule/pause/cancel), list changes and deletion of a
  step campaign, through the UI and the API. A step (and its campaign) is deleted from the sequence
  editor, only while the sequence isn't active.
- "Send test" stays available (it never touches the list).

## 6. Upstream touch points added

Campaign screen template (banner, hidden controls, locked lists); campaign status / update / delete
handlers (guards); every campaign listing/counting query (filter); the campaign tag-rewrite regex
and `TemplateFuncs` (`SequenceNextURL`). Each is listed in `plus/UPSTREAM-TOUCHPOINTS.md`.
