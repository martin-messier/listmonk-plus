package plus

import (
	"fmt"

	"github.com/knadh/listmonk/models"
)

// campaignCache holds campaigns compiled during one worker pass, so a batch of
// sends of the same step compiles its campaign once. It's discarded after the
// pass, so edits to a step's campaign (or its template) apply from the next pass.
type campaignCache map[int]*models.Campaign

// sendCampaign renders a step's campaign for one subscriber and queues it,
// exactly like Listmonk's "Send test" (cmd/campaigns.go sendTestMessage): same
// template functions ({{ UnsubscribeURL }}, {{ TrackView }}, {{ TrackLink }},
// {{ SequenceNextURL }}), same headers, same rate-limited campaign queue.
func (p *Plus) sendCampaign(cache campaignCache, campaignID, subID int) error {
	camp, ok := cache[campaignID]
	if !ok {
		c, err := p.opt.Core.GetCampaignForPreview(campaignID, 0)
		if err != nil {
			return fmt.Errorf("loading campaign %d: %v", campaignID, err)
		}
		if err := p.opt.Manager.LoadInlineImages(&c); err != nil {
			return fmt.Errorf("loading inline images of campaign %d: %v", campaignID, err)
		}
		if err := c.CompileTemplate(p.opt.Manager.TemplateFuncs(&c)); err != nil {
			return fmt.Errorf("compiling campaign %d: %v", campaignID, err)
		}
		camp = &c
		cache[campaignID] = camp
	}

	sub, err := p.opt.Core.GetSubscriber(subID, "", "")
	if err != nil {
		return fmt.Errorf("loading subscriber: %v", err)
	}

	msg, err := p.opt.Manager.NewCampaignMessage(camp, sub)
	if err != nil {
		return fmt.Errorf("rendering campaign %d: %v", campaignID, err)
	}
	if err := p.opt.Manager.PushCampaignMessage(msg); err != nil {
		return err
	}

	// Count it in the campaign's "Sent" (shown on the campaign's View tab).
	if _, err := p.db.Exec(`UPDATE campaigns SET sent = sent + 1 WHERE id = $1`, campaignID); err != nil {
		p.log.Printf("listmonk-plus: error counting send for campaign %d: %v", campaignID, err)
	}
	return nil
}
