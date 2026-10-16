package email

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// DraftEmail contains already-selected content and layout inputs. Document takes
// precedence over ContentHTML when present. Locale lookup,
// authorization, recipient resolution, and sending remain with their owners.
type DraftEmail struct {
	Document    *contentv1.LocalizedRichTextDocument
	ContentHTML string
	Subject     string
	LayoutHTML  string
	Variables   map[string]string
	Locale      string
}

// RenderDraftEmail renders an unsaved document or a stored content projection
// through the same body, layout, and variable pipeline used by delivery. It does
// not access a database, resolve files, contact a provider, or send mail.
func RenderDraftEmail(ctx context.Context, draft DraftEmail) (*RenderedEmail, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	content := draft.ContentHTML
	if draft.Document != nil {
		materialized, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, draft.Document, nil, sitevariable.TemplateContext())
		if err != nil {
			return nil, fmt.Errorf("render email document: %w", err)
		}
		content = materialized.HTML
	}
	subject, err := RenderSubjectVarsStrict(draft.Subject, draft.Variables)
	if err != nil {
		return nil, fmt.Errorf("render email subject: %w", err)
	}
	variables := maps.Clone(draft.Variables)
	if variables == nil {
		variables = make(map[string]string)
	}
	variables["subject"] = subject
	body, err := RenderEmailHTML(content, draft.LayoutHTML, variables)
	if err != nil {
		return nil, err
	}
	return &RenderedEmail{
		Subject: subject, HTML: body, Text: StripHTML(body),
		DisplayedLocale: normalizeEmailLocale(draft.Locale),
	}, nil
}

// RenderEmailHTML is the shared presentation boundary for previews and delivery.
// Only rich-text content receives email presentation; raw layout tags and styles
// are preserved. Variables are expanded once, after content enters the layout.
func RenderEmailHTML(content, layout string, variables map[string]string) (string, error) {
	body := NormalizeEmailContentHTML(content)
	if strings.TrimSpace(layout) != "" {
		layout = NormalizeTemplatePlaceholders(layout)
		body = strings.ReplaceAll(layout, "{{content}}", body)
	}
	body, err := RenderHTMLVarsStrict(body, variables)
	if err != nil {
		return "", fmt.Errorf("render email HTML: %w", err)
	}
	return NormalizeRenderedHTML(body), nil
}

// PrepareDraftLayoutHTML models the source preparation performed when an
// authored layout is saved and localized. Delivery already supplies selected
// markup and must not assign fresh translation markers to a frozen snapshot.
func PrepareDraftLayoutHTML(source string) (string, error) {
	canonical, err := CanonicalizeLayoutSourceMarkers(source)
	if err != nil {
		return "", err
	}
	return ResolveLayoutLocaleMarkup(canonical, nil)
}
