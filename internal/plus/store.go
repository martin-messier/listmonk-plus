package plus

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
	"github.com/lib/pq"
	null "gopkg.in/volatiletech/null.v6"
)

const seqSelect = `
SELECT s.id, s.uuid, s.name, s.status, s.list_id, l.name AS list_name, l.optin AS list_optin,
       s.trigger_enabled, s.reentry_after_exit, s.reentry_while_active, s.from_email,
       s.created_at, s.updated_at,
       COALESCE((SELECT jsonb_object_agg(status, n) FROM
                   (SELECT status, COUNT(*) AS n FROM plus_enrollments
                    WHERE sequence_id = s.id GROUP BY status) x), '{}') AS stats,
       (SELECT COUNT(*) FROM plus_sequence_steps WHERE sequence_id = s.id) AS step_count
FROM plus_sequences s JOIN lists l ON l.id = s.list_id`

const stepSelect = `
SELECT st.id, st.uuid, st.sequence_id, st.position, st.delay::TEXT AS delay,
       EXTRACT(EPOCH FROM st.delay)::BIGINT AS delay_seconds,
       st.template_id, t.name AS template_name, st.subject, st.skip_landing_url,
       st.created_at, st.updated_at
FROM plus_sequence_steps st JOIN templates t ON t.id = st.template_id`

// errDryRun rolls back a preview transaction.
var errDryRun = errors.New("dry run")

func errNotFound(what string) error {
	return echo.NewHTTPError(http.StatusNotFound, what+" not found")
}

func errBad(format string, a ...any) error {
	return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf(format, a...))
}

// seqInput is the create/update payload for a sequence.
type seqInput struct {
	Name               *string `json:"name"`
	Status             *string `json:"status"`
	ListID             *int    `json:"list_id"`
	TriggerEnabled     *bool   `json:"trigger_enabled"`
	ReentryAfterExit   *string `json:"reentry_after_exit"`
	ReentryWhileActive *string `json:"reentry_while_active"`
	FromEmail          *string `json:"from_email"`
}

// stepInput is the create/update payload for a step.
type stepInput struct {
	Position       *int    `json:"position"`
	Delay          *string `json:"delay"`
	TemplateID     *int    `json:"template_id"`
	Subject        *string `json:"subject"`
	SkipLandingURL *string `json:"skip_landing_url"`
}

func (p *Plus) getSequences() ([]Sequence, error) {
	out := []Sequence{}
	if err := p.db.Select(&out, seqSelect+` ORDER BY s.id`); err != nil {
		return nil, err
	}
	return out, nil
}

func (p *Plus) getSequence(id int) (Sequence, error) {
	var s Sequence
	if err := p.db.Get(&s, seqSelect+` WHERE s.id = $1`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return s, errNotFound("sequence")
		}
		return s, err
	}
	s.Steps = []Step{}
	if err := p.db.Select(&s.Steps, stepSelect+` WHERE st.sequence_id = $1 ORDER BY st.position`, id); err != nil {
		return s, err
	}
	return s, nil
}

func (p *Plus) createSequence(in seqInput) (Sequence, error) {
	if in.Name == nil || strings.TrimSpace(*in.Name) == "" {
		return Sequence{}, errBad("name is required")
	}
	if in.ListID == nil {
		return Sequence{}, errBad("list_id is required")
	}
	if err := validateSeqInput(in); err != nil {
		return Sequence{}, err
	}
	if in.Status != nil && *in.Status != SeqDraft {
		return Sequence{}, errBad("new sequences start as 'draft'; add steps, then activate")
	}

	var id int
	err := p.db.Get(&id, `
		INSERT INTO plus_sequences (name, list_id, trigger_enabled, reentry_after_exit, reentry_while_active, from_email)
		VALUES ($1, $2, COALESCE($3, true), COALESCE($4, 'ignore'), COALESCE($5, 'continue'), NULLIF($6, ''))
		RETURNING id`,
		strings.TrimSpace(*in.Name), *in.ListID, in.TriggerEnabled, in.ReentryAfterExit, in.ReentryWhileActive,
		null.StringFromPtr(in.FromEmail))
	if err != nil {
		return Sequence{}, p.dbErr(err, *in.ListID)
	}
	return p.getSequence(id)
}

