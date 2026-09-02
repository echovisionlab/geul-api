package worker

import (
	emaildeliveryadapter "github.com/echovisionlab/geul-api/internal/adapters/emaildelivery"
)

func (h *Handlers) emailDeliveryRenderer() *emaildeliveryadapter.Renderer {
	cdnURL := ""
	siteOrigin := ""
	if h.config != nil {
		cdnURL = h.config.CDNURL
		siteOrigin = h.config.SiteOrigin
	}
	return emaildeliveryadapter.NewRenderer(h.db, cdnURL, siteOrigin, h.campaignEmail)
}
