package plus

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
)

// Data accessors for the admin HTML views (cmd/plus_views.go). The views live
// in package main because they reuse Listmonk's admin view helpers.

// ListOption is a list in the sequence editor's list picker.
type ListOption struct {
	ID    int    `db:"id" json:"id"`
	Name  string `db:"name" json:"name"`
	Optin string `db:"optin" json:"optin"`
}

// FormOptions holds the choices for the sequence and step editors.
type FormOptions struct {
	Lists []ListOption `json:"lists"`
}

// Sequences returns all sequences (with enrollment stats).
func (p *Plus) Sequences() ([]Sequence, error) {
	return p.getSequences()
}

// Sequence returns one sequence with its steps.
func (p *Plus) Sequence(id int) (Sequence, error) {
	return p.getSequence(id)
}

// Enrollments returns a page of a sequence's enrollments and the total count.
func (p *Plus) Enrollments(seqID int, status string, page, perPage int) ([]Enrollment, int, error) {
	if !oneOf(status, "", EnrActive, EnrCompleted, EnrCancelled) {
		status = ""
	}
	if page < 1 {
		page = 1
	}
	return p.getEnrollments(seqID, status, page, perPage)
}

// Options returns the lists for the editors.
func (p *Plus) Options() (FormOptions, error) {
	out := FormOptions{Lists: []ListOption{}}
	if err := p.db.Select(&out.Lists, `SELECT id, name, optin FROM lists WHERE status = 'active' ORDER BY LOWER(name)`); err != nil {
		return out, err
	}
	return out, nil
}

// StepRef identifies the sequence step that owns a campaign.
type StepRef struct {
	SequenceID     int    `db:"sequence_id" json:"sequence_id"`
	SequenceName   string `db:"sequence_name" json:"sequence_name"`
	SequenceStatus string `db:"sequence_status" json:"sequence_status"`
	ListID         int    `db:"list_id" json:"list_id"`
	Position       int    `db:"position" json:"position"`
	TotalSteps     int    `db:"total_steps" json:"total_steps"`
}

// StepOf returns the sequence step that owns the campaign, if any. Used by the
// guards in Listmonk's campaign handlers and by the campaign screen.
func (p *Plus) StepOf(campaignID int) (StepRef, bool) {
	var r StepRef
	err := p.db.Get(&r, `
		SELECT sq.id AS sequence_id, sq.name AS sequence_name, sq.status AS sequence_status, sq.list_id,
		       st.position, (SELECT COUNT(*) FROM plus_sequence_steps WHERE sequence_id = sq.id) AS total_steps
		FROM plus_sequence_steps st JOIN plus_sequences sq ON sq.id = st.sequence_id
		WHERE st.campaign_id = $1`, campaignID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			p.log.Printf("listmonk-plus: error looking up step of campaign %d: %v", campaignID, err)
		}
		return r, false
	}
	return r, true
}

// StepGuardError is the error returned when someone tries to broadcast,
// schedule or delete a step campaign directly.
func (r StepRef) StepGuardError(action string) error {
	return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf(
		"This campaign is step %d of the sequence %q and is sent automatically, one person at a time. It can't be %s here; manage it from the sequence.",
		r.Position, r.SequenceName, action))
}

// StepStats are one step's numbers over a date range.
type StepStats struct {
	SequenceID      int    `db:"sequence_id" json:"sequence_id"`
	Position        int    `db:"position" json:"position"`
	CampaignID      int    `db:"campaign_id" json:"campaign_id"`
	CampaignSubject string `db:"campaign_subject" json:"campaign_subject"`
	Sent            int    `db:"sent" json:"sent"`
	UniqueOpens     int    `db:"unique_opens" json:"unique_opens"`
	Opens           int    `db:"opens" json:"opens"`
	UniqueClicks    int    `db:"unique_clicks" json:"unique_clicks"`
	Clicks          int    `db:"clicks" json:"clicks"`
	Bounces         int    `db:"bounces" json:"bounces"`
	Unsubscribes    int    `db:"unsubscribes" json:"unsubscribes"`
}

// OpenRate / ClickRate use unique counts when Listmonk records who opened or
// clicked (individual tracking on), and total counts otherwise.
func (s StepStats) OpenRate() string {
	if s.UniqueOpens > 0 {
		return s.Pct(s.UniqueOpens)
	}
	return s.Pct(s.Opens)
}

func (s StepStats) ClickRate() string {
	if s.UniqueClicks > 0 {
		return s.Pct(s.UniqueClicks)
	}
	return s.Pct(s.Clicks)
}

