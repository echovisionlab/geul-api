package campaign

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/email"
	emailpostgres "github.com/echovisionlab/geul-api/internal/email/postgres"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/sitevariable"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

// emailRegex for validating email format in test campaign sends
var campaignEmailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)

// PreviewCampaignQuery renders a Campaign with sample data. A set Subject,
// Document or LayoutID previews that unsaved value instead of the stored
// one; an empty LayoutID previews without a layout.
type PreviewCampaignQuery struct {
	ID       string
	Locale   *string
	Subject  *string
	Document *contentv1.LocalizedRichTextDocument
	LayoutID *string
}

// CampaignPreview is a rendered Campaign preview.
type CampaignPreview struct {
	Subject     string
	HTMLContent string
	TextContent string
}

// PreviewCampaign renders the Campaign in the requested locale for a
// deterministic sample recipient; recipient-specific previews are not part
// of the Campaign contract.
func (s *Service) PreviewCampaign(ctx context.Context, query PreviewCampaignQuery) (CampaignPreview, error) {
	can, canErr := policyv1.Campaign.View(query.ID)
	if err := s.requireAdminCan(ctx, can, canErr); err != nil {
		return CampaignPreview{}, err
	}
	if query.ID == "" {
		return CampaignPreview{}, domainerr.Required("id")
	}
	campaign, err := s.loadCampaign(ctx, query.ID)
	if err != nil {
		return CampaignPreview{}, err
	}
	requestedLocale, err := resolveCampaignRequestedLocale(s.runtime, query.Locale)
	if err != nil {
		return CampaignPreview{}, err
	}
	sampleData := s.emailRendering.BuildRenderData(ctx, s.db, s.cdnDomain, s.siteOrigin, requestedLocale, map[string]string{
		"recipient_email":  "john@example.com",
		"unsubscribe_link": "https://example.com/unsubscribe/sample",
	})
	localizedCampaign, _, err := ResolveLocalizedCampaign(ctx, s.db, s.contentBlocks, campaign, requestedLocale)
	if err != nil {
		return CampaignPreview{}, err
	}
	if err := applyCampaignPreviewDraft(ctx, &localizedCampaign, query); err != nil {
		return CampaignPreview{}, err
	}
	subject, err := email.RenderSubjectVarsStrict(localizedCampaign.Subject, sampleData)
	if err != nil {
		return CampaignPreview{}, domainerr.InvalidField("subject", err.Error())
	}
	sampleData["subject"] = subject
	htmlContent, err := s.renderCampaignPreviewHTML(ctx, localizedCampaign, query.LayoutID, requestedLocale, sampleData)
	if err != nil {
		return CampaignPreview{}, err
	}
	return CampaignPreview{Subject: subject, HTMLContent: htmlContent, TextContent: s.emailRendering.PlainText(htmlContent)}, nil
}

// applyCampaignPreviewDraft overlays the unsaved subject and document of a
// preview on the stored Campaign.
func applyCampaignPreviewDraft(ctx context.Context, campaign *model.Campaign, query PreviewCampaignQuery) error {
	if query.Subject != nil {
		campaign.Subject = *query.Subject
	}
	if query.Document == nil {
		return nil
	}
	materialized, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, query.Document, nil, sitevariable.TemplateContext())
	if err != nil {
		return domainerr.InvalidField("document", err.Error())
	}
	campaign.ContentHTML = &materialized.HTML
	return nil
}

// renderCampaignPreviewHTML renders the Campaign's content inside the
// requested layout, the saved one when none is requested, or no layout.
func (s *Service) renderCampaignPreviewHTML(ctx context.Context, campaign model.Campaign, requestedLayoutID *string, locale string, sampleData map[string]string) (string, error) {
	htmlContent := ""
	if campaign.ContentHTML != nil {
		htmlContent = *campaign.ContentHTML
	}
	layoutID := campaign.LayoutID
	if requestedLayoutID != nil {
		layoutID = nil
		if strings.TrimSpace(*requestedLayoutID) != "" {
			layoutID = requestedLayoutID
		}
	}
	var err error
	if layoutID != nil {
		htmlContent, err = s.emailRendering.WrapWithLayout(ctx, s.db, *layoutID, locale, htmlContent, sampleData)
		if err != nil {
			return "", err
		}
	} else if htmlContent, err = email.RenderEmailHTML(htmlContent, "", sampleData); err != nil {
		return "", domainerr.InvalidField("document", err.Error())
	}
	return s.emailRendering.NormalizeHTML(htmlContent), nil
}