func (p *Plus) updateSequence(id int, in seqInput) (Sequence, error) {
	cur, err := p.getSequence(id)
	if err != nil {
		return cur, err
	}
	if err := validateSeqInput(in); err != nil {
		return cur, err
	}

	if in.ListID != nil && *in.ListID != cur.ListID && cur.Status != SeqDraft {
		return cur, errBad("the list can only be changed while the sequence is a draft")
	}
	if in.Status != nil && *in.Status != cur.Status {
		switch {
		case *in.Status == SeqDraft:
			return cur, errBad("a sequence can't go back to draft")
		case *in.Status == SeqActive && len(cur.Steps) == 0:
			return cur, errBad("add at least one step before activating")
		}
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) == "" {
		return cur, errBad("name can't be empty")
	}

	listID := cur.ListID
	if in.ListID != nil {
		listID = *in.ListID
	}
	_, err = p.db.Exec(`
		UPDATE plus_sequences SET
		    name                 = COALESCE(NULLIF(TRIM($2), ''), name),
		    status               = COALESCE($3, status),
		    list_id              = COALESCE($4, list_id),
		    trigger_enabled      = COALESCE($5, trigger_enabled),
		    reentry_after_exit   = COALESCE($6, reentry_after_exit),
		    reentry_while_active = COALESCE($7, reentry_while_active),
		    from_email           = CASE WHEN $8::BOOL THEN NULLIF($9, '') ELSE from_email END,
		    updated_at           = NOW()
		WHERE id = $1`,
		id, in.Name, in.Status, in.ListID, in.TriggerEnabled, in.ReentryAfterExit, in.ReentryWhileActive,
		in.FromEmail != nil, null.StringFromPtr(in.FromEmail))
	if err != nil {
		return cur, p.dbErr(err, listID)
	}
	if in.Status != nil && *in.Status == SeqActive {
		p.Wake() // resume: send overdue steps now
	}
	return p.getSequence(id)
}

func (p *Plus) deleteSequence(id int) error {
	cur, err := p.getSequence(id)
	if err != nil {
		return err
	}
	if cur.Status == SeqActive {
		return errBad("pause the sequence before deleting it")
	}
	_, err = p.db.Exec(`DELETE FROM plus_sequences WHERE id = $1`, id)
	return err
}

func validateSeqInput(in seqInput) error {
	if in.Status != nil && !oneOf(*in.Status, SeqDraft, SeqActive, SeqPaused) {
		return errBad("status must be draft, active or paused")
	}
	if in.ReentryAfterExit != nil && !oneOf(*in.ReentryAfterExit, ReentryIgnore, ReentryRestart) {
		return errBad("reentry_after_exit must be 'ignore' or 'restart'")
	}
	if in.ReentryWhileActive != nil && !oneOf(*in.ReentryWhileActive, ReentryContinue, ReentryRestart) {
		return errBad("reentry_while_active must be 'continue' or 'restart'")
	}
	if in.FromEmail != nil && len(*in.FromEmail) > 500 {
		return errBad("from_email is too long")
	}
	return nil
}

// dbErr turns known constraint violations into friendly errors.
func (p *Plus) dbErr(err error, listID int) error {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		switch pqErr.Constraint {
		case "idx_plus_sequences_one_auto_per_list":
			var name string
			_ = p.db.Get(&name, `SELECT name FROM plus_sequences WHERE list_id = $1 AND trigger_enabled
			                      AND status IN ('active', 'paused') LIMIT 1`, listID)
			return echo.NewHTTPError(http.StatusConflict, fmt.Sprintf(
				"this list already has an automated sequence (%q). A list can have only one; "+
					"pause it and turn its trigger off, or turn this sequence's trigger off", name))
		case "plus_sequences_list_id_fkey":
			return errBad("list not found")
		case "plus_sequence_steps_template_id_fkey":
			return errBad("template not found")
		}
		if pqErr.Code == "22007" || pqErr.Code == "22008" || pqErr.Code == "22P02" {
			return errBad("invalid value: %s", pqErr.Message)
		}
	}
	return err
}

