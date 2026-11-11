// Package plus contains listmonk-plus additions to Listmonk: the event log
// (plus_events) and, later, sequences and the trigger engine.
//
// Fork rule: code lives here; upstream files only get small, documented hooks
// (see plus/UPSTREAM-TOUCHPOINTS.md).
package plus

import (
	"log"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// Event types written to plus_events.
const (
	EvSubCreated      = "subscription.created"
	EvSubConfirmed    = "subscription.confirmed"
	EvSubUnsubscribed = "subscription.unsubscribed"
	EvSubRemoved      = "subscription.removed"
	EvSubRepeated     = "subscription.repeated"
)

// Plus is the entry point for listmonk-plus features.
type Plus struct {
	db  *sqlx.DB
	log *log.Logger
}

// New returns a new Plus instance.
func New(db *sqlx.DB, lo *log.Logger) *Plus {
	return &Plus{db: db, log: lo}
}

// ActiveListIDs returns the IDs of the given lists (by UUID) that the subscriber
// is currently subscribed to (status unconfirmed or confirmed). Call it before
// re-subscribing an existing subscriber so that RecordRepeated can tell which
// subscriptions were repeats. Errors are logged, not returned: this must never
// break the upstream subscription flow.
func (p *Plus) ActiveListIDs(subID int, listUUIDs []string) []int {
	var ids []int
	if err := p.db.Select(&ids, `
		SELECT sl.list_id FROM subscriber_lists sl
		JOIN lists l ON l.id = sl.list_id
		WHERE sl.subscriber_id = $1 AND l.uuid = ANY($2::UUID[])
		  AND sl.status IN ('unconfirmed', 'confirmed')
		ORDER BY sl.list_id`, subID, pq.Array(listUUIDs)); err != nil {
		p.log.Printf("listmonk-plus: error fetching active subscriptions for %d: %v", subID, err)
		return nil
	}
	return ids
}

// RecordRepeated records a subscription.repeated event for each list the
// subscriber was already actively subscribed to and subscribed to again (e.g. by
// submitting the public subscription form again). Status changes are captured by
// database triggers; this case isn't, because the status doesn't change.
func (p *Plus) RecordRepeated(subID int, listIDs []int, source string) {
	if len(listIDs) == 0 {
		return
	}
	if _, err := p.db.Exec(`
		INSERT INTO plus_events (type, subscriber_id, list_id, payload)
		SELECT $1, $2, sl.list_id,
		       jsonb_build_object('status', sl.status, 'source', $4::TEXT)
		FROM subscriber_lists sl
		WHERE sl.subscriber_id = $2 AND sl.list_id = ANY($3::INT[])
		  AND sl.status IN ('unconfirmed', 'confirmed')
		ORDER BY sl.list_id`, EvSubRepeated, subID, pq.Array(listIDs), source); err != nil {
		p.log.Printf("listmonk-plus: error recording %s for %d: %v", EvSubRepeated, subID, err)
	}
}
