package plus

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	null "gopkg.in/volatiletech/null.v6"
)

const (
	// While a step is being sent, the enrollment's next_send_at is pushed this
	// far into the future so that no other worker picks it up meanwhile.
	sendLease = 10 * time.Minute

	// Attempts per step before giving up on it and moving to the next step.
	maxSendAttempts = 3
)

// Backoff before retry n (1-based) of a failed step.
var sendBackoff = []time.Duration{5 * time.Minute, 30 * time.Minute, 2 * time.Hour}

// runOnce does one worker pass: consume pending events, then send due steps.
func (p *Plus) runOnce(ctx context.Context) {
	for ctx.Err() == nil {
		n, err := p.processEvents()
		if err != nil {
			p.log.Printf("listmonk-plus: error processing events: %v", err)
			break
		}
		if n < p.opt.BatchSize {
			break
		}
	}

	for ctx.Err() == nil {
		n, err := p.processDue(ctx)
		if err != nil {
			p.log.Printf("listmonk-plus: error sending sequence steps: %v", err)
			break
		}
		if n < p.opt.BatchSize {
			break
		}
	}
}

type event struct {
	ID           int64           `db:"id"`
	Type         string          `db:"type"`
	SubscriberID null.Int        `db:"subscriber_id"`
	ListID       null.Int        `db:"list_id"`
	Payload      json.RawMessage `db:"payload"`
}

// processEvents consumes a batch of pending events (design §6a) and returns how
// many it handled.
func (p *Plus) processEvents() (int, error) {
	tx, err := p.db.Beginx()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var evs []event
	if err := tx.Select(&evs, `
		SELECT id, type, subscriber_id, list_id, payload FROM plus_events
		WHERE processed_at IS NULL ORDER BY id LIMIT $1
		FOR UPDATE SKIP LOCKED`, p.opt.BatchSize); err != nil {
		return 0, err
	}
	if len(evs) == 0 {
		return 0, nil
	}

	// Automated sequences (at most one per list, D5) for the lists involved.
	listIDs := make([]int64, 0, len(evs))
	for _, e := range evs {
		if e.ListID.Valid {
			listIDs = append(listIDs, int64(e.ListID.Int))
		}
	}
	var seqs []seqRules
	if err := tx.Select(&seqs, `
		SELECT s.id, s.list_id, l.optin AS list_optin, s.reentry_after_exit, s.reentry_while_active
		FROM plus_sequences s JOIN lists l ON l.id = s.list_id
		WHERE s.list_id = ANY($1::INT[]) AND s.status = 'active' AND s.trigger_enabled`,
		pq.Array(listIDs)); err != nil {
		return 0, err
	}
	auto := make(map[int]seqRules, len(seqs))
	for _, s := range seqs {
		auto[s.ListID] = s
	}

	ids := make([]int64, 0, len(evs))
	for _, e := range evs {
		ids = append(ids, e.ID)
		if !e.SubscriberID.Valid || !e.ListID.Valid {
			continue
		}

		// Each event in its own savepoint: one bad event must not block the queue.
		if _, err := tx.Exec(`SAVEPOINT plus_ev`); err != nil {
			return 0, err
		}
		if err := p.handleEvent(tx, e, auto); err != nil {
			p.log.Printf("listmonk-plus: error handling event %d (%s): %v", e.ID, e.Type, err)
			if _, err := tx.Exec(`ROLLBACK TO SAVEPOINT plus_ev`); err != nil {
				return 0, err
			}
			continue
		}
		if _, err := tx.Exec(`RELEASE SAVEPOINT plus_ev`); err != nil {
			return 0, err
		}
	}

	if _, err := tx.Exec(`UPDATE plus_events SET processed_at = NOW() WHERE id = ANY($1::BIGINT[])`,
		pq.Array(ids)); err != nil {
		return 0, err
	}
	return len(evs), tx.Commit()
}

func (p *Plus) handleEvent(tx *sqlx.Tx, e event, auto map[int]seqRules) error {
	subID, listID := e.SubscriberID.Int, e.ListID.Int

	switch e.Type {
	case EvSubUnsubscribed, EvSubRemoved:
		// Stop every sequence on that list (automated or manual) for this person.
		reason := "unsubscribed"
		if e.Type == EvSubRemoved {
			reason = "removed_from_list"
		}
		_, err := tx.Exec(`
			UPDATE plus_enrollments SET status = 'cancelled', cancel_reason = $3,
			       next_send_at = NULL, updated_at = NOW()
			WHERE subscriber_id = $1 AND status = 'active'
			  AND sequence_id IN (SELECT id FROM plus_sequences WHERE list_id = $2)`,
			subID, listID, reason)
		return err

	case EvSubCreated, EvSubConfirmed, EvSubRepeated:
		seq, ok := auto[listID]
		if !ok || !triggerMatches(seq.ListOptin, e) {
			return nil
		}
		res, err := p.enroll(tx, seq, []int{subID}, SrcTrigger, false)
		if err != nil {
			return err
		}
		if res.Enrolled+res.Restarted > 0 {
			p.log.Printf("listmonk-plus: subscriber %d entered sequence %d (%s)", subID, seq.ID, e.Type)
		}
	}
	return nil
}

