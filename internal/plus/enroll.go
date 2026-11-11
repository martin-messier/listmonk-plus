package plus

import (
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// seqRules is the subset of a sequence needed to enroll people.
type seqRules struct {
	ID                 int    `db:"id"`
	ListID             int    `db:"list_id"`
	ListOptin          string `db:"list_optin"`
	ReentryAfterExit   string `db:"reentry_after_exit"`
	ReentryWhileActive string `db:"reentry_while_active"`
}

// Skip reasons reported in EnrollResult.Skipped.
const (
	skipNotFound      = "not_found"
	skipNotOnList     = "not_on_list"
	skipUnsubscribed  = "unsubscribed"
	skipUnconfirmed   = "unconfirmed"
	skipAlreadyActive = "already_active"
	skipAlreadyDone   = "already_completed_or_left"
	skipInOther       = "in_other_sequence"
)

// classifySQL sorts candidate subscribers into eligibility classes
// (design §4.1) and re-entry classes (§4.2).
const classifySQL = `
WITH cand AS (SELECT DISTINCT UNNEST($2::INT[]) AS id)
SELECT c.id, CASE
    WHEN s.id IS NULL THEN 'not_found'
    WHEN s.status <> 'enabled' THEN 'subscriber_' || s.status
    WHEN sl.status IS NULL THEN 'not_on_list'
    WHEN sl.status = 'unsubscribed' THEN 'unsubscribed'
    WHEN $4 = 'double' AND sl.status <> 'confirmed' THEN 'unconfirmed'
    WHEN EXISTS (SELECT 1 FROM plus_enrollments e
                 WHERE e.sequence_id = $1 AND e.subscriber_id = c.id AND e.status = 'active') THEN 'active'
    WHEN $5 AND EXISTS (SELECT 1 FROM plus_enrollments e
                 WHERE e.sequence_id <> $1 AND e.subscriber_id = c.id AND e.status = 'active') THEN 'in_other_sequence'
    WHEN EXISTS (SELECT 1 FROM plus_enrollments e
                 WHERE e.sequence_id = $1 AND e.subscriber_id = c.id) THEN 'past'
    ELSE 'new'
  END AS class
FROM cand c
LEFT JOIN subscribers s ON s.id = c.id
LEFT JOIN subscriber_lists sl ON sl.subscriber_id = c.id AND sl.list_id = $3
ORDER BY c.id`

// enroll applies eligibility and re-entry rules to the given subscribers and
// creates enrollments, all inside tx. The sequence must have at least one step.
// With excludeOther, people currently active in another sequence are skipped.
func (p *Plus) enroll(tx *sqlx.Tx, seq seqRules, subIDs []int, source string, excludeOther bool) (EnrollResult, error) {
	res := EnrollResult{Skipped: map[string]int{}}
	if len(subIDs) == 0 {
		return res, nil
	}

	var rows []struct {
		ID    int    `db:"id"`
		Class string `db:"class"`
	}
	if err := tx.Select(&rows, classifySQL, seq.ID, pq.Array(subIDs), seq.ListID, seq.ListOptin, excludeOther); err != nil {
		return res, fmt.Errorf("classifying subscribers: %w", err)
	}

	var (
		toEnroll  []int
		toRestart []int
	)
	for _, r := range rows {
		switch r.Class {
		case "new":
			toEnroll = append(toEnroll, r.ID)
		case "active":
			if seq.ReentryWhileActive == ReentryRestart {
				toRestart = append(toRestart, r.ID)
			} else {
				res.Skipped[skipAlreadyActive]++
			}
		case "past":
			if seq.ReentryAfterExit == ReentryRestart {
				toEnroll = append(toEnroll, r.ID)
			} else {
				res.Skipped[skipAlreadyDone]++
			}
		default:
			// Ineligible: not_found, not_on_list, unsubscribed, unconfirmed, subscriber_<status>.
			res.Skipped[r.Class]++
		}
	}

	// Restart = close the current pass (history is kept) and open a new one.
	if len(toRestart) > 0 {
		if _, err := tx.Exec(`
			UPDATE plus_enrollments SET status = 'cancelled', cancel_reason = 'restarted',
			       next_send_at = NULL, updated_at = NOW()
			WHERE sequence_id = $1 AND subscriber_id = ANY($2::INT[]) AND status = 'active'`,
			seq.ID, pq.Array(toRestart)); err != nil {
			return res, fmt.Errorf("restarting enrollments: %w", err)
		}
		res.Restarted = len(toRestart)
		toEnroll = append(toEnroll, toRestart...)
	}

	if len(toEnroll) > 0 {
		r, err := tx.Exec(`
			INSERT INTO plus_enrollments (sequence_id, subscriber_id, source, next_step, next_send_at)
			SELECT $1, sub_id, $3, 1,
			       NOW() + (SELECT delay FROM plus_sequence_steps WHERE sequence_id = $1 AND position = 1)
			FROM UNNEST($2::INT[]) AS sub_id
			ON CONFLICT (sequence_id, subscriber_id) WHERE status = 'active' DO NOTHING`,
			seq.ID, pq.Array(toEnroll), source)
		if err != nil {
			return res, fmt.Errorf("creating enrollments: %w", err)
		}
		n, _ := r.RowsAffected()
		res.Enrolled = int(n) - res.Restarted
	}

	return res, nil
}

// getSeqRules loads the enrollment rules of a sequence.
func getSeqRules(q sqlx.Queryer, seqID int) (seqRules, error) {
	var s seqRules
	err := sqlx.Get(q, &s, `
		SELECT s.id, s.list_id, l.optin AS list_optin, s.reentry_after_exit, s.reentry_while_active
		FROM plus_sequences s JOIN lists l ON l.id = s.list_id WHERE s.id = $1`, seqID)
	return s, err
}