// Pct returns n as a percentage of the step's sends ("–" when nothing was sent).
func (s StepStats) Pct(n int) string {
	if s.Sent == 0 {
		return "–"
	}
	return fmt.Sprintf("%.1f%%", float64(n)*100/float64(s.Sent))
}

// SequenceStats are one sequence's numbers over a date range.
type SequenceStats struct {
	ID        int    `db:"id" json:"id"`
	Name      string `db:"name" json:"name"`
	Status    string `db:"status" json:"status"`
	ListName  string `db:"list_name" json:"list_name"`
	Entered   int    `db:"entered" json:"entered"`
	Completed int    `db:"completed" json:"completed"`
	Stopped   int    `db:"stopped" json:"stopped"`
	Active    int    `db:"active_now" json:"active_now"`

	Steps []StepStats `db:"-" json:"steps"`
}

// Analytics returns per-sequence and per-step numbers between from and to.
// Opens and clicks come from Listmonk's own campaign tracking of each step's
// campaign. "Unique" counts need individual tracking to be enabled in
// Listmonk's privacy settings.
func (p *Plus) Analytics(from, to time.Time, seqID int) ([]SequenceStats, error) {
	out := []SequenceStats{}
	if err := p.db.Select(&out, `
		SELECT sq.id, sq.name, sq.status, l.name AS list_name,
		  (SELECT COUNT(*) FROM plus_enrollments e WHERE e.sequence_id = sq.id AND e.enrolled_at BETWEEN $1 AND $2) AS entered,
		  (SELECT COUNT(*) FROM plus_enrollments e WHERE e.sequence_id = sq.id AND e.status = 'completed' AND e.updated_at BETWEEN $1 AND $2) AS completed,
		  (SELECT COUNT(*) FROM plus_enrollments e WHERE e.sequence_id = sq.id AND e.status = 'cancelled' AND e.updated_at BETWEEN $1 AND $2) AS stopped,
		  (SELECT COUNT(*) FROM plus_enrollments e WHERE e.sequence_id = sq.id AND e.status = 'active') AS active_now
		FROM plus_sequences sq JOIN lists l ON l.id = sq.list_id
		WHERE ($3 = 0 OR sq.id = $3)
		ORDER BY LOWER(sq.name)`, from, to, seqID); err != nil {
		return nil, err
	}

	var steps []StepStats
	if err := p.db.Select(&steps, `
		SELECT st.sequence_id, st.position, c.id AS campaign_id, c.subject AS campaign_subject,
		  (SELECT COUNT(*) FROM plus_sends sd WHERE sd.step_id = st.id AND sd.status = 'queued' AND sd.created_at BETWEEN $1 AND $2) AS sent,
		  (SELECT COUNT(DISTINCT v.subscriber_id) FROM campaign_views v WHERE v.campaign_id = c.id AND v.created_at BETWEEN $1 AND $2) AS unique_opens,
		  (SELECT COUNT(*) FROM campaign_views v WHERE v.campaign_id = c.id AND v.created_at BETWEEN $1 AND $2) AS opens,
		  (SELECT COUNT(DISTINCT k.subscriber_id) FROM link_clicks k WHERE k.campaign_id = c.id AND k.created_at BETWEEN $1 AND $2) AS unique_clicks,
		  (SELECT COUNT(*) FROM link_clicks k WHERE k.campaign_id = c.id AND k.created_at BETWEEN $1 AND $2) AS clicks,
		  (SELECT COUNT(*) FROM bounces b WHERE b.campaign_id = c.id AND b.created_at BETWEEN $1 AND $2) AS bounces,
		  -- Unsubscribed while this was the last step they'd received.
		  (SELECT COUNT(*) FROM plus_enrollments e WHERE e.sequence_id = st.sequence_id AND e.status = 'cancelled'
		      AND e.cancel_reason = 'unsubscribed' AND e.next_step - 1 = st.position AND e.updated_at BETWEEN $1 AND $2) AS unsubscribes
		FROM plus_sequence_steps st JOIN campaigns c ON c.id = st.campaign_id
		WHERE ($3 = 0 OR st.sequence_id = $3)
		ORDER BY st.sequence_id, st.position`, from, to, seqID); err != nil {
		return nil, err
	}
	idx := map[int]int{}
	for i := range out {
		idx[out[i].ID] = i
		out[i].Steps = []StepStats{}
	}
	for _, s := range steps {
		if i, ok := idx[s.SequenceID]; ok {
			out[i].Steps = append(out[i].Steps, s)
		}
	}
	return out, nil
}
