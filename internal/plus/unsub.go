package plus

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v5"
)

// Unsubscribe from a sequence email (design §8).
//
// The link ({{ SequenceUnsubscribeURL }}) and the List-Unsubscribe header both
// point here. It unsubscribes the person from the sequence's list only, like a
// campaign's unsubscribe link does for the campaign's lists. The status change
// fires the subscriber_lists trigger, which cancels their sequences on that list.
//
// GET shows a confirmation button (email scanners pre-fetch links, so a GET must
// not unsubscribe anyone). POST unsubscribes: that's the button, and also the
// RFC 8058 one-click request mail providers send (body
// "List-Unsubscribe=One-Click"), which must work without any confirmation.

type unsubState struct {
	SubscriberID int    `db:"subscriber_id"`
	ListID       int    `db:"list_id"`
	ListName     string `db:"list_name"`
	SubStatus    string `db:"sub_status"`
}

func (p *Plus) loadUnsubState(uuid string) (unsubState, bool, error) {
	var s unsubState
	if !reUUID.MatchString(uuid) {
		return s, false, nil
	}
	err := p.db.Get(&s, `
		SELECT sd.subscriber_id, sq.list_id, l.name AS list_name, COALESCE(sl.status::TEXT, 'removed') AS sub_status
		FROM plus_sends sd
		JOIN plus_enrollments e ON e.id = sd.enrollment_id
		JOIN plus_sequences sq ON sq.id = e.sequence_id
		JOIN lists l ON l.id = sq.list_id
		LEFT JOIN subscriber_lists sl ON sl.subscriber_id = sd.subscriber_id AND sl.list_id = sq.list_id
		WHERE sd.uuid = $1`, uuid)
	if errors.Is(err, sql.ErrNoRows) {
		return s, false, nil
	}
	return s, err == nil, err
}

func (p *Plus) handleUnsubPage(c *echo.Context) error {
	s, ok, err := p.loadUnsubState(c.Param("uuid"))
	if err != nil {
		p.log.Printf("listmonk-plus: unsubscribe lookup: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "error")
	}
	if !ok {
		return p.renderNext(c, "invalid", nextStates["invalid"])
	}
	if s.SubStatus == "unsubscribed" || s.SubStatus == "removed" {
		return p.renderNext(c, "done", unsubDonePage(s.ListName))
	}
	return p.renderNext(c, "ready", nextPage{
		Title:   "Unsubscribe?",
		Message: fmt.Sprintf("You'll stop receiving emails from \"%s\".", s.ListName),
		Button:  "Unsubscribe",
		Action:  c.Request().URL.Path,
	})
}

func (p *Plus) handleUnsubSubmit(c *echo.Context) error {
	s, ok, err := p.loadUnsubState(c.Param("uuid"))
	if err != nil {
		p.log.Printf("listmonk-plus: unsubscribe lookup: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "error")
	}
	if !ok {
		return p.renderNext(c, "invalid", nextStates["invalid"])
	}
	if _, err := p.db.Exec(`
		UPDATE subscriber_lists SET status = 'unsubscribed', updated_at = NOW()
		WHERE subscriber_id = $1 AND list_id = $2 AND status <> 'unsubscribed'`, s.SubscriberID, s.ListID); err != nil {
		p.log.Printf("listmonk-plus: unsubscribe: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "error")
	}
	p.Wake() // cancel their enrollments right away
	return p.renderNext(c, "done", unsubDonePage(s.ListName))
}

func unsubDonePage(listName string) nextPage {
	return nextPage{
		Title:   "You're unsubscribed",
		Message: fmt.Sprintf("You won't receive any more emails from \"%s\".", listName),
	}
}
