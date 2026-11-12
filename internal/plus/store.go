package plus

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/knadh/listmonk/models"
	"github.com/labstack/echo/v5"
	"github.com/lib/pq"
	null "gopkg.in/volatiletech/null.v6"
)

const seqSelect = `
SELECT s.id, s.uuid, s.name, s.status, s.list_id, l.name AS list_name, l.optin AS list_optin,
       s.trigger_enabled, s.reentry_after_exit, s.reentry_while_active, s.from_email,
       s.default_skip_landing_url, s.created_at, s.updated_at,
       COALESCE((SELECT jsonb_object_agg(status, n) FROM
                   (SELECT status, COUNT(*) AS n FROM plus_enrollments
                    WHERE sequence_id = s.id GROUP BY status) x), '{}') AS stats,
       (SELECT COUNT(*) FROM plus_sequence_steps WHERE sequence_id = s.id) AS step_count
FROM plus_sequences s JOIN lists l ON l.id = s.list_id`

const stepSelect = `
SELECT st.id, st.uuid, st.sequence_id, st.position, st.delay::TEXT AS delay,
       EXTRACT(EPOCH FROM st.delay)::BIGINT AS delay_seconds, st.skip_landing_url,
       st.created_at, st.updated_at,
       c.id AS campaign_id, c.uuid AS campaign_uuid, c.name AS campaign_name,
       c.subject AS campaign_subject, c.content_type, c.sent
FROM plus_sequence_steps st JOIN campaigns c ON c.id = st.campaign_id`

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
	DefaultLandingURL  *string `json:"default_skip_landing_url"`
}

// stepInput is the create/update payload for a step.
type stepInput struct {
	Position       *int    `json:"position"`
	Delay          *string `json:"delay"`
	SkipLandingURL *string `json:"skip_landing_url"`

	// The step's message (campaign) name. On add it defaults to
	// "<sequence> · Step N"; on update it renames the campaign.
	Name *string `json:"name"`

	// Only used when adding a step: the new campaign's subject. Defaults to the
	// name given, else the sequence's name.
	Subject *string `json:"subject"`
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
		INSERT INTO plus_sequences (name, list_id, trigger_enabled, reentry_after_exit, reentry_while_active, from_email,
		                            default_skip_landing_url)
		VALUES ($1, $2, COALESCE($3, true), COALESCE($4, 'ignore'), COALESCE($5, 'continue'), NULLIF($6, ''), NULLIF(TRIM($7), ''))
		RETURNING id`,
		strings.TrimSpace(*in.Name), *in.ListID, in.TriggerEnabled, in.ReentryAfterExit, in.ReentryWhileActive,
		null.StringFromPtr(in.FromEmail), null.StringFromPtr(in.DefaultLandingURL))
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
	err = p.inTx(func(tx *sqlx.Tx) error {
		_, err := tx.Exec(`
		UPDATE plus_sequences SET
		    name                 = COALESCE(NULLIF(TRIM($2), ''), name),
		    status               = COALESCE($3, status),
		    list_id              = COALESCE($4, list_id),
		    trigger_enabled      = COALESCE($5, trigger_enabled),
		    reentry_after_exit   = COALESCE($6, reentry_after_exit),
		    reentry_while_active = COALESCE($7, reentry_while_active),
		    from_email           = CASE WHEN $8::BOOL THEN NULLIF($9, '') ELSE from_email END,
		    default_skip_landing_url = CASE WHEN $10::BOOL THEN NULLIF(TRIM($11), '') ELSE default_skip_landing_url END,
		    updated_at           = NOW()
		WHERE id = $1`,
			id, in.Name, in.Status, in.ListID, in.TriggerEnabled, in.ReentryAfterExit, in.ReentryWhileActive,
			in.FromEmail != nil, null.StringFromPtr(in.FromEmail),
			in.DefaultLandingURL != nil, null.StringFromPtr(in.DefaultLandingURL))
		if err != nil {
			return err
		}
		if err := syncStepNames(tx, id, cur.Name); err != nil {
			return err
		}
		if listID == cur.ListID {
			return nil
		}
		// Draft whose list changed: move its step campaigns to the new list.
		if _, err := tx.Exec(`DELETE FROM campaign_lists WHERE campaign_id IN
		                        (SELECT campaign_id FROM plus_sequence_steps WHERE sequence_id = $1)`, id); err != nil {
			return err
		}
		_, err = tx.Exec(`
			INSERT INTO campaign_lists (campaign_id, list_id, list_name)
			SELECT st.campaign_id, l.id, l.name FROM plus_sequence_steps st, lists l
			WHERE st.sequence_id = $1 AND l.id = $2`, id, listID)
		return err
	})
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
	// Delete the sequence (steps, enrollments and sends cascade), then the
	// steps' campaigns (they reference nothing back, so they'd be orphaned).
	return p.inTx(func(tx *sqlx.Tx) error {
		var campIDs []int64
		if err := tx.Select(&campIDs, `SELECT campaign_id FROM plus_sequence_steps WHERE sequence_id = $1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM plus_sequences WHERE id = $1`, id); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM campaigns WHERE id = ANY($1::INT[])`, pq.Array(campIDs))
		return err
	})
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
	if in.DefaultLandingURL != nil && !validLandingURL(*in.DefaultLandingURL) {
		return errBad("the default landing page must be a full http(s) URL")
	}
	return nil
}

