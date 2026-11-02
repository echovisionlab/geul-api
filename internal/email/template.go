package email

import (
	"context"
	"errors"
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"regexp"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/structured"
)

const directTemplatePrefix = "template:"

// DirectTemplateType returns the worker template_type identifier for directly
// rendering an admin-managed email template by ID.
func DirectTemplateType(templateID string) string {
	return directTemplatePrefix + strings.TrimSpace(templateID)
}

func IsDirectTemplateType(templateType string) bool {
	templateID := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(templateType), directTemplatePrefix))
	return strings.HasPrefix(strings.TrimSpace(templateType), directTemplatePrefix) && templateID != ""
}

// RenderedEmail contains the rendered email content.
type RenderedEmail struct {
	Subject            string
	HTML               string
	Text               string
	DisplayedLocale    string
	TemplateLocale     string
	LayoutID           string
	LayoutLocale       string
	ResolvedByFallback bool
	LayoutUsedFallback bool
}

// RenderTemplateForLocale renders an email template with locale-specific template
// and layout overrides when the requested locale rows exist.
func RenderTemplateForLocale(
	ctx context.Context,
	store Store,
	eventKey string,
	requestedLocale string,
	data map[string]string,
) (*RenderedEmail, error) {
	if _, ok := strings.CutPrefix(strings.TrimSpace(eventKey), "campaign:"); ok {
		return nil, fmt.Errorf("campaign templates require the campaign renderer")
	}

	// Handle a direct template reference (template:{id})
	if after, ok := strings.CutPrefix(eventKey, directTemplatePrefix); ok {
		templateID := after
		return renderTemplateByID(ctx, store, templateID, requestedLocale, data)
	}

	// Look up template by event_key
	tmpl, err := store.ActiveTemplateByEventKey(ctx, eventKey)
	if err != nil {
		if errors.Is(err, ErrTemplateNotFound) {
			slog.Info("Email template render skipped",
				"domain", "mail",
				"event", "mail.delivery.render_skipped",
				"outcome", "skipped",
				"reason", "template_not_configured",
				"template_type", eventKey,
			)
			return nil, nil
		}
		return nil, fmt.Errorf("failed to fetch template: %w", err)
	}

	return renderStoredTemplate(ctx, store, tmpl, requestedLocale, data)
}

func renderTemplateByID(
	ctx context.Context,
	store Store,
	templateID string,
	requestedLocale string,
	data map[string]string,
) (*RenderedEmail, error) {
	tmpl, err := store.TemplateByID(ctx, templateID)
	if err != nil {
		if errors.Is(err, ErrTemplateNotFound) {
			slog.Info("Email template render skipped",
				"domain", "mail",
				"event", "mail.delivery.render_skipped",
				"outcome", "skipped",
				"reason", "direct_template_not_found",
				"template_id", templateID,
			)
			return nil, nil
		}
		return nil, fmt.Errorf("failed to fetch template by id: %w", err)
	}

	return renderStoredTemplate(ctx, store, tmpl, requestedLocale, data)
}

func renderStoredTemplate(
	ctx context.Context,
	store Store,
	tmpl Template,
	requestedLocale string,
	data map[string]string,
) (result *RenderedEmail, renderErr error) {
	layoutID := ""
	renderLocale := requestedLocale
	if tmpl.LayoutID != nil {
		layoutID = *tmpl.LayoutID
	}
	defer func() {
		if renderErr != nil {
			renderErr = &RenderError{LayoutID: layoutID, Locale: renderLocale, Err: renderErr}
		}
	}()

	localizedTemplate, templateLocale, err := ResolveLocalizedEmailTemplate(ctx, store, tmpl, requestedLocale)
	if renderLocale == "" {
		renderLocale = templateLocale
	}
	if err != nil {
		return nil, fmt.Errorf("failed to resolve localized template: %w", err)
	}

	subject, err := RenderSubjectVarsStrict(localizedTemplate.Subject, data)
	if err != nil {
		return nil, fmt.Errorf("render email subject: %w", err)
	}

	html := ""
	if localizedTemplate.ContentHTML != nil {
		html = *localizedTemplate.ContentHTML
	}

	// Add subject to data for layout rendering
	data["subject"] = subject

	displayedLocale := templateLocale
	layoutLocale := ""

	// Wrap with layout if configured
	if localizedTemplate.LayoutID != nil {
		var resolvedLayoutLocale string
		html, resolvedLayoutLocale, err = WrapWithLayoutForLocaleStrict(ctx, store, *localizedTemplate.LayoutID, requestedLocale, html, data)
		if resolvedLayoutLocale != "" {
			renderLocale = resolvedLayoutLocale
		}
		if err != nil {
			return nil, fmt.Errorf("render email layout: %w", err)
		}
		layoutLocale = resolvedLayoutLocale
		if layoutLocale != "" {
			displayedLocale = layoutLocale
		}
	} else {
		html, err = RenderEmailHTML(html, "", data)
		if err != nil {
			return nil, fmt.Errorf("render email HTML: %w", err)
		}
	}

	html = NormalizeRenderedHTML(html)

	return &RenderedEmail{
		Subject:            subject,
		HTML:               html,
		Text:               StripHTML(html),
		DisplayedLocale:    displayedLocale,
		TemplateLocale:     templateLocale,
		LayoutID:           layoutID,
		LayoutLocale:       layoutLocale,
		ResolvedByFallback: templateLocale != "" && requestedLocale != "" && templateLocale != requestedLocale,
		LayoutUsedFallback: layoutLocale != "" && requestedLocale != "" && layoutLocale != requestedLocale,
	}, nil
}

