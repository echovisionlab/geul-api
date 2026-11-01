package worker

import (
	"context"

	"gorm.io/gorm"

	emaildeliveryadapter "github.com/echovisionlab/geul-api/internal/adapters/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/config"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// emailSeams composes the email delivery adapters workerpostgres.New wires
// into the worker, for the tests of those adapters that still live here
// rather than with Email Delivery.
type emailSeams struct {
	db            *gorm.DB
	kratosClient  auth.IdentityManager
	config        *config.Config
	publisher     emaildelivery.EmailCommandPublisher
	campaignEmail emaildeliveryadapter.CampaignEmailRenderer
}

func (s *emailSeams) settings() (siteOrigin, cdnURL, tokenSigningSecret string) {
	if s.config == nil {
		return "", "", ""
	}
	return s.config.SiteOrigin, s.config.CDNURL, s.config.TokenSigningSecret
}

func (s *emailSeams) bulkCampaignStore() *emaildeliveryadapter.BulkCampaignStore {
	siteOrigin, _, tokenSigningSecret := s.settings()
	return emaildeliveryadapter.NewBulkCampaignStore(
		s.db, nil, nil, emaildeliveryadapter.Metrics{}, tokenSigningSecret, siteOrigin,
	)
}

func (s *emailSeams) handleSendBulkEmailBatch(ctx context.Context, job *managev1.SendBulkEmailBatchEvent) error {
	return emaildelivery.NewBulkApplication(
		s.bulkCampaignStore(), emaildeliveryadapter.NewSuppressionStore(s.db), s.publisher, emaildeliveryadapter.Metrics{},
	).Handle(ctx, job)
}

func (s *emailSeams) buildMaterializedCampaignEmailJob(
	delivery campaign.CampaignDeliveryRecipientJob,
) (*managev1.SendEmailEvent, bool) {
	return s.bulkCampaignStore().BuildCommand(delivery)
}

// enforceEmailRecipientContext reports whether the recipient policy blocks
// job.
func (s *emailSeams) enforceEmailRecipientContext(
	ctx context.Context,
	job *managev1.SendEmailEvent,
) (bool, error) {
	decision, err := emaildeliveryadapter.NewRecipientPolicy(s.db, s.kratosClient).Authorize(ctx, job)
	if err != nil {
		return false, err
	}
	return decision.Blocked, nil
}

func (s *emailSeams) renderEmailJobTemplate(
	ctx context.Context,
	job *managev1.SendEmailEvent,
	targetLocale string,
	templateData map[string]string,
) (*email.RenderedEmail, error) {
	siteOrigin, cdnURL, _ := s.settings()
	return emaildeliveryadapter.NewRenderer(s.db, cdnURL, siteOrigin, s.campaignEmail).
		RenderResolved(ctx, job, targetLocale, templateData)
}

type emailAdapterFailure struct {
	err       error
	errorType string
}

func classifyEmailDeliveryFailure(
	failures []emailAdapterFailure,
	fallback error,
) (string, string, error) {
	domainFailures := make([]emaildelivery.ProviderFailure, 0, len(failures))
	for _, failure := range failures {
		domainFailures = append(domainFailures, emaildelivery.ProviderFailure{
			Err: failure.err, ErrorType: failure.errorType,
		})
	}
	return emaildelivery.ClassifyProviderFailures(domainFailures, fallback)
}

func deliveryStatusForEmailError(errorType string) string {
	return emaildelivery.StatusForProviderError(errorType)
}

func emailDeliveryErrorDecision(err error) (string, bool) {
	return emaildelivery.ProviderErrorDecision(err)
}
