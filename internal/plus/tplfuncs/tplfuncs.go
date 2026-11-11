// Package tplfuncs declares the template functions that listmonk-plus adds to
// email templates. It is a leaf package (no listmonk imports) so that upstream's
// manager package can register the placeholders without an import cycle.
package tplfuncs

import "html/template"

// Placeholders returns no-op versions of the listmonk-plus template functions.
// They are registered in Listmonk's generic template function map so that
// templates using them can be saved, previewed and sent outside a sequence
// (where they render as empty strings). When a sequence step is sent, plus
// recompiles the template with the real implementations.
func Placeholders() template.FuncMap {
	return template.FuncMap{
		// URL of the "send me the next email now" page for this email.
		"SequenceNextURL": func() string { return "" },

		// URL of the page that unsubscribes the person from the sequence's list.
		"SequenceUnsubscribeURL": func() string { return "" },
	}
}
