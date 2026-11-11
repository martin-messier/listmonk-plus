package plus

// Data accessors for the admin HTML views (cmd/plus_views.go). The views live
// in package main because they reuse Listmonk's admin view helpers.

// ListOption is a list in the sequence editor's list picker.
type ListOption struct {
	ID    int    `db:"id" json:"id"`
	Name  string `db:"name" json:"name"`
	Optin string `db:"optin" json:"optin"`
}

// TemplateOption is a tx template in the step editor's template picker.
type TemplateOption struct {
	ID   int    `db:"id" json:"id"`
	Name string `db:"name" json:"name"`

	// Whether the template uses {{ SequenceUnsubscribeURL }} / {{ SequenceNextURL }}.
	HasUnsub bool `db:"has_unsub" json:"has_unsub"`
	HasNext  bool `db:"has_next" json:"has_next"`
}

// FormOptions holds the choices for the sequence and step editors.
type FormOptions struct {
	Lists     []ListOption     `json:"lists"`
	Templates []TemplateOption `json:"templates"`
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

// Options returns the lists and tx templates for the editors.
func (p *Plus) Options() (FormOptions, error) {
	out := FormOptions{Lists: []ListOption{}, Templates: []TemplateOption{}}
	if err := p.db.Select(&out.Lists, `SELECT id, name, optin FROM lists WHERE status = 'active' ORDER BY LOWER(name)`); err != nil {
		return out, err
	}
	if err := p.db.Select(&out.Templates, `
		SELECT id, name,
		       body ~ '\{\{-?\s*SequenceUnsubscribeURL\s*-?\}\}' AS has_unsub,
		       body ~ '\{\{-?\s*SequenceNextURL\s*-?\}\}' AS has_next
		FROM templates WHERE type = 'tx' ORDER BY LOWER(name)`); err != nil {
		return out, err
	}
	return out, nil
}
