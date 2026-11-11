// Package plus contains listmonk-plus additions to Listmonk: the event log
// (plus_events), sequences, and later the trigger engine.
//
// Fork rule: code lives here; upstream files only get small, documented hooks
// (see plus/UPSTREAM-TOUCHPOINTS.md).
package plus

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/knadh/listmonk/internal/core"
	"github.com/knadh/listmonk/internal/manager"
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

// Permissions (declared in permissions.json).
const (
	PermSequencesGet    = "sequences:get"
	PermSequencesManage = "sequences:manage"
)

// Opt holds the dependencies and settings for Plus.
type Opt struct {
	DB      *sqlx.DB
	Log     *log.Logger
	Core    *core.Core
	Manager *manager.Manager

	// Public root URL of the Listmonk install (for links in emails).
	RootURL string

	// Default "From" address (Listmonk's app.from_email).
	FromEmail string

	// How often the worker looks for events and due steps.
	WorkerInterval time.Duration

	// Max events / sends handled per worker pass.
	BatchSize int
}

// Plus is the entry point for listmonk-plus features.
type Plus struct {
	opt Opt
	db  *sqlx.DB
	log *log.Logger

	wake   chan struct{}
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
}

// New returns a new Plus instance.
func New(o Opt) *Plus {
	if o.WorkerInterval <= 0 {
		o.WorkerInterval = 15 * time.Second
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 200
	}
	o.RootURL = strings.TrimRight(o.RootURL, "/")

	return &Plus{
		opt:  o,
		db:   o.DB,
		log:  o.Log,
		wake: make(chan struct{}, 1),
	}
}

// Run starts the sequence worker. It blocks until Close() is called, so run it
// in a goroutine.
func (p *Plus) Run() {
	ctx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	p.cancel = cancel
	p.done = make(chan struct{})
	done := p.done
	p.mu.Unlock()
	defer close(done)

	p.log.Printf("listmonk-plus: sequence worker started (interval %s)", p.opt.WorkerInterval)
	t := time.NewTicker(p.opt.WorkerInterval)
	defer t.Stop()

	for {
		p.runOnce(ctx)

		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-p.wake:
		}
	}
}

// Close stops the worker and waits (briefly) for the current pass to finish.
// It must be called before the campaign manager is closed, as the worker pushes
// messages into the manager's queue.
func (p *Plus) Close() {
	p.mu.Lock()
	cancel, done := p.cancel, p.done
	p.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		p.log.Printf("listmonk-plus: worker did not stop within 5s")
	}
}

// Wake asks the worker to run a pass now instead of waiting for the next tick.
func (p *Plus) Wake() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
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
		return
	}
	p.Wake()
}