// triggerMatches implements design §3.3: single opt-in lists start on
// "created", double opt-in lists only on "confirmed". "repeated" counts on both
// (on double opt-in only if the subscription is confirmed).
func triggerMatches(optin string, e event) bool {
	switch e.Type {
	case EvSubCreated:
		return optin != "double"
	case EvSubConfirmed:
		return optin == "double"
	case EvSubRepeated:
		if optin != "double" {
			return true
		}
		var pl struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(e.Payload, &pl)
		return pl.Status == "confirmed"
	}
	return false
}

// dueRow is an enrollment whose next step is due.
type dueRow struct {
	EnrollmentID int64    `db:"enrollment_id"`
	SequenceID   int      `db:"sequence_id"`
	SubscriberID int      `db:"subscriber_id"`
	NextStep     int      `db:"next_step"`
	ListID       int      `db:"list_id"`
	ListOptin    string   `db:"list_optin"`
	StepID       null.Int `db:"step_id"`
	CampaignID   null.Int `db:"campaign_id"`
}

// processDue claims a batch of due enrollments and sends their next step
// (design §6b). Returns how many enrollments it handled.
func (p *Plus) processDue(ctx context.Context) (int, error) {
	// Claim: lock due rows and push them out by the lease so that no other worker
	// takes them while we send. Each row's real next_send_at is set when done.
	tx, err := p.db.Beginx()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var rows []dueRow
	if err := tx.Select(&rows, `
		SELECT e.id AS enrollment_id, e.sequence_id, e.subscriber_id, e.next_step,
		       s.list_id, l.optin AS list_optin,
		       st.id AS step_id, st.campaign_id
		FROM plus_enrollments e
		JOIN plus_sequences s ON s.id = e.sequence_id AND s.status = 'active'
		JOIN lists l ON l.id = s.list_id
		LEFT JOIN plus_sequence_steps st ON st.sequence_id = e.sequence_id AND st.position = e.next_step
		WHERE e.status = 'active' AND e.next_send_at <= NOW()
		ORDER BY e.next_send_at
		LIMIT $1
		FOR UPDATE OF e SKIP LOCKED`, p.opt.BatchSize); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.EnrollmentID
	}
	if _, err := tx.Exec(`UPDATE plus_enrollments SET next_send_at = NOW() + $2::INTERVAL WHERE id = ANY($1::BIGINT[])`,
		pq.Array(ids), fmt.Sprintf("%d seconds", int(sendLease.Seconds()))); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}

	cache := campaignCache{}
	for _, r := range rows {
		if ctx.Err() != nil {
			// Shutting down: release the remaining claims so they go out right after restart.
			p.releaseClaim(r.EnrollmentID)
			continue
		}
		if err := p.handleDue(r, cache); err != nil {
			p.log.Printf("listmonk-plus: enrollment %d step %d: %v", r.EnrollmentID, r.NextStep, err)
		}
	}
	return len(rows), nil
}

func (p *Plus) releaseClaim(enrollmentID int64) {
	if _, err := p.db.Exec(`UPDATE plus_enrollments SET next_send_at = NOW() WHERE id = $1 AND status = 'active'`,
		enrollmentID); err != nil {
		p.log.Printf("listmonk-plus: error releasing enrollment %d: %v", enrollmentID, err)
	}
}

