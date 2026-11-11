package plus

import (
	"encoding/json"
	"time"

	"github.com/jmoiron/sqlx/types"
	null "gopkg.in/volatiletech/null.v6"
)

// Sequence statuses.
const (
	SeqDraft  = "draft"
	SeqActive = "active"
	SeqPaused = "paused"
)

// Enrollment statuses and sources.
const (
	EnrActive    = "active"
	EnrCompleted = "completed"
	EnrCancelled = "cancelled"

	SrcTrigger    = "trigger"
	SrcManual     = "manual"
	SrcManualBulk = "manual-bulk"
	SrcAPI        = "api"
)

// Send statuses.
const (
	SendPending = "pending"
	SendQueued  = "queued"
	SendFailed  = "failed"
	SendSkipped = "skipped"
)

// Re-entry rules.
const (
	ReentryIgnore   = "ignore"
	ReentryRestart  = "restart"
	ReentryContinue = "continue"
)

// Sequence is a series of steps sent to subscribers of a list.
type Sequence struct {
	ID                 int         `db:"id" json:"id"`
	UUID               string      `db:"uuid" json:"uuid"`
	Name               string      `db:"name" json:"name"`
	Status             string      `db:"status" json:"status"`
	ListID             int         `db:"list_id" json:"list_id"`
	ListName           string      `db:"list_name" json:"list_name"`
	ListOptin          string      `db:"list_optin" json:"list_optin"`
	TriggerEnabled     bool        `db:"trigger_enabled" json:"trigger_enabled"`
	ReentryAfterExit   string      `db:"reentry_after_exit" json:"reentry_after_exit"`
	ReentryWhileActive string      `db:"reentry_while_active" json:"reentry_while_active"`
	FromEmail          null.String `db:"from_email" json:"from_email"`
	CreatedAt          time.Time   `db:"created_at" json:"created_at"`
	UpdatedAt          time.Time   `db:"updated_at" json:"updated_at"`

	// Counts of enrollments by status.
	Stats     types.JSONText `db:"stats" json:"stats"`
	StepCount int            `db:"step_count" json:"step_count"`

	Steps []Step `db:"-" json:"steps"`
}

// Count returns the number of enrollments with the given status.
func (s Sequence) Count(status string) int {
	var m map[string]int
	_ = json.Unmarshal(s.Stats, &m)
	return m[status]
}

// Step is one email in a sequence.
type Step struct {
	ID             int         `db:"id" json:"id"`
	UUID           string      `db:"uuid" json:"uuid"`
	SequenceID     int         `db:"sequence_id" json:"sequence_id"`
	Position       int         `db:"position" json:"position"`
	Delay          string      `db:"delay" json:"delay"`
	DelaySeconds   int64       `db:"delay_seconds" json:"delay_seconds"`
	TemplateID     int         `db:"template_id" json:"template_id"`
	TemplateName   string      `db:"template_name" json:"template_name"`
	Subject        null.String `db:"subject" json:"subject"`
	SkipLandingURL null.String `db:"skip_landing_url" json:"skip_landing_url"`
	CreatedAt      time.Time   `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time   `db:"updated_at" json:"updated_at"`
}

// Enrollment is one pass of a subscriber through a sequence.
type Enrollment struct {
	ID              int64       `db:"id" json:"id"`
	UUID            string      `db:"uuid" json:"uuid"`
	SequenceID      int         `db:"sequence_id" json:"sequence_id"`
	SubscriberID    int         `db:"subscriber_id" json:"subscriber_id"`
	SubscriberEmail string      `db:"subscriber_email" json:"subscriber_email"`
	SubscriberName  string      `db:"subscriber_name" json:"subscriber_name"`
	Source          string      `db:"source" json:"source"`
	Status          string      `db:"status" json:"status"`
	NextStep        int         `db:"next_step" json:"next_step"`
	NextSendAt      null.Time   `db:"next_send_at" json:"next_send_at"`
	CancelReason    null.String `db:"cancel_reason" json:"cancel_reason"`
	EnrolledAt      time.Time   `db:"enrolled_at" json:"enrolled_at"`
	UpdatedAt       time.Time   `db:"updated_at" json:"updated_at"`
	Sends           int         `db:"sends" json:"sends"`
	Total           int         `db:"total" json:"-"`
}

// EnrollResult summarises an enrollment request.
type EnrollResult struct {
	// DryRun is true when nothing was saved (preview).
	DryRun    bool           `json:"dry_run"`
	Enrolled  int            `json:"enrolled"`
	Restarted int            `json:"restarted"`
	Skipped   map[string]int `json:"skipped"`
}
