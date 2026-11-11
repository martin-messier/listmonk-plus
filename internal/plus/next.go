package plus

import (
	"bytes"
	"database/sql"
	_ "embed"
	"errors"
	"html/template"
	"net/http"
	"regexp"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
	null "gopkg.in/volatiletech/null.v6"
)

// "Send me the next email now" (design §7).
//
// GET shows a confirmation page with a button; only the POST (button press)
// skips the wait. Email security scanners routinely GET links in emails, so a
// GET must never change anything.

//go:embed next.html
var nextHTML string

var (
	nextTpl    = template.Must(template.New("next").Parse(nextHTML))
	reUUID     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	nextStates = map[string]nextPage{
		"ready": {
			Title:   "Want the next email now?",
			Message: "Skip the wait and we'll send you the next email in this series right away.",
			Button:  "Send me the next email now",
		},
		"sent": {
			Title:   "It's on its way",
			Message: "The next email will arrive in your inbox within a minute or two.",
		},
		"already": {
			Title:   "Already sent",
			Message: "You've already received the email that comes after this one. Check your inbox.",
		},
		"last": {
			Title:   "That was the last one",
			Message: "This was the final email in the series. Thanks for reading!",
		},
		"inactive": {
			Title:   "This series has ended",
			Message: "This email series is no longer active for you.",
		},
		"invalid": {
			Title:   "Link not valid",
			Message: "This link isn't valid. It may be incomplete; try clicking it again from the email.",
		},
	}
)

type nextPage struct {
	Title   string
	Message string
	Button  string
	Action  string
}

// nextState is what a "next now" link points at.
type nextState struct {
	SendID         int64       `db:"send_id"`
	EnrollmentID   int64       `db:"enrollment_id"`
	EnrStatus      string      `db:"enr_status"`
	NextStep       int         `db:"next_step"`
	SeqStatus      string      `db:"seq_status"`
	Position       null.Int    `db:"position"`
	TotalSteps     int         `db:"total_steps"`
	SkipLandingURL null.String `db:"skip_landing_url"`
	SubscriberID   int         `db:"subscriber_id"`
	SequenceID     int         `db:"sequence_id"`
}

// RegisterPublic registers the public (unauthenticated) routes.
func (p *Plus) RegisterPublic(g *echo.Group) {
	g.GET("/plus/seq/next/:uuid", p.handleNextPage)
	g.POST("/plus/seq/next/:uuid", p.handleNextSubmit)
	g.GET("/plus/seq/unsubscribe/:uuid", p.handleUnsubPage)
	g.POST("/plus/seq/unsubscribe/:uuid", p.handleUnsubSubmit)
}

func (p *Plus) loadNextState(uuid string) (nextState, string, error) {
	var s nextState
	if !reUUID.MatchString(uuid) {
		return s, "invalid", nil
	}
	err := p.db.Get(&s, `
		SELECT sd.id AS send_id, e.id AS enrollment_id, e.status AS enr_status, e.next_step,
		       sq.status AS seq_status, st.position, st.skip_landing_url,
		       (SELECT COUNT(*) FROM plus_sequence_steps WHERE sequence_id = e.sequence_id) AS total_steps,
		       e.subscriber_id, e.sequence_id
		FROM plus_sends sd
		JOIN plus_enrollments e ON e.id = sd.enrollment_id
		JOIN plus_sequences sq ON sq.id = e.sequence_id
		LEFT JOIN plus_sequence_steps st ON st.id = sd.step_id
		WHERE sd.uuid = $1`, uuid)
	if errors.Is(err, sql.ErrNoRows) {
		return s, "invalid", nil
	}
	if err != nil {
		return s, "", err
	}

	switch {
	case !s.Position.Valid:
		return s, "inactive", nil // the step was deleted
	case int(s.Position.Int) >= s.TotalSteps:
		return s, "last", nil
	case s.EnrStatus != EnrActive || s.SeqStatus != SeqActive:
		return s, "inactive", nil
	case int(s.Position.Int) < s.NextStep-1:
		return s, "already", nil // a later email has been sent since
	}
	return s, "ready", nil
}

func (p *Plus) handleNextPage(c *echo.Context) error {
	_, state, err := p.loadNextState(c.Param("uuid"))
	if err != nil {
		p.log.Printf("listmonk-plus: next-now lookup: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "error")
	}
	pg := nextStates[state]
	if state == "ready" {
		pg.Action = c.Request().URL.Path
	}
	return p.renderNext(c, state, pg)
}

func (p *Plus) handleNextSubmit(c *echo.Context) error {
	s, state, err := p.loadNextState(c.Param("uuid"))
	if err != nil {
		p.log.Printf("listmonk-plus: next-now lookup: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "error")
	}
	if state != "ready" {
		return p.renderNext(c, state, nextStates[state])
	}

	// Skip only the current wait (the guard on next_step makes double clicks and
	// races harmless).
	if err := p.inTx(func(tx *sqlx.Tx) error {
		if _, err := tx.Exec(`
			UPDATE plus_enrollments SET next_send_at = LEAST(next_send_at, NOW()), updated_at = NOW()
			WHERE id = $1 AND status = 'active' AND next_step = $2`, s.EnrollmentID, int(s.Position.Int)+1); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE plus_sends SET skip_clicked_at = COALESCE(skip_clicked_at, NOW()) WHERE id = $1`,
			s.SendID); err != nil {
			return err
		}
		// Future trigger-engine event.
		_, err := tx.Exec(`
			INSERT INTO plus_events (type, subscriber_id, list_id, payload, processed_at)
			SELECT 'sequence.next_clicked', $1, sq.list_id,
			       jsonb_build_object('sequence_id', sq.id, 'enrollment_id', $3::BIGINT, 'step', $4::INT), NOW()
			FROM plus_sequences sq WHERE sq.id = $2`,
			s.SubscriberID, s.SequenceID, s.EnrollmentID, int(s.Position.Int))
		return err
	}); err != nil {
		p.log.Printf("listmonk-plus: next-now update: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "error")
	}
	p.Wake()

	if s.SkipLandingURL.Valid && s.SkipLandingURL.String != "" {
		return c.Redirect(http.StatusSeeOther, s.SkipLandingURL.String)
	}
	return p.renderNext(c, "sent", nextStates["sent"])
}

func (p *Plus) renderNext(c *echo.Context, state string, pg nextPage) error {
	code := http.StatusOK
	if state == "invalid" {
		code = http.StatusNotFound
	}
	var b bytes.Buffer
	if err := nextTpl.Execute(&b, pg); err != nil {
		return err
	}
	c.Response().Header().Set("X-Robots-Tag", "noindex")
	return c.HTMLBlob(code, b.Bytes())
}
