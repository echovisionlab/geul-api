package worker

import (
	"context"

	emaildeliveryadapter "github.com/echovisionlab/geul-api/internal/adapters/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (h *Handlers) handleSendBulkEmailBatch(
	ctx context.Context,
	job *managev1.SendBulkEmailBatchEvent,
) error {
	return h.bulkEmailApplication().Handle(ctx, job)
}

func (h *Handlers) bulkEmailApplication() *emaildelivery.BulkApplication {
	store := h.bulkCampaignStore()
	return emaildelivery.NewBulkApplication(
		store,
		emaildeliveryadapter.NewSuppressionStore(h.db),
		h.publisher,
		h.mailMetrics,
	)
}

func (h *Handlers) bulkCampaignStore() *emaildeliveryadapter.BulkCampaignStore {
	tokenSigningSecret := ""
	siteOrigin := ""
	if h.config != nil {
		tokenSigningSecret = h.config.TokenSigningSecret
		siteOrigin = h.config.SiteOrigin
	}
	return emaildeliveryadapter.NewBulkCampaignStore(
		h.db, h.spicedbClient, h.auditWriter, h.mailMetrics,
		tokenSigningSecret, siteOrigin,
	)
}
