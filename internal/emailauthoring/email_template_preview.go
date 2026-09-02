package emailauthoring

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// templateKeyRegex validates the format of template keys
func (s *EmailTemplateService) PreviewEmailTemplate(
	ctx context.Context,
	req *connect.Request[managev1.PreviewEmailTemplateRequest],
) (*connect.Response[managev1.PreviewEmailTemplateResponse], error) {
	can, canErr := policyv1.EmailTemplate.View(req.Msg.Id)
	if _, err := s.requireEmailTemplateCan(ctx, can, canErr); err != nil {
		return nil, err
	}

	var emailTemplate model.EmailTemplate
	if err := s.db.WithContext(ctx).First(&emailTemplate, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("email template", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}

	// Render with sample data. Site branding variables come from site settings.
	requestedLocale := ""
	if req.Msg.Locale != nil {
		requestedLocale = *req.Msg.Locale
	}

	sampleData := s.buildEmailRenderData(ctx, requestedLocale, map[string]string{
		"name":               "John Doe",
		"recipient_name":     "John Doe",
		"recipient_email":    "john@example.com",
		"to":                 "john@example.com",
		"identity_email":     "john@example.com",
		"identity_name":      "John Doe",
		"confirm_url":        "https://example.com/confirm/abc123",
		"reset_link":         "https://example.com/reset/abc123",
		"confirm_link":       "https://example.com/confirm/abc123",
		"cancel_url":         "https://example.com/cancel/abc123",
		"unsubscribe_link":   "https://example.com/unsubscribe/abc123",
		"verification_code":  "123456",
		"verification_url":   "https://example.com/verify/abc123",
		"request_url":        "https://example.com/auth/continue/abc123",
		"expires_in_minutes": "15",
		"recover_url":        "https://example.com/recover/abc123",
		"login_code":         "789012",
		"registration_code":  "345678",
		"preview_url":        "https://example.com/preview/abc123",
		"policy_title":       "September policy update",
		"terms_url":          "https://example.com/terms",
		"privacy_url":        "https://example.com/privacy",
		"login_url":          "https://example.com/login",
		"signout_all_url":    "https://example.com/my/security",
		"expires_in":         "24 hours",
		"effective_date":     "2026-01-01",
		"scheduled_date":     "2026-02-01",
		"grace_period":       "30 days",
		"old_email":          "old@example.com",
		"new_email":          "new@example.com",
		"country":            "US",
		"location":           "United States",
		"ip":                 "203.0.113.42",
		"time":               "2026-01-01T09:00:00Z",
		"device":             "Chrome on macOS",
		"provider":           "email",
	})

	localizedTemplate, err := s.runtime.ResolveLocalizedTemplate(ctx, s.db, emailTemplate, requestedLocale)
	if err != nil {
		if connect.CodeOf(err) == connect.CodeNotFound {
			return nil, errs.NotFound("email template", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}

	previewSubject := resolveEmailTemplatePreviewSubject(req.Msg, localizedTemplate)
	html, err := resolveEmailTemplatePreviewHTML(ctx, req.Msg, localizedTemplate)
	if err != nil {
		return nil, err
	}

	// Fill any template-specific placeholders with synthetic sample values
	// so preview can render unknown/custom variables instead of leaving braces.
	fillMissingPreviewVariables(sampleData, previewSubject, html)

	renderedSubject := s.runtime.RenderVariables(previewSubject, sampleData)
	renderedHTML := s.runtime.RenderVariables(html, sampleData)

	// Add subject to sampleData for layout rendering
	sampleData["subject"] = renderedSubject

	// Determine layout ID: request param takes precedence over saved value
	layoutID := resolveEmailTemplatePreviewLayoutID(req.Msg, localizedTemplate)

	// Wrap with layout if configured
	if layoutID != nil {
		var layoutErr error
		renderedHTML, layoutErr = s.runtime.WrapWithLayout(
			ctx,
			s.db,
			*layoutID,
			requestedLocale,
			renderedHTML,
			sampleData,
		)
		if layoutErr != nil {
			return nil, errs.FailedPrecondition("email layout preview failed: " + layoutErr.Error())
		}
		fillMissingPreviewVariables(sampleData, renderedHTML)
	}

	renderedHTML = s.runtime.RenderVariables(renderedHTML, sampleData)
	renderedHTML = s.runtime.NormalizeRenderedHTML(renderedHTML)

	return connect.NewResponse(&managev1.PreviewEmailTemplateResponse{
		Subject: renderedSubject,
		Html:    renderedHTML,
	}), nil
}

// Helper functions

func resolveEmailTemplatePreviewSubject(
	req *managev1.PreviewEmailTemplateRequest,
	emailTemplate model.EmailTemplate,
) string {
	if req.Subject != nil {
		return *req.Subject
	}

	return emailTemplate.Subject
}

func resolveEmailTemplatePreviewHTML(
	ctx context.Context,
	req *managev1.PreviewEmailTemplateRequest,
	emailTemplate model.EmailTemplate,
) (string, error) {
	if req.GetDocument() != nil {
		materialized, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, req.GetDocument(), nil)
		if err != nil {
			return "", errs.InvalidArgument("document", err.Error())
		}
		return materialized.HTML, nil
	}

	if emailTemplate.ContentHTML != nil {
		return *emailTemplate.ContentHTML, nil
	}

	return "", nil
}

func resolveEmailTemplatePreviewLayoutID(
	req *managev1.PreviewEmailTemplateRequest,
	emailTemplate model.EmailTemplate,
) *string {
	if req.LayoutId != nil {
		if *req.LayoutId == "" {
			return nil
		}

		return req.LayoutId
	}

	return emailTemplate.LayoutID
}

func fillMissingPreviewVariables(sampleData map[string]string, templateTexts ...string) {
	for _, text := range templateTexts {
		if strings.TrimSpace(text) == "" {
			continue
		}
		matches := previewPlaceholderRegex.FindAllStringSubmatch(text, -1)
		for _, match := range matches {
			if len(match) < 2 {
				continue
			}
			key := strings.TrimSpace(match[1])
			if key == "" {
				continue
			}
			if _, exists := sampleData[key]; exists {
				continue
			}
			sampleData[key] = previewSampleValueForKey(key)
		}
	}
}

func previewSampleValueForKey(key string) string {
	lowerKey := strings.ToLower(strings.TrimSpace(key))

	switch {
	case lowerKey == "to" || strings.Contains(lowerKey, "email"):
		return "john@example.com"
	case strings.Contains(lowerKey, "name"):
		return "John Doe"
	case strings.Contains(lowerKey, "url") || strings.HasSuffix(lowerKey, "_link"):
		return fmt.Sprintf("https://example.com/%s", strings.ReplaceAll(lowerKey, "_", "-"))
	case strings.Contains(lowerKey, "code"):
		return "123456"
	case strings.Contains(lowerKey, "date"):
		return "2026-01-01"
	case strings.Contains(lowerKey, "time"):
		return "2026-01-01T09:00:00Z"
	case strings.Contains(lowerKey, "expires"):
		return "24 hours"
	case strings.Contains(lowerKey, "grace"):
		return "30 days"
	case strings.Contains(lowerKey, "ip"):
		return "203.0.113.42"
	case strings.Contains(lowerKey, "country"):
		return "US"
	case strings.Contains(lowerKey, "provider"):
		return "email"
	default:
		return fmt.Sprintf("sample_%s", lowerKey)
	}
}