// --- steps ---------------------------------------------------------------

func (p *Plus) validateStep(in stepInput, requireAll bool) error {
	if requireAll && (in.TemplateID == nil || in.Delay == nil) {
		return errBad("delay and template_id are required")
	}
	if in.Delay != nil {
		var ok bool
		if err := p.db.Get(&ok, `SELECT $1::INTERVAL >= '0'::INTERVAL`, strings.TrimSpace(*in.Delay)); err != nil {
			return errBad("invalid delay %q: use e.g. '0', '10 minutes', '2 hours', '3 days'", *in.Delay)
		}
		if !ok {
			return errBad("delay can't be negative")
		}
	}
	if in.TemplateID != nil {
		var typ string
		if err := p.db.Get(&typ, `SELECT type FROM templates WHERE id = $1`, *in.TemplateID); err != nil {
			return errBad("template %d not found", *in.TemplateID)
		}
		if typ != "tx" {
			return errBad("template %d is a %q template; sequence steps use transactional (tx) templates", *in.TemplateID, typ)
		}
	}
	if in.SkipLandingURL != nil && *in.SkipLandingURL != "" {
		u, err := url.Parse(*in.SkipLandingURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errBad("skip_landing_url must be a full http(s) URL")
		}
	}
	return nil
}

func (p *Plus) addStep(seqID int, in stepInput) (Sequence, error) {
	seq, err := p.getSequence(seqID)
	if err != nil {
		return seq, err
	}
	if err := p.validateStep(in, true); err != nil {
		return seq, err
	}
	pos := len(seq.Steps) + 1
	if in.Position != nil && *in.Position != pos {
		if seq.Status == SeqActive {
			return seq, errBad("while a sequence is active, steps can only be added at the end; pause it to insert elsewhere")
		}
		if *in.Position < 1 || *in.Position > pos {
			return seq, errBad("position must be between 1 and %d", pos)
		}
		pos = *in.Position
	}

	err = p.inTx(func(tx *sqlx.Tx) error {
		if _, err := tx.Exec(`SET CONSTRAINTS ALL DEFERRED`); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE plus_sequence_steps SET position = position + 1, updated_at = NOW()
		                      WHERE sequence_id = $1 AND position >= $2`, seqID, pos); err != nil {
			return err
		}
		_, err := tx.Exec(`
			INSERT INTO plus_sequence_steps (sequence_id, position, delay, template_id, subject, skip_landing_url)
			VALUES ($1, $2, $3::INTERVAL, $4, NULLIF($5, ''), NULLIF($6, ''))`,
			seqID, pos, strings.TrimSpace(*in.Delay), *in.TemplateID,
			null.StringFromPtr(in.Subject), null.StringFromPtr(in.SkipLandingURL))
		return err
	})
	if err != nil {
		return seq, p.dbErr(err, seq.ListID)
	}
	return p.getSequence(seqID)
}

func (p *Plus) updateStep(seqID, stepID int, in stepInput) (Sequence, error) {
	seq, err := p.getSequence(seqID)
	if err != nil {
		return seq, err
	}
	var cur *Step
	for i := range seq.Steps {
		if seq.Steps[i].ID == stepID {
			cur = &seq.Steps[i]
		}
	}
	if cur == nil {
		return seq, errNotFound("step")
	}
	if err := p.validateStep(in, false); err != nil {
		return seq, err
	}

	err = p.inTx(func(tx *sqlx.Tx) error {
		if in.Position != nil && *in.Position != cur.Position {
			if seq.Status == SeqActive {
				return errBad("pause the sequence before reordering steps")
			}
			to := *in.Position
			if to < 1 || to > len(seq.Steps) {
				return errBad("position must be between 1 and %d", len(seq.Steps))
			}
			if _, err := tx.Exec(`SET CONSTRAINTS ALL DEFERRED`); err != nil {
				return err
			}
			// Shift the steps between the old and new position by one.
			if _, err := tx.Exec(`
				UPDATE plus_sequence_steps SET position = position + CASE WHEN $2 > $3 THEN 1 ELSE -1 END
				WHERE sequence_id = $1 AND position BETWEEN LEAST($2, $3) AND GREATEST($2, $3) AND id <> $4`,
				seqID, cur.Position, to, stepID); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE plus_sequence_steps SET position = $2 WHERE id = $1`, stepID, to); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`
			UPDATE plus_sequence_steps SET
			    delay            = COALESCE($2::INTERVAL, delay),
			    template_id      = COALESCE($3, template_id),
			    subject          = CASE WHEN $4::BOOL THEN NULLIF($5, '') ELSE subject END,
			    skip_landing_url = CASE WHEN $6::BOOL THEN NULLIF($7, '') ELSE skip_landing_url END,
			    updated_at       = NOW()
			WHERE id = $1`,
			stepID, null.StringFromPtr(trimPtr(in.Delay)), in.TemplateID,
			in.Subject != nil, null.StringFromPtr(in.Subject),
			in.SkipLandingURL != nil, null.StringFromPtr(in.SkipLandingURL))
		return err
	})
	if err != nil {
		return seq, p.dbErr(err, seq.ListID)
	}
	return p.getSequence(seqID)
}

