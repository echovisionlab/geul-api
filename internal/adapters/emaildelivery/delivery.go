package emaildeliveryadapter

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

type CampaignDeliveryStore struct {
	db      *gorm.DB
	audit   domainaudit.Appender
	metrics campaign.CampaignDeliveryMetrics
}

func NewCampaignDeliveryStore(
	db *gorm.DB,
	audit domainaudit.Appender,
	metrics campaign.CampaignDeliveryMetrics,
) *CampaignDeliveryStore {
	return &CampaignDeliveryStore{db: db, audit: audit, metrics: metrics}
}

func (s *CampaignDeliveryStore) NeedsDelivery(ctx context.Context, recipientID string) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("campaign delivery database is required")
	}
	return campaign.CampaignDeliveryRecipientNeedsDelivery(ctx, s.db, recipientID)
}

func (s *CampaignDeliveryStore) MarkResult(
	ctx context.Context,
	recipientID string,
	status string,
	providerMessageID string,
	errorType string,
) error {
	if s == nil || s.db == nil || s.audit == nil || s.metrics == nil {
		return fmt.Errorf("campaign delivery persistence dependencies are required")
	}
	return campaign.MarkCampaignDeliveryRecipientResultWithAudit(
		ctx, s.db, s.audit, recipientID, status, providerMessageID, errorType, s.metrics,
	)
}

func (s *SuppressionStore) Find(ctx context.Context, recipient string) (*emaildelivery.Suppression, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("email suppression database is required")
	}
	row, err := emaildelivery.GetActiveEmailSuppression(ctx, s.db, recipient)
	if err != nil || row == nil {
		return nil, err
	}
	return &emaildelivery.Suppression{Reason: row.Reason}, nil
}

func (s *SuppressionStore) Lookup(ctx context.Context, recipients []string) (map[string]emaildelivery.Suppression, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("email suppression database is required")
	}
	rows, err := emaildelivery.LookupActiveEmailSuppressionsByEmail(ctx, s.db, recipients)
	if err != nil {
		return nil, err
	}
	result := make(map[string]emaildelivery.Suppression, len(rows))
	for address, row := range rows {
		result[address] = emaildelivery.Suppression{Reason: row.Reason}
	}
	return result, nil
}

type BulkCampaignStore struct {
	db                 *gorm.DB
	spiceDB            *auth.SpiceDBClient
	audit              domainaudit.Appender
	metrics            campaign.CampaignDeliveryMetrics
	tokenSigningSecret string
	siteOrigin         string
}

func NewBulkCampaignStore(
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	audit domainaudit.Appender,
	metrics campaign.CampaignDeliveryMetrics,
	tokenSigningSecret string,
	siteOrigin string,
) *BulkCampaignStore {
	return &BulkCampaignStore{
		db: db, spiceDB: spiceDB, audit: audit, metrics: metrics,
		tokenSigningSecret: tokenSigningSecret, siteOrigin: siteOrigin,
	}
}

func (s *BulkCampaignStore) Materialize(ctx context.Context, runID string) error {
	if s == nil || s.db == nil || s.spiceDB == nil {
		return fmt.Errorf("bulk campaign materialization dependencies are required")
	}
	return campaign.MaterializeCampaignDeliveryRun(ctx, s.db, s.spiceDB, runID)
}

func (s *BulkCampaignStore) ListPending(
	ctx context.Context,
	runID string,
	afterID string,
	batchSize int,
) ([]emaildelivery.BulkDelivery, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("bulk campaign delivery database is required")
	}
	jobs, err := campaign.ListPendingCampaignDeliveryRecipients(ctx, s.db, runID, afterID, batchSize)
	if err != nil {
		return nil, err
	}
	result := make([]emaildelivery.BulkDelivery, 0, len(jobs))
	for _, job := range jobs {
		command, _ := s.BuildCommand(job)
		result = append(result, emaildelivery.BulkDelivery{
			RecipientID: job.Recipient.ID, RecipientEmail: job.Recipient.RecipientEmail,
			TemplateType: campaign.EmailDeliveryRunTemplateType(job.Run), Command: command,
		})
	}
	return result, nil
}

func (s *BulkCampaignStore) MarkResult(
	ctx context.Context,
	recipientID string,
	status string,
	errorType string,
) error {
	if s == nil || s.db == nil || s.audit == nil || s.metrics == nil {
		return fmt.Errorf("bulk campaign delivery persistence dependencies are required")
	}
	return campaign.MarkCampaignDeliveryRecipientResultWithAudit(
		ctx, s.db, s.audit, recipientID, status, "", errorType, s.metrics,
	)
}