// RenderCampaignSnapshotForLocale renders a materialized campaign delivery run
// from its run-level snapshot. This keeps already-created delivery runs stable
// even if the campaign or email layout is edited while recipients are still
// being fanned out or sent.
func RenderCampaignSnapshotForLocale(
	snapshot structured.Fields,
	requestedLocale string,
	data map[string]string,
) (*RenderedEmail, error) {
	normalizedRequestedLocale := normalizeEmailLocale(requestedLocale)
	content, templateLocale := selectCampaignSnapshotContent(snapshot, normalizedRequestedLocale)
	subject, err := RenderSubjectVarsStrict(content.Subject, data)
	if err != nil {
		return nil, fmt.Errorf("render delivery snapshot subject: %w", err)
	}
	html := content.ContentHTML

	data["subject"] = subject

	displayedLocale := templateLocale
	layoutHTML, layoutLocale := selectCampaignSnapshotLayout(snapshot, normalizedRequestedLocale)
	if layoutLocale != "" {
		displayedLocale = layoutLocale
	}
	html, err = RenderEmailHTML(html, layoutHTML, data)
	if err != nil {
		return nil, fmt.Errorf("render delivery snapshot HTML: %w", err)
	}

	return &RenderedEmail{
		Subject:            subject,
		HTML:               html,
		Text:               StripHTML(html),
		DisplayedLocale:    displayedLocale,
		TemplateLocale:     templateLocale,
		LayoutLocale:       layoutLocale,
		ResolvedByFallback: templateLocale != "" && normalizedRequestedLocale != "" && templateLocale != normalizedRequestedLocale,
		LayoutUsedFallback: layoutLocale != "" && normalizedRequestedLocale != "" && layoutLocale != normalizedRequestedLocale,
	}, nil
}

type campaignSnapshotContent struct {
	Locale      string
	Subject     string
	ContentHTML string
}

type campaignSnapshotLayout struct {
	Locale      string
	HTMLContent string
}

func selectCampaignSnapshotContent(snapshot structured.Fields, requestedLocale string) (campaignSnapshotContent, string) {
	sourceLocale := normalizeEmailLocale(stringFromStructuredValue(snapshot["source_locale"]))
	base := campaignSnapshotContent{
		Locale:      sourceLocale,
		Subject:     stringFromStructuredValue(snapshot["subject"]),
		ContentHTML: stringFromStructuredValue(snapshot["content_html"]),
	}
	rows := campaignSnapshotContentRows(snapshot["translations"])
	selected := selectCampaignSnapshotContentRow(rows, requestedLocale, sourceLocale)
	if selected == nil {
		return base, sourceLocale
	}
	return *selected, selected.Locale
}

func selectCampaignSnapshotLayout(snapshot structured.Fields, requestedLocale string) (string, string) {
	sourceLocale := normalizeEmailLocale(stringFromStructuredValue(snapshot["layout_source_locale"]))
	rows := campaignSnapshotLayoutRows(snapshot["layout_translations"])
	selected := selectCampaignSnapshotLayoutRow(rows, requestedLocale, sourceLocale)
	if selected == nil {
		return "", ""
	}
	return selected.HTMLContent, selected.Locale
}

func selectCampaignSnapshotContentRow(rows []campaignSnapshotContent, requestedLocale string, sourceLocale string) *campaignSnapshotContent {
	requestedLocale = normalizeEmailLocale(requestedLocale)
	sourceLocale = normalizeEmailLocale(sourceLocale)
	byLocale := make(map[string]campaignSnapshotContent, len(rows))
	for _, row := range rows {
		locale := normalizeEmailLocale(row.Locale)
		if locale == "" {
			continue
		}
		row.Locale = locale
		byLocale[locale] = row
	}
	if requestedLocale != "" {
		if row, ok := byLocale[requestedLocale]; ok {
			return &row
		}
	}
	if sourceLocale != "" {
		if row, ok := byLocale[sourceLocale]; ok {
			return &row
		}
	}
	return nil
}