func (p *Plus) deleteStep(seqID, stepID int) (Sequence, error) {
	seq, err := p.getSequence(seqID)
	if err != nil {
		return seq, err
	}
	if seq.Status == SeqActive {
		return seq, errBad("pause the sequence before deleting steps")
	}
	err = p.inTx(func(tx *sqlx.Tx) error {
		var pos int
		if err := tx.Get(&pos, `DELETE FROM plus_sequence_steps WHERE id = $1 AND sequence_id = $2 RETURNING position`,
			stepID, seqID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errNotFound("step")
			}
			return err
		}
		if _, err := tx.Exec(`SET CONSTRAINTS ALL DEFERRED`); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE plus_sequence_steps SET position = position - 1, updated_at = NOW()
		                   WHERE sequence_id = $1 AND position > $2`, seqID, pos)
		return err
	})
	if err != nil {
		return seq, err
	}
	return p.getSequence(seqID)
}

// --- enrollments -----------------------------------------------------------

type enrollInput struct {
	SubscriberIDs    []int    `json:"subscriber_ids"`
	SubscriberEmails []string `json:"subscriber_emails"`
	All              bool     `json:"all"`
	Source           string   `json:"source"`

	// Skip people currently active in another sequence.
	ExcludeOther bool `json:"exclude_other_sequences"`

	// Preview: compute the result without saving anything.
	DryRun bool `json:"dry_run"`
}

func (p *Plus) enrollManual(seqID int, in enrollInput) (EnrollResult, error) {
	seq, err := p.getSequence(seqID)
	if err != nil {
		return EnrollResult{}, err
	}
	if seq.Status == SeqDraft {
		return EnrollResult{}, errBad("activate (or pause) the sequence before enrolling people")
	}
	if len(seq.Steps) == 0 {
		return EnrollResult{}, errBad("the sequence has no steps")
	}
	hasList := len(in.SubscriberIDs) > 0 || len(in.SubscriberEmails) > 0
	if in.All == hasList {
		return EnrollResult{}, errBad("send either subscriber_ids / subscriber_emails, or all=true")
	}

	source := in.Source
	switch {
	case source == "":
		source = SrcAPI
		if in.All {
			source = SrcManualBulk
		}
	case !oneOf(source, SrcManual, SrcManualBulk, SrcAPI):
		return EnrollResult{}, errBad("source must be manual, manual-bulk or api")
	}

	ids := append([]int{}, in.SubscriberIDs...)
	notFound := 0
	if len(in.SubscriberEmails) > 0 {
		emails := make([]string, 0, len(in.SubscriberEmails))
		for _, e := range in.SubscriberEmails {
			if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
				emails = append(emails, e)
			}
		}
		var found []struct {
			ID    int    `db:"id"`
			Email string `db:"email"`
		}
		if err := p.db.Select(&found, `SELECT id, LOWER(email) AS email FROM subscribers WHERE LOWER(email) = ANY($1::TEXT[])`,
			pq.Array(emails)); err != nil {
			return EnrollResult{}, err
		}
		seen := map[string]bool{}
		for _, f := range found {
			ids = append(ids, f.ID)
			seen[f.Email] = true
		}
		uniq := map[string]bool{}
		for _, e := range emails {
			if !seen[e] && !uniq[e] {
				notFound++
			}
			uniq[e] = true
		}
	}
	if in.All {
		ids = []int{}
		if err := p.db.Select(&ids, `SELECT subscriber_id FROM subscriber_lists WHERE list_id = $1 ORDER BY subscriber_id`,
			seq.ListID); err != nil {
			return EnrollResult{}, err
		}
	}

	rules, err := getSeqRules(p.db, seqID)
	if err != nil {
		return EnrollResult{}, err
	}

	var res EnrollResult
	err = p.inTx(func(tx *sqlx.Tx) error {
		var err error
		res, err = p.enroll(tx, rules, ids, source, in.ExcludeOther)
		if err == nil && in.DryRun {
			return errDryRun // roll back
		}
		return err
	})
	if errors.Is(err, errDryRun) {
		err = nil
		res.DryRun = true
	}
	if notFound > 0 {
		if res.Skipped == nil {
			res.Skipped = map[string]int{}
		}
		res.Skipped[skipNotFound] += notFound
	}
	if err == nil && !res.DryRun && res.Enrolled+res.Restarted > 0 {
		p.Wake() // steps with a 0 delay go out right away
	}
	return res, err
}

func (p *Plus) getEnrollments(seqID int, status string, page, perPage int) ([]Enrollment, int, error) {
	out := []Enrollment{}
	err := p.db.Select(&out, `
		SELECT e.id, e.uuid, e.sequence_id, e.subscriber_id, COALESCE(s.email, '') AS subscriber_email,
		       COALESCE(s.name, '') AS subscriber_name, e.source, e.status, e.next_step, e.next_send_at,
		       e.cancel_reason, e.enrolled_at, e.updated_at,
		       (SELECT COUNT(*) FROM plus_sends sd WHERE sd.enrollment_id = e.id AND sd.status = 'queued') AS sends,
		       COUNT(*) OVER () AS total
		FROM plus_enrollments e LEFT JOIN subscribers s ON s.id = e.subscriber_id
		WHERE e.sequence_id = $1 AND ($2 = '' OR e.status = $2)
		ORDER BY e.id DESC LIMIT $3 OFFSET $4`, seqID, status, perPage, (page-1)*perPage)
	total := 0
	if len(out) > 0 {
		total = out[0].Total
	}
	return out, total, err
}

func (p *Plus) cancelEnrollment(seqID int, enrID int64) error {
	r, err := p.db.Exec(`
		UPDATE plus_enrollments SET status = 'cancelled', cancel_reason = 'cancelled_by_admin',
		       next_send_at = NULL, updated_at = NOW()
		WHERE id = $1 AND sequence_id = $2 AND status = 'active'`, enrID, seqID)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return errBad("no active enrollment with that id in this sequence")
	}
	return nil
}

// --- helpers ---------------------------------------------------------------

func (p *Plus) inTx(fn func(*sqlx.Tx) error) error {
	tx, err := p.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func oneOf(v string, opts ...string) bool {
	for _, o := range opts {
		if v == o {
			return true
		}
	}
	return false
}

func trimPtr(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	return &t
}
