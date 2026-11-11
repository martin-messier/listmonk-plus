package main

// listmonk-plus: admin HTML views for sequences. This file is ours (not an
// upstream file); it lives in package main to reuse Listmonk's admin view
// helpers (newAdminView, adminPage, permissions).

import (
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/knadh/listmonk/internal/plus"
	"github.com/labstack/echo/v5"
)

type plusSequencesView struct {
	adminView
	Sequences []plus.Sequence
}

type plusSequenceView struct {
	adminView
	IsNew    bool
	Tab      string
	Sequence plus.Sequence
	Options  plus.FormOptions

	// Activity tab.
	Enrollments []plus.Enrollment
	Status      string
	Page        int
	PerPage     int
	Total       int
}

// HasPrev / HasNext drive the activity tab's pagination.
func (v plusSequenceView) HasPrev() bool { return v.Page > 1 }
func (v plusSequenceView) HasNext() bool { return v.Page*v.PerPage < v.Total }

// registerPlusViews registers the sequences admin pages.
func (a *App) registerPlusViews(g *echo.Group, pm func(echo.HandlerFunc, ...string) echo.HandlerFunc) {
	get, manage := plus.PermSequencesGet, plus.PermSequencesManage
	base := path.Join(uriAdmin, "/plus/sequences")

	g.GET(base, a.adminPage(pm(a.ViewPlusSequences, get)))
	g.GET(base+"/new", a.adminPage(pm(a.ViewPlusNewSequence, manage)))
	g.GET(base+"/:id", a.adminPage(pm(a.ViewPlusSequence, get)))
	g.GET(base+"/:id/settings", a.adminPage(pm(a.ViewPlusSequence, get)))
	g.GET(base+"/:id/activity", a.adminPage(pm(a.ViewPlusSequence, get)))
}

// ViewPlusSequences renders the sequences list.
func (a *App) ViewPlusSequences(c *echo.Context) error {
	seqs, err := a.plus.Sequences()
	if err != nil {
		return err
	}
	return c.Render(http.StatusOK, "plus-sequences", plusSequencesView{
		adminView: newAdminView(c, "Sequences", "", "sequences.all"),
		Sequences: seqs,
	})
}

// ViewPlusNewSequence renders the new-sequence form.
func (a *App) ViewPlusNewSequence(c *echo.Context) error {
	opts, err := a.plus.Options()
	if err != nil {
		return err
	}
	return c.Render(http.StatusOK, "plus-sequence", plusSequenceView{
		adminView: newAdminView(c, "New sequence / Sequences", "", "sequences.new"),
		IsNew:     true,
		Tab:       "settings",
		Options:   opts,
	})
}

// ViewPlusSequence renders a sequence's steps, settings or activity tab.
func (a *App) ViewPlusSequence(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid sequence id")
	}
	seq, err := a.plus.Sequence(id)
	if err != nil {
		return err
	}
	opts, err := a.plus.Options()
	if err != nil {
		return err
	}

	tab := "steps"
	switch {
	case strings.HasSuffix(c.Path(), "/settings"):
		tab = "settings"
	case strings.HasSuffix(c.Path(), "/activity"):
		tab = "activity"
	}

	v := plusSequenceView{
		adminView: newAdminView(c, seq.Name+" / Sequences", "", "sequences.all"),
		Tab:       tab,
		Sequence:  seq,
		Options:   opts,
	}

	if tab == "activity" {
		v.Status = c.QueryParam("status")
		v.Page, _ = strconv.Atoi(c.QueryParam("page"))
		if v.Page < 1 {
			v.Page = 1
		}
		v.PerPage = 50
		v.Enrollments, v.Total, err = a.plus.Enrollments(id, v.Status, v.Page, v.PerPage)
		if err != nil {
			return err
		}
	}

	return c.Render(http.StatusOK, "plus-sequence", v)
}