func (p *Plus) handleDue(r dueRow, cache campaignCache) error {
	// No step at this position (steps were removed): the pass is complete.
	if !r.StepID.Valid {
		return p.advance(r.EnrollmentID, r.SequenceID, r.NextStep)
	}

	// Re-check eligibility right before sending (§4.1).
	var reason null.String
	if err := p.db.Get(&reason, `
		SELECT CASE
		    WHEN s.id IS NULL THEN 'subscriber_deleted'
		    WHEN s.status <> 'enabled' THEN 'subscriber_' || s.status
		    WHEN sl.status IS NULL THEN 'removed_from_list'
		    WHEN sl.status = 'unsubscribed' THEN 'unsubscribed'
		    WHEN $3 = 'double' AND sl.status <> 'confirmed' THEN 'unconfirmed'
		    ELSE NULL END
		FROM (SELECT 1) x
		LEFT JOIN subscribers s ON s.id = $1
		LEFT JOIN subscriber_lists sl ON sl.subscriber_id = $1 AND sl.list_id = $2`,
		r.SubscriberID, r.ListID, r.ListOptin); err != nil {
		return err
	}
	if reason.Valid {
		_, err := p.db.Exec(`
			UPDATE plus_enrollments SET status = 'cancelled', cancel_reason = $2,
			       next_send_at = NULL, updated_at = NOW()
			WHERE id = $1 AND status = 'active'`, r.EnrollmentID, reason.String)
		return err
	}

	// Record the send before sending. The UNIQUE (enrollment_id, step_id) key is
	// what guarantees a step is never sent twice in one pass.
	var sd struct {
		ID       int64  `db:"id"`
		UUID     string `db:"uuid"`
		Status   string `db:"status"`
		Attempts int    `db:"attempts"`
		Inserted bool   `db:"inserted"`
	}
	if err := p.db.Get(&sd, `
		INSERT INTO plus_sends (enrollment_id, step_id, subscriber_id, status)
		VALUES ($1, $2, $3, 'pending')
		ON CONFLICT (enrollment_id, step_id) DO UPDATE SET updated_at = NOW()
		RETURNING id, uuid, status, attempts, (xmax = 0) AS inserted`,
		r.EnrollmentID, r.StepID.Int, r.SubscriberID); err != nil {
		return err
	}

	switch {
	case sd.Status == SendQueued || sd.Status == SendSkipped:
		// Already sent; only the advance was missed.
		return p.advance(r.EnrollmentID, r.SequenceID, r.NextStep)
	case sd.Status == SendPending && !sd.Inserted:
		// A previous attempt stopped between "pending" and "queued". It may have
		// been handed to the mailer: treat as sent rather than risk a duplicate.
		if _, err := p.db.Exec(`UPDATE plus_sends SET status = 'queued', error = 'assumed sent after interruption', updated_at = NOW() WHERE id = $1`, sd.ID); err != nil {
			return err
		}
		return p.advance(r.EnrollmentID, r.SequenceID, r.NextStep)
	}

	// New send, or a retry of a failed one.
	attempts := sd.Attempts + 1
	if _, err := p.db.Exec(`UPDATE plus_sends SET status = 'pending', attempts = $2, updated_at = NOW() WHERE id = $1`,
		sd.ID, attempts); err != nil {
		return err
	}

	sendErr := p.sendCampaign(cache, int(r.CampaignID.Int), r.SubscriberID)
	if sendErr == nil {
		if _, err := p.db.Exec(`UPDATE plus_sends SET status = 'queued', error = NULL, updated_at = NOW() WHERE id = $1`, sd.ID); err != nil {
			return err
		}
		return p.advance(r.EnrollmentID, r.SequenceID, r.NextStep)
	}

	// Failed.
	if _, err := p.db.Exec(`UPDATE plus_sends SET status = 'failed', error = $2, updated_at = NOW() WHERE id = $1`,
		sd.ID, sendErr.Error()); err != nil {
		return err
	}
	if attempts >= maxSendAttempts {
		p.log.Printf("listmonk-plus: giving up on enrollment %d step %d after %d attempts: %v",
			r.EnrollmentID, r.NextStep, attempts, sendErr)
		return p.advance(r.EnrollmentID, r.SequenceID, r.NextStep)
	}
	wait := sendBackoff[attempts-1]
	if _, err := p.db.Exec(`UPDATE plus_enrollments SET next_send_at = NOW() + $2::INTERVAL, updated_at = NOW() WHERE id = $1`,
		r.EnrollmentID, fmt.Sprintf("%d seconds", int(wait.Seconds()))); err != nil {
		return fmt.Errorf("rescheduling after failed send: %w", err)
	}
	return fmt.Errorf("send failed (attempt %d, retry in %s): %w", attempts, wait, sendErr)
}

// advance moves an enrollment past step `pos`: schedules the next step after
// its delay, or completes the pass if there is none. The next_step = pos guard
// makes it safe against double-advancing.
func (p *Plus) advance(enrollmentID int64, seqID, pos int) error {
	_, err := p.db.Exec(`
		WITH nx AS (SELECT delay FROM plus_sequence_steps WHERE sequence_id = $2 AND position = $3 + 1)
		UPDATE plus_enrollments SET
		    next_step    = $3 + 1,
		    next_send_at = NOW() + (SELECT delay FROM nx),
		    status       = CASE WHEN EXISTS (SELECT 1 FROM nx) THEN 'active' ELSE 'completed' END,
		    updated_at   = NOW()
		WHERE id = $1 AND next_step = $3 AND status = 'active'`, enrollmentID, seqID, pos)
	return err
}
