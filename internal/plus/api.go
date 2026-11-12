package plus

import (
	"net/http"
	"strconv"

	"github.com/labstack/echo/v5"
)

// PermFunc wraps a handler with a permission check (Listmonk's auth.Perm).
type PermFunc func(next echo.HandlerFunc, perms ...string) echo.HandlerFunc

type okResp struct {
	Data any `json:"data"`
}

// RegisterAPI registers the admin API on an authenticated route group.
func (p *Plus) RegisterAPI(g *echo.Group, pm PermFunc) {
	get, manage := PermSequencesGet, PermSequencesManage

	g.GET("/api/plus/sequences", pm(p.handleGetSequences, get))
	g.POST("/api/plus/sequences", pm(p.handleCreateSequence, manage))
	g.GET("/api/plus/sequences/:id", pm(p.handleGetSequence, get))
	g.PUT("/api/plus/sequences/:id", pm(p.handleUpdateSequence, manage))
	g.DELETE("/api/plus/sequences/:id", pm(p.handleDeleteSequence, manage))

	g.POST("/api/plus/sequences/:id/steps", pm(p.handleAddStep, manage))
	g.PUT("/api/plus/sequences/:id/steps/:stepID", pm(p.handleUpdateStep, manage))
	g.DELETE("/api/plus/sequences/:id/steps/:stepID", pm(p.handleDeleteStep, manage))

	g.GET("/api/plus/sequences/:id/enrollments", pm(p.handleGetEnrollments, get))
	// Lists subscribers' names and emails, so it also needs a subscriber read permission.
	g.GET("/api/plus/sequences/:id/candidates", pm(pm(p.handleGetCandidates, "subscribers:get_all", "subscribers:get"), manage))
	g.POST("/api/plus/sequences/:id/enrollments", pm(p.handleEnroll, manage))
	g.DELETE("/api/plus/sequences/:id/enrollments/:enrID", pm(p.handleCancelEnrollment, manage))
}

func intParam(c *echo.Context, name string) (int, error) {
	n, err := strconv.Atoi(c.Param(name))
	if err != nil || n < 1 {
		return 0, errBad("invalid %s", name)
	}
	return n, nil
}

func (p *Plus) handleGetSequences(c *echo.Context) error {
	out, err := p.getSequences()
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, okResp{out})
}

func (p *Plus) handleGetSequence(c *echo.Context) error {
	id, err := intParam(c, "id")
	if err != nil {
		return err
	}
	out, err := p.getSequence(id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, okResp{out})
}

func (p *Plus) handleCreateSequence(c *echo.Context) error {
	var in seqInput
	if err := c.Bind(&in); err != nil {
		return errBad("invalid JSON: %v", err)
	}
	out, err := p.createSequence(in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, okResp{out})
}

func (p *Plus) handleUpdateSequence(c *echo.Context) error {
	id, err := intParam(c, "id")
	if err != nil {
		return err
	}
	var in seqInput
	if err := c.Bind(&in); err != nil {
		return errBad("invalid JSON: %v", err)
	}
	out, err := p.updateSequence(id, in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, okResp{out})
}

func (p *Plus) handleDeleteSequence(c *echo.Context) error {
	id, err := intParam(c, "id")
	if err != nil {
		return err
	}
	if err := p.deleteSequence(id); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, okResp{true})
}

func (p *Plus) handleAddStep(c *echo.Context) error {
	id, err := intParam(c, "id")
	if err != nil {
		return err
	}
	var in stepInput
	if err := c.Bind(&in); err != nil {
		return errBad("invalid JSON: %v", err)
	}
	out, err := p.addStep(id, in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, okResp{out})
}

func (p *Plus) handleUpdateStep(c *echo.Context) error {
	id, err := intParam(c, "id")
	if err != nil {
		return err
	}
	stepID, err := intParam(c, "stepID")
	if err != nil {
		return err
	}
	var in stepInput
	if err := c.Bind(&in); err != nil {
		return errBad("invalid JSON: %v", err)
	}
	out, err := p.updateStep(id, stepID, in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, okResp{out})
}

func (p *Plus) handleDeleteStep(c *echo.Context) error {
	id, err := intParam(c, "id")
	if err != nil {
		return err
	}
	stepID, err := intParam(c, "stepID")
	if err != nil {
		return err
	}
	out, err := p.deleteStep(id, stepID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, okResp{out})
}

func (p *Plus) handleGetEnrollments(c *echo.Context) error {
	id, err := intParam(c, "id")
	if err != nil {
		return err
	}
	status := c.QueryParam("status")
	if status != "" && !oneOf(status, EnrActive, EnrCompleted, EnrCancelled) {
		return errBad("status must be active, completed or cancelled")
	}
	page, _ := strconv.Atoi(c.QueryParam("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(c.QueryParam("per_page"))
	if perPage < 1 || perPage > 500 {
		perPage = 50
	}
	out, total, err := p.getEnrollments(id, status, page, perPage)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, okResp{map[string]any{
		"results": out, "total": total, "page": page, "per_page": perPage,
	}})
}

func (p *Plus) handleGetCandidates(c *echo.Context) error {
	id, err := intParam(c, "id")
	if err != nil {
		return err
	}
	page, _ := strconv.Atoi(c.QueryParam("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(c.QueryParam("per_page"))
	if perPage < 1 || perPage > 500 {
		perPage = 50
	}
	excl, _ := strconv.ParseBool(c.QueryParam("exclude_other_sequences"))
	out, total, err := p.candidates(id, c.QueryParam("query"), excl, page, perPage)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, okResp{map[string]any{
		"results": out, "total": total, "page": page, "per_page": perPage,
	}})
}

func (p *Plus) handleEnroll(c *echo.Context) error {
	id, err := intParam(c, "id")
	if err != nil {
		return err
	}
	var in enrollInput
	if err := c.Bind(&in); err != nil {
		return errBad("invalid JSON: %v", err)
	}
	out, err := p.enrollManual(id, in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, okResp{out})
}

func (p *Plus) handleCancelEnrollment(c *echo.Context) error {
	id, err := intParam(c, "id")
	if err != nil {
		return err
	}
	enrID, err := strconv.ParseInt(c.Param("enrID"), 10, 64)
	if err != nil || enrID < 1 {
		return errBad("invalid enrollment id")
	}
	if err := p.cancelEnrollment(id, enrID); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, okResp{true})
}
