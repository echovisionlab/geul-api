package emailauthoring

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/sitevariable"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/model"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// PreviewEmailTemplateQuery renders an Email Template with sample data. A
// set Subject, Document or LayoutID previews that unsaved value instead of
// the stored one; an empty LayoutID previews without a layout.
type PreviewEmailTemplateQuery struct {
	ID       string
	Locale   *string
	Subject  *string
	Document *contentv1.LocalizedRichTextDocument
	LayoutID *string
}

// TemplatePreview is a rendered Email Template preview.
type TemplatePreview struct {
	Subject string
	HTML    string
}

// PreviewEmailTemplate renders the template in the requested locale with
// sample data and the site's branding variables.
func (s *Service) PreviewEmailTemplate(ctx context.Context, query PreviewEmailTemplateQuery) (TemplatePreview, error) {
	can, canErr := policyv1.EmailTemplate.View(query.ID)
	if err := s.requireAdminCan(ctx, can, canErr); err != nil {
		return TemplatePreview{}, err
	}
	var emailTemplate model.EmailTemplate
	if err := s.db.WithContext(ctx).First(&emailTemplate, "id = ?", query.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return TemplatePreview{}, domainerr.ResourceNotFound("email template", query.ID)
		}
		return TemplatePreview{}, err
	}
	requestedLocale := ""
	if query.Locale != nil {
		requestedLocale = *query.Locale
	}
	sampleData := s.buildEmailRenderData(ctx, requestedLocale, emailTemplateSampleData())
	localizedTemplate, err := s.runtime.ResolveLocalizedTemplate(ctx, s.db, emailTemplate, requestedLocale)
	if err != nil {
		if mutation.IsNotFound(err) {
			return TemplatePreview{}, domainerr.ResourceNotFound("email template", query.ID)
		}
		return TemplatePreview{}, err
	}
	previewSubject := resolveEmailTemplatePreviewSubject(query, localizedTemplate)
	html, err := resolveEmailTemplatePreviewHTML(ctx, query, localizedTemplate)
	if err != nil {
		return TemplatePreview{}, err
	}
	// Fill any template-specific placeholders with synthetic sample values
	// so preview can render unknown/custom variables instead of leaving braces.
	fillMissingPreviewVariables(sampleData, previewSubject, html)
	layoutHTML := ""
	if layoutID := resolveEmailTemplatePreviewLayoutID(query, localizedTemplate); layoutID != nil {
		layoutHTML, err = s.runtime.ResolveLayoutHTML(ctx, s.db, *layoutID, requestedLocale)
		if err != nil {
			return TemplatePreview{}, domainerr.Precondition("email layout preview failed: " + err.Error())
		}
	}
	rendered, err := email.RenderDraftEmail(ctx, email.DraftEmail{
		Subject: previewSubject, ContentHTML: html, LayoutHTML: layoutHTML,
		Variables: sampleData, Locale: requestedLocale,
	})
	if err != nil {
		return TemplatePreview{}, domainerr.Precondition(err.Error())
	}
	return TemplatePreview{Subject: rendered.Subject, HTML: rendered.HTML}, nil
}

// Helper functions

func resolveEmailTemplatePreviewSubject(
	req PreviewEmailTemplateQuery,
	emailTemplate model.EmailTemplate,
) string {
	if req.Subject != nil {
		return *req.Subject
	}

	return emailTemplate.Subject
}

func resolveEmailTemplatePreviewHTML(
	ctx context.Context,
	req PreviewEmailTemplateQuery,
	emailTemplate model.EmailTemplate,
) (string, error) {
	if req.Document != nil {
		materialized, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, req.Document, nil, sitevariable.TemplateContext())
		if err != nil {
			return "", domainerr.InvalidField("document", err.Error())
		}
		return materialized.HTML, nil
	}

	if emailTemplate.ContentHTML != nil {
		return *emailTemplate.ContentHTML, nil
	}

	return "", nil
}

func resolveEmailTemplatePreviewLayoutID(
	req PreviewEmailTemplateQuery,
	emailTemplate model.EmailTemplate,
) *string {
	if req.LayoutID != nil {
		if *req.LayoutID == "" {
			return nil
		}

		return req.LayoutID
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
			if strings.HasPrefix(key, "site.") || strings.HasPrefix(key, "contact.") {
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

// emailTemplateSampleData is shared by preview and explicit admin test sends.
// Always return a fresh map: rendering adds subject and recipient aliases.
func emailTemplateSampleData() map[string]string {
	return map[string]string{
		"name":               "John Doe",
		"recipient_name":     "John Doe",
		"recipient_email":    "john@example.com",
		"email":              "john@example.com",
		"to":                 "john@example.com",
		"identity_email":     "john@example.com",
		"identity_name":      "John Doe",
		"confirm_url":        "https://example.com/confirm/abc123",
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
	}
}
