package campaign

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/sitevariable"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

// emailRegex for validating email format in test campaign sends
var campaignEmailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)

// PreviewCampaign returns a preview of the campaign email
func (s *CampaignService) PreviewCampaign(
	ctx context.Context,
	req *connect.Request[managev1.PreviewCampaignRequest],
) (*connect.Response[managev1.PreviewCampaignResponse], error) {
	can, canErr := policyv1.Campaign.View(req.Msg.Id)
	if err := s.requireCampaignCan(ctx, can, canErr); err != nil {
		return nil, err
	}

	if req.Msg.Id == "" {
		return nil, errs.Required("id")
	}

	var campaign model.Campaign
	if err := s.db.WithContext(ctx).First(&campaign, "id = ?", req.Msg.Id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFoundMsg("campaign not found")
		}
		return nil, errs.Internal(err)
	}
	// Site branding variables come from site settings. Campaign preview uses a
	// deterministic sample recipient; recipient-specific previews are not part
	// of the campaign contract.
	requestedLocale, err := resolveCampaignRequestedLocale(s.runtime, req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	if s.emailRendering == nil {
		return nil, errs.DependencyUnavailable("Email rendering")
	}
	sampleData := s.emailRendering.BuildRenderData(ctx, s.db, s.cdnDomain, s.siteOrigin, requestedLocale, map[string]string{
		"recipient_email":  "john@example.com",
		"unsubscribe_link": "https://example.com/unsubscribe/sample",
	})

	localizedCampaign, _, err := ResolveLocalizedCampaign(
		ctx,
		s.db,
		s.contentBlocks,
		campaign,
		requestedLocale,
	)
	if err != nil {
		return nil, err
	}
	if req.Msg.Subject != nil {
		localizedCampaign.Subject = *req.Msg.Subject
	}
	if req.Msg.GetDocument() != nil {
		materialized, materializeErr := contentblock.MaterializeLocalizedRichTextDocument(ctx, req.Msg.GetDocument(), nil, sitevariable.TemplateContext())
		if materializeErr != nil {
			return nil, errs.InvalidArgument("document", materializeErr.Error())
		}
		localizedCampaign.ContentHTML = &materialized.HTML
	}

	// Render subject and content with variables
	subject, err := email.RenderSubjectVarsStrict(localizedCampaign.Subject, sampleData)
	if err != nil {
		return nil, errs.InvalidArgument("subject", err.Error())
	}
	htmlContent := ""
	if localizedCampaign.ContentHTML != nil {
		htmlContent = *localizedCampaign.ContentHTML
	}

	// Add subject to sampleData for layout rendering
	sampleData["subject"] = subject

	// Determine layout ID: request param takes precedence over saved value
	var layoutID *string
	if req.Msg.LayoutId != nil {
		if strings.TrimSpace(*req.Msg.LayoutId) != "" {
			layoutID = req.Msg.LayoutId
		}
	} else {
		layoutID = localizedCampaign.LayoutID
	}

	// Wrap with layout if configured
	if layoutID != nil {
		htmlContent, err = s.emailRendering.WrapWithLayout(
			ctx,
			s.db,
			*layoutID,
			requestedLocale,
			htmlContent,
			sampleData,
		)
		if err != nil {
			return nil, err
		}
	}

	if layoutID == nil {
		htmlContent, err = email.RenderEmailHTML(htmlContent, "", sampleData)
		if err != nil {
			return nil, errs.InvalidArgument("document", err.Error())
		}
	}
	htmlContent = s.emailRendering.NormalizeHTML(htmlContent)

	// Generate plain text from HTML
	textContent := s.emailRendering.PlainText(htmlContent)

	return connect.NewResponse(&managev1.PreviewCampaignResponse{
		Preview: &managev1.CampaignPreview{
			Subject:     subject,
			HtmlContent: htmlContent,
			TextContent: textContent,
		},
	}), nil
}

// SendTestCampaign sends a test email
func (s *CampaignService) SendTestCampaign(
	ctx context.Context,
	req *connect.Request[managev1.SendTestCampaignRequest],
) (*connect.Response[managev1.SendTestCampaignResponse], error) {
	campaignCan, err := policyv1.Campaign.Manage(req.Msg.Id)
	if err != nil {
		return nil, errs.InvalidArgument("id", err.Error())
	}

	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var campaign model.Campaign
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&campaign, "id = ?", req.Msg.Id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.NotFoundMsg("campaign not found")
			}
			return errs.Internal(err)
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, campaignCan); err != nil {
			return err
		}
		if req.Msg.Email == "" {
			return errs.Required("email")
		}
		if !campaignEmailRegex.MatchString(req.Msg.Email) {
			return errs.InvalidArgument("email", "invalid format")
		}
		if s.emailRendering == nil {
			return errs.DependencyUnavailable("Email rendering")
		}
		if err := email.CheckTestSendReadiness(ctx, tx, req.Msg.Email); err != nil {
			if message := email.TestSendReadinessMessage(err); message != "" {
				return errs.FailedPrecondition(message)
			}
			return errs.Internal(err)
		}
		campaignID := req.Msg.Id
		sendJob := &managev1.SendEmailEvent{
			Recipient:    req.Msg.Email,
			TemplateType: fmt.Sprintf("campaign:%s", campaignID),
			TemplateData: map[string]string{
				"recipient_email":  req.Msg.Email,
				"unsubscribe_link": campaignPreviewUnsubscribeLink(s.siteOrigin),
			},
			ReferenceId:      &campaignID,
			RecipientContext: s.emailRendering.TestRecipientContext(campaignEmailTestActorID(ctx)),
		}
		if req.Msg.Locale != nil && strings.TrimSpace(*req.Msg.Locale) != "" {
			normalizedLocale := s.runtime.NormalizeSupportedLocale(*req.Msg.Locale)
			if normalizedLocale == nil {
				return errs.InvalidArgument("locale", "unsupported locale")
			}
			sendJob.Locale = normalizedLocale
		}
		messageID := "campaign-test:" + uuid.NewString()
		sendJob.MessageId = &messageID
		if err := publishCampaignDurableProtoInTransaction(ctx, s.runtime, tx, eventpkg.QueueEmailSend, messageID, sendJob); err != nil {
			return errs.Internal(fmt.Errorf("queue campaign test email: %w", err))
		}
		return nil
	}); err != nil {
		return nil, err
	}

	return connect.NewResponse(&managev1.SendTestCampaignResponse{
		Success: true,
	}), nil
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
			return "", errs.InvalidArgument("locale", "unsupported locale")
		}
		return *normalized, nil
	}
	return "", nil
}
