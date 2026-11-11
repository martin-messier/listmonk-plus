package plus

import (
	"fmt"
	"maps"
	"net/textproto"

	"github.com/knadh/listmonk/internal/messenger/email"
	"github.com/knadh/listmonk/models"
	null "gopkg.in/volatiletech/null.v6"
)

// sendStep renders a step's tx template for a subscriber and hands the message
// to Listmonk's mailer queue, exactly like /api/tx does (cmd/tx.go), but with
// the real implementations of the listmonk-plus template functions.
func (p *Plus) sendStep(subID, templateID int, subject null.String, from, sendUUID string) error {
	sub, err := p.opt.Core.GetSubscriber(subID, "", "")
	if err != nil {
		return fmt.Errorf("loading subscriber: %v", err)
	}

	cached, err := p.opt.Manager.GetTpl(templateID)
	if err != nil {
		return fmt.Errorf("template %d is not a cached transactional template: %v", templateID, err)
	}

	// Per-message functions. The cached template was compiled with placeholders
	// (see tplfuncs), so compile a copy with the real ones.
	nextURL, unsubURL := p.nextURL(sendUUID), p.unsubURL(sendUUID)
	funcs := maps.Clone(p.opt.Manager.GenericTemplateFuncs())
	funcs["SequenceNextURL"] = func() string { return nextURL }
	funcs["SequenceUnsubscribeURL"] = func() string { return unsubURL }

	tpl := *cached
	if err := tpl.Compile(funcs); err != nil {
		return fmt.Errorf("compiling template %d: %v", templateID, err)
	}

	m := models.TxMessage{
		Subject:     subject.String, // empty = use the template's subject
		ContentType: "html",
		FromEmail:   from,
		Messenger:   email.MessengerName,
	}
	if err := m.Render(sub, &tpl, funcs); err != nil {
		return fmt.Errorf("rendering template %d: %v", templateID, err)
	}

	msg := models.Message{
		Subscriber:  sub,
		To:          []string{sub.Email},
		From:        m.FromEmail,
		Subject:     m.Subject,
		ContentType: m.ContentType,
		Messenger:   m.Messenger,
		Body:        m.Body,
		AltBody:     []byte(m.AltBody),
		Attachments: append([]models.Attachment(nil), cached.Attachments...),

		// One-click unsubscribe (RFC 8058), required by Gmail and Yahoo for
		// bulk senders. Mail clients POST to this URL; see unsub.go.
		Headers: textproto.MIMEHeader{
			"List-Unsubscribe":      {"<" + unsubURL + ">"},
			"List-Unsubscribe-Post": {"List-Unsubscribe=One-Click"},
		},
	}
	return p.opt.Manager.PushMessage(msg)
}

func (p *Plus) nextURL(sendUUID string) string {
	return p.opt.RootURL + "/plus/seq/next/" + sendUUID
}

func (p *Plus) unsubURL(sendUUID string) string {
	return p.opt.RootURL + "/plus/seq/unsubscribe/" + sendUUID
}