func selectCampaignSnapshotLayoutRow(rows []campaignSnapshotLayout, requestedLocale string, sourceLocale string) *campaignSnapshotLayout {
	requestedLocale = normalizeEmailLocale(requestedLocale)
	sourceLocale = normalizeEmailLocale(sourceLocale)
	byLocale := make(map[string]campaignSnapshotLayout, len(rows))
	for _, row := range rows {
		locale := normalizeEmailLocale(row.Locale)
		if locale == "" {
			continue
		}
		row.Locale = locale
		byLocale[locale] = row
	}
	if requestedLocale != "" {
		if row, ok := byLocale[requestedLocale]; ok {
			return &row
		}
	}
	if sourceLocale != "" {
		if row, ok := byLocale[sourceLocale]; ok {
			return &row
		}
	}
	return nil
}

func campaignSnapshotContentRows(value structured.Value) []campaignSnapshotContent {
	entries := structuredMapEntries(value)
	rows := make([]campaignSnapshotContent, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, campaignSnapshotContent{
			Locale:      stringFromStructuredValue(entry["locale"]),
			Subject:     stringFromStructuredValue(entry["subject"]),
			ContentHTML: stringFromStructuredValue(entry["content_html"]),
		})
	}
	return rows
}

func campaignSnapshotLayoutRows(value structured.Value) []campaignSnapshotLayout {
	entries := structuredMapEntries(value)
	rows := make([]campaignSnapshotLayout, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, campaignSnapshotLayout{
			Locale:      stringFromStructuredValue(entry["locale"]),
			HTMLContent: stringFromStructuredValue(entry["html_content"]),
		})
	}
	return rows
}

func structuredMapEntries(value structured.Value) []structured.Fields {
	switch typed := value.(type) {
	case []structured.Fields:
		return typed
	case structured.Values:
		entries := make([]structured.Fields, 0, len(typed))
		for _, raw := range typed {
			if entry, ok := raw.(structured.Fields); ok {
				entries = append(entries, entry)
			}
		}
		return entries
	default:
		return nil
	}
}

func stringFromStructuredValue(value structured.Value) string {
	if value == nil {
		return ""
	}
	if typed, ok := value.(string); ok {
		return typed
	}
	return fmt.Sprint(value)
}

func WrapWithLayoutForLocaleStrict(
	ctx context.Context,
	store Store,
	layoutID string,
	requestedLocale string,
	content string,
	data map[string]string,
) (string, string, error) {
	layoutHTML, displayedLocale, err := ResolveLocalizedEmailLayoutHTML(ctx, store, layoutID, requestedLocale)
	if err != nil {
		return "", displayedLocale, err
	}
	rendered, err := RenderEmailHTML(content, layoutHTML, data)
	if strings.TrimSpace(layoutHTML) == "" {
		return rendered, "", err
	}
	if err != nil {
		return "", "", err
	}
	return rendered, displayedLocale, nil
}

// RenderVars replaces {{key}} placeholders with HTML-escaped values.
// This is the single template rendering function used by all email paths:
// campaign emails, system emails (Kratos), and preview rendering.
func RenderVars(tmplStr string, data map[string]string) string {
	tmplStr = NormalizeTemplatePlaceholders(tmplStr)
	if tmplStr == "" || len(data) == 0 {
		return tmplStr
	}

	escapedByKey := make(map[string]string, len(data)*3)
	for key, value := range data {
		escaped := template.HTMLEscapeString(value)
		escapedByKey[key] = escaped

		lowerKey := strings.ToLower(key)
		if _, exists := escapedByKey[lowerKey]; !exists {
			escapedByKey[lowerKey] = escaped
		}

		upperKey := strings.ToUpper(key)
		if _, exists := escapedByKey[upperKey]; !exists {
			escapedByKey[upperKey] = escaped
		}
	}

	return renderVarRe.ReplaceAllStringFunc(tmplStr, func(match string) string {
		// match is guaranteed to be "{{...}}" by renderVarRe.
		key := strings.TrimSpace(match[2 : len(match)-2])
		if key == "" {
			return match
		}
		if value, ok := escapedByKey[key]; ok {
			return value
		}
		if value, ok := escapedByKey[strings.ToLower(key)]; ok {
			return value
		}
		return match
	})
}