// validLandingURL accepts an empty string (= not set) or a full http(s) URL.
func validLandingURL(v string) bool {
	if strings.TrimSpace(v) == "" {
		return true
	}
	u, err := url.Parse(strings.TrimSpace(v))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// dbErr turns known constraint violations into friendly errors.
func (p *Plus) dbErr(err error, listID int) error {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		switch pqErr.Constraint {
		case "idx_plus_sequences_one_per_list":
			var name string
			_ = p.db.Get(&name, `SELECT name FROM plus_sequences WHERE list_id = $1 LIMIT 1`, listID)
			return echo.NewHTTPError(http.StatusConflict, fmt.Sprintf(
				"This list already has a sequence (%q). A list can have only one sequence.", name))
		case "plus_sequences_list_id_fkey":
			return errBad("list not found")
		}
		if pqErr.Code == "22007" || pqErr.Code == "22008" || pqErr.Code == "22P02" {
			return errBad("invalid value: %s", pqErr.Message)
		}
	}
	return err
}

// --- steps ---------------------------------------------------------------

func (p *Plus) validateStep(in stepInput, requireDelay bool) error {
	if requireDelay && in.Delay == nil {
		return errBad("delay is required")
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
	if in.SkipLandingURL != nil && !validLandingURL(*in.SkipLandingURL) {
		return errBad("the landing page must be a full http(s) URL")
	}
	if in.Name != nil && (strings.TrimSpace(*in.Name) == "" || len(*in.Name) > 2000) {
		return errBad("the message name can't be empty")
	}
	return nil
}

const newStepBody = `<p>Hi {{ .Subscriber.FirstName }},</p>
<p>Write this step's message here.</p>
<p>Can't wait for the next one? <a href="{{ SequenceNextURL }}">Send me the next email now</a>.</p>`

// addStep creates the step's campaign (a draft on the sequence's list, with
// Listmonk's default template) and the step itself.
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

	name := fmt.Sprintf("%s · Step %d", seq.Name, pos)
	if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
		name = strings.TrimSpace(*in.Name)
	}
	subject := seq.Name
	if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
		subject = strings.TrimSpace(*in.Name)
	}
	if in.Subject != nil && strings.TrimSpace(*in.Subject) != "" {
		subject = strings.TrimSpace(*in.Subject)
	}
	from := seq.FromEmail.String
	if from == "" {
		from = p.opt.FromEmail
	}

	camp, err := p.opt.Core.CreateCampaign(models.Campaign{
		Type:        models.CampaignTypeRegular,
		Name:        name,
		Subject:     subject,
		FromEmail:   from,
		Body:        newStepBody,
		ContentType: models.CampaignContentTypeRichtext,
		Messenger:   "email",
		Headers:     models.Headers{},
		Attribs:     models.JSON{},
		Tags:        pq.StringArray{},
		ArchiveMeta: json.RawMessage(`{}`),
	}, []int{seq.ListID}, nil)
	if err != nil {
		return seq, err
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
			INSERT INTO plus_sequence_steps (sequence_id, position, delay, campaign_id, skip_landing_url)
			VALUES ($1, $2, $3::INTERVAL, $4, NULLIF($5, ''))`,
			seqID, pos, strings.TrimSpace(*in.Delay), camp.ID, null.StringFromPtr(in.SkipLandingURL))
		if err != nil {
			return err
		}
		return syncStepNames(tx, seqID, seq.Name)
	})
	if err != nil {
		// Don't leave an orphan campaign behind.
		if _, derr := p.db.Exec(`DELETE FROM campaigns WHERE id = $1`, camp.ID); derr != nil {
			p.log.Printf("listmonk-plus: error removing campaign %d after failed step insert: %v", camp.ID, derr)
		}
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
				UPDATE plus_sequence_steps SET position = position + CASE WHEN $2::INT > $3::INT THEN 1 ELSE -1 END
				WHERE sequence_id = $1 AND position BETWEEN LEAST($2::INT, $3::INT) AND GREATEST($2::INT, $3::INT) AND id <> $4`,
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
			    skip_landing_url = CASE WHEN $3::BOOL THEN NULLIF($4, '') ELSE skip_landing_url END,
			    updated_at       = NOW()
			WHERE id = $1`,
			stepID, null.StringFromPtr(trimPtr(in.Delay)),
			in.SkipLandingURL != nil, null.StringFromPtr(trimPtr(in.SkipLandingURL)))
		if err != nil {
			return err
		}
		// Renaming the message renames its campaign. A name typed here no longer
		// has the automatic "<sequence> · Step N" form, so it's kept from now on.
		if in.Name != nil {
			if _, err := tx.Exec(`UPDATE campaigns SET name = $2, updated_at = NOW() WHERE id = $1`,
				cur.CampaignID, strings.TrimSpace(*in.Name)); err != nil {
				return err
			}
		}
		return syncStepNames(tx, seqID, seq.Name)
	})
	if err != nil {
		return seq, p.dbErr(err, seq.ListID)
	}
	return p.getSequence(seqID)
}

// deleteStep deletes a step and its campaign (with its stats).
func (p *Plus) deleteStep(seqID, stepID int) (Sequence, error) {
	seq, err := p.getSequence(seqID)
	if err != nil {
		return seq, err
	}
	if seq.Status == SeqActive {
		return seq, errBad("pause the sequence before deleting steps")
	}
	err = p.inTx(func(tx *sqlx.Tx) error {
		var st struct {
			Position   int `db:"position"`
			CampaignID int `db:"campaign_id"`
		}
		if err := tx.Get(&st, `DELETE FROM plus_sequence_steps WHERE id = $1 AND sequence_id = $2 RETURNING position, campaign_id`,
			stepID, seqID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errNotFound("step")
			}
			return err
		}
		if _, err := tx.Exec(`DELETE FROM campaigns WHERE id = $1`, st.CampaignID); err != nil {
			return err
		}
		if _, err := tx.Exec(`SET CONSTRAINTS ALL DEFERRED`); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE plus_sequence_steps SET position = position - 1, updated_at = NOW()
		                   WHERE sequence_id = $1 AND position > $2`, seqID, st.Position); err != nil {
			return err
		}
		return syncStepNames(tx, seqID, seq.Name)
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