// SendTestCampaignCommand sends a Campaign to one address with sample
// recipient data.
type SendTestCampaignCommand struct {
	ID     string
	Email  string
	Locale *string
}

// SendTestCampaign queues a test send of the Campaign to an address that is
// ready to receive one. It changes no Campaign state and records no audit.
func (s *Service) SendTestCampaign(ctx context.Context, command SendTestCampaignCommand) error {
	access, err := adminAccess(policyv1.Campaign.Manage(command.ID))
	if err != nil {
		return err
	}
	var campaign model.Campaign
	_, err = mutation.Run(ctx, s.mutations, mutation.Spec[*managev1.SendEmailEvent]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			return lockCampaign(ctx, tx, command.ID, &campaign)
		},
		Access: access,
		Change: func(ctx context.Context, tx *gorm.DB) (*managev1.SendEmailEvent, error) {
			return s.newCampaignTestSend(ctx, tx, command)
		},
		Audit: func(*managev1.SendEmailEvent) mutation.Audit { return mutation.Unchanged() },
		Outbox: func(ctx context.Context, tx *gorm.DB, sendJob *managev1.SendEmailEvent) error {
			if err := publishCampaignDurableProtoInTransaction(ctx, s.runtime, tx, eventpkg.QueueEmailSend, sendJob.GetMessageId(), sendJob); err != nil {
				return fmt.Errorf("queue campaign test email: %w", err)
			}
			return nil
		},
	})
	return err
}

// newCampaignTestSend validates the address and returns the test send job.
func (s *Service) newCampaignTestSend(ctx context.Context, tx *gorm.DB, command SendTestCampaignCommand) (*managev1.SendEmailEvent, error) {
	if command.Email == "" {
		return nil, domainerr.Required("email")
	}
	if !campaignEmailRegex.MatchString(command.Email) {
		return nil, domainerr.InvalidField("email", "invalid format")
	}
	if err := email.CheckTestSendReadiness(ctx, emailpostgres.Bind(tx), command.Email); err != nil {
		if message := email.TestSendReadinessMessage(err); message != "" {
			return nil, domainerr.Precondition(message)
		}
		return nil, err
	}
	campaignID := command.ID
	sendJob := &managev1.SendEmailEvent{
		Recipient:    command.Email,
		TemplateType: fmt.Sprintf("campaign:%s", campaignID),
		TemplateData: map[string]string{
			"recipient_email":  command.Email,
			"unsubscribe_link": campaignPreviewUnsubscribeLink(s.siteOrigin),
		},
		ReferenceId:      &campaignID,
		RecipientContext: s.emailRendering.TestRecipientContext(campaignEmailTestActorID(ctx)),
	}
	if command.Locale != nil && strings.TrimSpace(*command.Locale) != "" {
		normalizedLocale := s.runtime.NormalizeSupportedLocale(*command.Locale)
		if normalizedLocale == nil {
			return nil, domainerr.InvalidField("locale", "unsupported locale")
		}
		sendJob.Locale = normalizedLocale
	}
	messageID := "campaign-test:" + uuid.NewString()
	sendJob.MessageId = &messageID
	return sendJob, nil
}

func campaignPreviewUnsubscribeLink(siteOrigin string) string {
	siteOrigin = strings.TrimRight(strings.TrimSpace(siteOrigin), "/")
	if siteOrigin == "" {
		return "https://example.com/unsubscribe/sample"
	}
	return siteOrigin + "/unsubscribe?preview=1"
}
func resolveCampaignRequestedLocale(runtime LocaleNormalizer, explicitLocale *string) (string, error) {
	if explicitLocale != nil && strings.TrimSpace(*explicitLocale) != "" {
		normalized := runtime.NormalizeSupportedLocale(*explicitLocale)
		if normalized == nil {
			return "", domainerr.InvalidField("locale", "unsupported locale")
		}
		return *normalized, nil
	}
	return "", nil
}