func (s *BulkCampaignStore) BuildCommand(
	delivery campaign.CampaignDeliveryRecipientJob,
) (*managev1.SendEmailEvent, bool) {
	recipient := delivery.Recipient
	run := delivery.Run
	templateType := campaign.EmailDeliveryRunTemplateType(run)
	referenceID := campaign.EmailDeliveryRunReferenceID(run)
	templateData, err := campaign.CampaignDeliveryRunTemplateData(run)
	if err != nil {
		return nil, false
	}
	if _, persisted := templateData["recipient_email"]; persisted {
		return nil, false
	}
	templateData["recipient_email"] = recipient.RecipientEmail

	contextKind := strings.TrimSpace(recipient.RecipientContextType)
	switch contextKind {
	case campaign.BulkEmailContextNewsletterSubscription, campaign.BulkEmailContextAccountCurrent:
		if recipient.IdentityID == nil || strings.TrimSpace(*recipient.IdentityID) == "" ||
			recipient.MemberID == nil || strings.TrimSpace(*recipient.MemberID) == "" {
			return nil, false
		}
		if strings.TrimSpace(s.tokenSigningSecret) != "" {
			token := crypto.GenerateSignedToken(
				member.NewsletterUnsubscribeTokenID(*recipient.IdentityID),
				crypto.PurposeUnsubscribe, time.Time{}, s.tokenSigningSecret,
			)
			templateData["unsubscribe_link"] = fmt.Sprintf(
				"%s/unsubscribe?token=%s", s.siteOrigin, token,
			)
		}
	default:
		return nil, false
	}

	deliveryRecipientID := recipient.ID
	command := &managev1.SendEmailEvent{
		Recipient: recipient.RecipientEmail, TemplateType: templateType,
		TemplateData: templateData, ReferenceId: &referenceID,
		DeliveryRecipientId: &deliveryRecipientID, MessageId: &deliveryRecipientID,
	}
	if recipient.Locale != nil {
		command.Locale = recipient.Locale
	}
	switch contextKind {
	case campaign.BulkEmailContextNewsletterSubscription:
		command.RecipientContext = email.NewsletterSubscriptionContext(*recipient.IdentityID, *recipient.MemberID)
	case campaign.BulkEmailContextAccountCurrent:
		command.RecipientContext = email.AccountSelectedPrimaryEmailContext(*recipient.IdentityID)
	}
	return command, command.GetRecipientContext() != nil
}

type CampaignEmailRenderer interface {
	RenderCampaignEmail(context.Context, *gorm.DB, string, string, map[string]string) (*email.RenderedEmail, error)
}

type Renderer struct {
	db               *gorm.DB
	cdnURL           string
	siteOrigin       string
	campaignRenderer CampaignEmailRenderer
}

func NewRenderer(
	db *gorm.DB,
	cdnURL string,
	siteOrigin string,
	campaignRenderer CampaignEmailRenderer,
) *Renderer {
	return &Renderer{
		db: db, cdnURL: cdnURL, siteOrigin: siteOrigin,
		campaignRenderer: campaignRenderer,
	}
}

func (r *Renderer) Render(
	ctx context.Context,
	job *managev1.SendEmailEvent,
) (*email.RenderedEmail, error) {
	targetLocale := ""
	if job.GetLocale() != "" {
		if normalized := localization.NormalizeSupportedLocale(strings.TrimSpace(job.GetLocale())); normalized != nil {
			targetLocale = *normalized
		}
	}
	if targetLocale == "" {
		targetLocale = emaildelivery.LookupEmailRecipientLocale(ctx, r.db, job.GetRecipient())
	}
	if err := emailauthoring.ValidateAutomaticEmailTemplateData(job.GetTemplateType(), job.GetTemplateData()); err != nil {
		return nil, err
	}
	templateData := emaildelivery.BuildEmailRenderData(
		ctx, r.db, r.cdnURL, r.siteOrigin, targetLocale, job.GetTemplateData(),
	)
	return r.renderTemplate(ctx, job, targetLocale, templateData)
}

func (r *Renderer) RenderResolved(
	ctx context.Context,
	job *managev1.SendEmailEvent,
	targetLocale string,
	templateData map[string]string,
) (*email.RenderedEmail, error) {
	return r.renderTemplate(ctx, job, targetLocale, templateData)
}

func (r *Renderer) renderTemplate(
	ctx context.Context,
	job *managev1.SendEmailEvent,
	targetLocale string,
	templateData map[string]string,
) (*email.RenderedEmail, error) {
	if strings.TrimSpace(job.GetDeliveryRecipientId()) == "" {
		if campaignID, ok := strings.CutPrefix(strings.TrimSpace(job.GetTemplateType()), "campaign:"); ok {
			campaignID = strings.TrimSpace(campaignID)
			if campaignID == "" {
				return nil, fmt.Errorf("campaign template identity is required")
			}
			if r.campaignRenderer == nil {
				return nil, fmt.Errorf("campaign email renderer is required")
			}
			return r.campaignRenderer.RenderCampaignEmail(ctx, r.db, campaignID, targetLocale, templateData)
		}
		return email.RenderTemplateForLocale(ctx, r.db, job.GetTemplateType(), targetLocale, templateData)
	}

	var recipient model.CampaignDeliveryRecipient
	if err := r.db.WithContext(ctx).Select("run_id").First(&recipient, "id = ?", job.GetDeliveryRecipientId()).Error; err != nil {
		return nil, err
	}
	var run model.CampaignDeliveryRun
	if err := r.db.WithContext(ctx).
		Select("run_kind", "template_event_key", "template_data", "render_snapshot", "snapshot_schema_version", "definition_sealed").
		First(&run, "id = ?", recipient.RunID).Error; err != nil {
		return nil, err
	}
	if err := campaign.ValidateEmailDeliveryRenderDefinition(run); err != nil {
		return nil, fmt.Errorf("invalid durable email delivery definition: %w", err)
	}
	rendered, err := email.RenderCampaignSnapshotForLocale(run.RenderSnapshot, targetLocale, templateData)
	if err != nil {
		return nil, fmt.Errorf("render durable email delivery snapshot: %w", err)
	}
	if rendered == nil || strings.TrimSpace(rendered.Subject) == "" || strings.TrimSpace(rendered.HTML) == "" {
		return nil, fmt.Errorf("durable email delivery snapshot produced incomplete content")
	}
	return rendered, nil
}