// syncStepNames keeps step campaign names in the form "<sequence> · Step N" in
// line with the sequence's name and the steps' positions. Only names that still
// have that automatic form (with the sequence's previous name) are changed, so a
// name the user typed on the campaign screen is left alone.
func syncStepNames(tx *sqlx.Tx, seqID int, oldName string) error {
	_, err := tx.Exec(`
		UPDATE campaigns c SET name = sq.name || ' · Step ' || st.position
		FROM plus_sequence_steps st JOIN plus_sequences sq ON sq.id = st.sequence_id
		WHERE st.campaign_id = c.id AND st.sequence_id = $1
		  AND STARTS_WITH(c.name, $2 || ' · Step ')
		  AND SUBSTR(c.name, LENGTH($2 || ' · Step ') + 1) ~ '^[0-9]+$'
		  AND c.name <> sq.name || ' · Step ' || st.position`, seqID, oldName)
	return err
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

// Candidate is a subscriber who can be enrolled in a sequence.
type Candidate struct {
	ID    int    `db:"id" json:"id"`
	Email string `db:"email" json:"email"`
	Name  string `db:"name" json:"name"`
	Total int    `db:"total" json:"-"`
}

// candidates returns the subscribers that enrolling would actually enroll
// (the "Specific subscribers" picker): eligible on the sequence's list (§4.1),
// not currently in this sequence, never completed it, and — unless the
// sequence restarts people who left — never in it before. With excludeOther,
// people active in another sequence are left out too.
func (p *Plus) candidates(seqID int, query string, excludeOther bool, page, perPage int) ([]Candidate, int, error) {
	rules, err := getSeqRules(p.db, seqID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, 0, errNotFound("sequence")
		}
		return nil, 0, err
	}
	out := []Candidate{}
	err = p.db.Select(&out, `
		SELECT s.id, s.email, s.name, COUNT(*) OVER () AS total
		FROM subscribers s
		JOIN subscriber_lists sl ON sl.subscriber_id = s.id AND sl.list_id = $2
		WHERE s.status = 'enabled' AND sl.status <> 'unsubscribed'
		  AND ($3 <> 'double' OR sl.status = 'confirmed')
		  AND NOT EXISTS (SELECT 1 FROM plus_enrollments e WHERE e.sequence_id = $1 AND e.subscriber_id = s.id
		                  AND (e.status IN ('active', 'completed') OR $4 <> 'restart'))
		  AND (NOT $5 OR NOT EXISTS (SELECT 1 FROM plus_enrollments e WHERE e.sequence_id <> $1
		                             AND e.subscriber_id = s.id AND e.status = 'active'))
		  AND ($6 = '' OR s.email ILIKE '%' || $6 || '%' OR s.name ILIKE '%' || $6 || '%')
		ORDER BY LOWER(s.name), s.email
		LIMIT $7 OFFSET $8`,
		seqID, rules.ListID, rules.ListOptin, rules.ReentryAfterExit, excludeOther,
		strings.TrimSpace(query), perPage, (page-1)*perPage)
	total := 0
	if len(out) > 0 {
		total = out[0].Total
	}
	return out, total, err
}