func RenderSubjectVarsStrict(tmplStr string, data map[string]string) (string, error) {
	return renderVarsStrict(tmplStr, data, func(value string) string { return value })
}

func RenderHTMLVarsStrict(tmplStr string, data map[string]string) (string, error) {
	if err := ValidateSiteVariableHrefs(NormalizeTemplatePlaceholders(tmplStr)); err != nil {
		return "", err
	}
	rendered, err := renderVarsStrict(tmplStr, data, html.EscapeString)
	if err != nil {
		return "", err
	}
	return resolveEmailHrefs(rendered, data)
}

func renderVarsStrict(
	tmplStr string,
	data map[string]string,
	escape func(string) string,
) (string, error) {
	tmplStr = NormalizeTemplatePlaceholders(tmplStr)
	values := make(map[string]string, len(data))
	for key, value := range data {
		key = strings.ToLower(strings.TrimSpace(key))
		if key != "" {
			values[key] = escape(value)
		}
	}
	missing := map[string]struct{}{}
	result := renderVarRe.ReplaceAllStringFunc(tmplStr, func(match string) string {
		key := strings.ToLower(strings.TrimSpace(match[2 : len(match)-2]))
		if value, ok := values[key]; ok {
			return value
		}
		missing[key] = struct{}{}
		return match
	})
	if len(missing) > 0 {
		keys := make([]string, 0, len(missing))
		for key := range missing {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		return "", fmt.Errorf("unknown or missing email placeholders: %s", strings.Join(keys, ", "))
	}
	return result, nil
}

// Pre-compiled template normalization patterns
var (
	renderVarRe                = regexp.MustCompile(`{{\s*[^\s{}]+\s*}}`)
	normalizedPlaceholderRe    = regexp.MustCompile(`\{\s*\{\s*([^\s{}]+)\s*\}\s*\}`)
	cssPlaceholderValueRe      = regexp.MustCompile(`:\s*({{[^\s{}]+}})\s*(!important|;)`)
	attributePlaceholderRe     = regexp.MustCompile(`=\s*({{[^\s{}]+}})`)
	doctypeRe                  = regexp.MustCompile(`(?is)<!doctype\s+html\s*>`)
	emptyParagraphRe           = regexp.MustCompile(`(?is)<p([^>]*)>\s*</p>`)
	doubleQuoteDuplicateHrefRe = regexp.MustCompile(`(?i)\bhref="(https?:\/\/)(https?:\/\/[^"<>]+)"`)
	singleQuoteDuplicateHrefRe = regexp.MustCompile(`(?i)\bhref='(https?:\/\/)(https?:\/\/[^'<>]+)'`)
)

func NormalizeTemplatePlaceholders(value string) string {
	if value == "" || !strings.Contains(value, "{") {
		return value
	}
	normalized := normalizedPlaceholderRe.ReplaceAllString(value, `{{$1}}`)
	normalized = cssPlaceholderValueRe.ReplaceAllStringFunc(normalized, func(match string) string {
		parts := cssPlaceholderValueRe.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		if parts[2] == "!important" {
			return ": " + parts[1] + " " + parts[2]
		}
		return ": " + parts[1] + parts[2]
	})
	normalized = attributePlaceholderRe.ReplaceAllString(normalized, `=$1`)
	return normalized
}

func normalizeRenderedLinkHrefs(html string) string {
	if html == "" || !strings.Contains(strings.ToLower(html), "href=") {
		return html
	}

	result := html
	for {
		next := doubleQuoteDuplicateHrefRe.ReplaceAllString(result, `href="$2"`)
		next = singleQuoteDuplicateHrefRe.ReplaceAllString(next, `href='$2'`)
		if next == result {
			return result
		}
		result = next
	}
}

// NormalizeRenderedHTML keeps empty paragraphs visible in rendered email output
// without changing the stored editor HTML in the database.
func NormalizeRenderedHTML(html string) string {
	if html == "" {
		return html
	}

	normalized := normalizeRenderedDoctype(html)
	normalized = normalizeRenderedLinkHrefs(normalized)
	if !strings.Contains(strings.ToLower(normalized), "<p") {
		return normalized
	}

	return emptyParagraphRe.ReplaceAllString(normalized, "<p$1>&nbsp;</p>")
}

func normalizeRenderedDoctype(html string) string {
	matches := doctypeRe.FindAllStringIndex(html, -1)
	if len(matches) <= 1 {
		return html
	}
	result := html
	for i := len(matches) - 1; i >= 0; i-- {
		match := matches[i]
		if i == 0 && strings.TrimSpace(result[:match[0]]) == "" {
			continue
		}
		result = result[:match[0]] + result[match[1]:]
	}
	return result
}
