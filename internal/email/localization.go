package email

import (
	"context"
	"log/slog"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
)

type localizedEmailTemplateRow struct {
	Locale      string
	Subject     *string
	ContentHTML *string
}

type canonicalEmailTemplateSourceRow struct {
	Subject     *string
	ContentHTML *string
}

type canonicalEmailLayoutSourceRow struct {
	HTMLContent *string
}

type localizedEmailLayoutRow struct {
	Locale      string
	HTMLContent *string
}

type emailLocalizationPolicy struct{}

func defaultEmailLocalizationPolicy() emailLocalizationPolicy {
	return emailLocalizationPolicy{}
}

func loadEmailLocalizationPolicy(context.Context, Store) emailLocalizationPolicy {
	return defaultEmailLocalizationPolicy()
}

// ResolveLocalizedEmailTemplate overlays a locale-specific translation row onto a
// source email template when an exact or fallback locale variant exists.
func ResolveLocalizedEmailTemplate(
	ctx context.Context,
	store Store,
	source model.EmailTemplate,
	requestedLocale string,
) (model.EmailTemplate, string, error) {
	sourceLocale := loadCanonicalEmailTemplateSourceLocale(ctx, store, source.ID)
	source, err := applyCanonicalEmailTemplateSourceContent(ctx, store, source, sourceLocale)
	if err != nil {
		return source, sourceLocale, err
	}
	requestedLocale = normalizeEmailLocale(requestedLocale)
	if requestedLocale == "" || requestedLocale == sourceLocale {
		return source, sourceLocale, nil
	}
	policy := loadEmailLocalizationPolicy(ctx, store)

	rows, err := loadEmailTemplateTranslationRows(ctx, store, source.ID, requestedLocale, sourceLocale, policy)
	if err != nil {
		return source, sourceLocale, err
	}

	selected, displayedLocale := selectLocalizedEmailTemplateRowWithPolicy(requestedLocale, sourceLocale, rows, policy)
	if selected == nil {
		return source, sourceLocale, nil
	}

	localized := source
	if selected.Subject != nil {
		localized.Subject = *selected.Subject
	}
	if selected.ContentHTML != nil {
		html := *selected.ContentHTML
		localized.ContentHTML = &html
	}

	return localized, displayedLocale, nil
}

func loadCanonicalEmailTemplateSourceLocale(ctx context.Context, store Store, templateID string) string {
	return loadEmailSourceLocale(ctx, store, "email_template", templateID)
}

func applyCanonicalEmailTemplateSourceContent(
	ctx context.Context,
	store Store,
	source model.EmailTemplate,
	sourceLocale string,
) (model.EmailTemplate, error) {
	sourceLocale = normalizeEmailLocale(sourceLocale)
	if sourceLocale == "" {
		return source, errs.NotFound("email template source translation", source.ID)
	}

	row, err := loadExactCanonicalEmailTemplateSourceRow(ctx, store, source.ID, sourceLocale)
	if err != nil {
		return source, failurecause.WithAttrs(err, slog.String("template_id", source.ID), slog.String("source_locale", sourceLocale))
	}
	if row == nil {
		return source, errs.NotFound("email template source translation", source.ID+":"+sourceLocale)
	}

	return applyCanonicalEmailTemplateSourceRow(source, row), nil
}

func applyCanonicalEmailTemplateSourceRow(
	source model.EmailTemplate,
	row *canonicalEmailTemplateSourceRow,
) model.EmailTemplate {
	if row == nil {
		return source
	}
	if row.Subject != nil {
		source.Subject = *row.Subject
	}
	if row.ContentHTML != nil {
		html := *row.ContentHTML
		source.ContentHTML = &html
	}
	return source
}

func loadExactCanonicalEmailTemplateSourceRow(
	ctx context.Context,
	store Store,
	templateID string,
	locale string,
) (*canonicalEmailTemplateSourceRow, error) {
	locale = normalizeEmailLocale(locale)
	if locale == "" {
		return nil, nil
	}

	content, err := store.TemplateContent(ctx, templateID, locale)
	if err != nil || content == nil {
		return nil, err
	}
	return &canonicalEmailTemplateSourceRow{Subject: content.Subject, ContentHTML: content.ContentHTML}, nil
}

// ResolveLocalizedEmailLayoutHTML overlays a locale-specific translation row onto
// an email layout and returns the HTML content that should be rendered.
func ResolveLocalizedEmailLayoutHTML(
	ctx context.Context,
	store Store,
	layoutID string,
	requestedLocale string,
) (string, string, error) {
	sourceLocale := loadCanonicalEmailLayoutSourceLocale(ctx, store, layoutID)
	sourceHTML, err := loadCanonicalEmailLayoutSourceHTML(ctx, store, layoutID, sourceLocale)
	if err != nil {
		return "", sourceLocale, err
	}
	renderedSourceHTML, err := ResolveLayoutLocaleMarkup(sourceHTML, nil)
	if err != nil {
		return "", sourceLocale, errs.Internal(err)
	}
	requestedLocale = normalizeEmailLocale(requestedLocale)
	if requestedLocale == "" || requestedLocale == sourceLocale {
		return renderedSourceHTML, sourceLocale, nil
	}
	policy := loadEmailLocalizationPolicy(ctx, store)

	rows, err := loadEmailLayoutTranslationRows(ctx, store, layoutID, requestedLocale, sourceLocale, policy)
	if err != nil {
		return renderedSourceHTML, sourceLocale, err
	}

	selected, displayedLocale := selectLocalizedEmailLayoutRowWithPolicy(requestedLocale, sourceLocale, rows, policy)
	if selected == nil || selected.HTMLContent == nil {
		return renderedSourceHTML, sourceLocale, nil
	}

	localizedHTML, err := ResolveLayoutLocaleMarkup(sourceHTML, selected.HTMLContent)
	if err != nil {
		return "", sourceLocale, errs.Internal(err)
	}
	localizedHTML = NormalizeTemplatePlaceholders(localizedHTML)
	if issues := ValidateLayoutHTMLContent(localizedHTML); len(issues) > 0 {
		slog.WarnContext(ctx, "invalid localized email layout html, falling back to source layout",
			"layout_id", layoutID,
			"requested_locale", requestedLocale,
			"displayed_locale", displayedLocale,
			"issue_code", issues[0].Code,
			"issue_message", issues[0].Message,
		)
		return renderedSourceHTML, sourceLocale, nil
	}

	return localizedHTML, displayedLocale, nil
}

func loadCanonicalEmailLayoutSourceLocale(ctx context.Context, store Store, layoutID string) string {
	locale, found, err := loadLayoutSourceLocaleFlag(ctx, store, layoutID)
	if err == nil && found {
		if normalized := normalizeEmailLocale(locale); normalized != "" {
			return normalized
		}
	} else if err != nil {
		slog.WarnContext(ctx, "failed to load canonical email layout source locale; using the stored source locale", "layout_id", layoutID, failurecause.Attr(err))
	}

	return loadEmailSourceLocale(ctx, store, "email_layout", layoutID)
}

func loadCanonicalEmailLayoutSourceHTML(
	ctx context.Context,
	store Store,
	layoutID string,
	sourceLocale string,
) (string, error) {
	sourceLocale = normalizeEmailLocale(sourceLocale)
	if sourceLocale == "" {
		return "", errs.NotFound("email layout source translation", layoutID)
	}

	row, err := loadExactCanonicalEmailLayoutSourceRow(ctx, store, layoutID, sourceLocale)
	if err != nil {
		return "", failurecause.WithAttrs(err, slog.String("layout_id", layoutID), slog.String("source_locale", sourceLocale))
	}
	if row == nil || row.HTMLContent == nil {
		return "", errs.NotFound("email layout source translation", layoutID+":"+sourceLocale)
	}
	materialized, _, err := MaterializeLayoutSourceLocale(*row.HTMLContent)
	if err != nil {
		return "", errs.Internal(err)
	}
	return NormalizeTemplatePlaceholders(dereferenceString(materialized)), nil
}

func loadExactCanonicalEmailLayoutSourceRow(
	ctx context.Context,
	store Store,
	layoutID string,
	locale string,
) (*canonicalEmailLayoutSourceRow, error) {
	locale = normalizeEmailLocale(locale)
	if locale == "" {
		return nil, nil
	}

	html, err := store.LayoutContent(ctx, layoutID, locale)
	if err != nil || html == nil {
		return nil, err
	}
	return &canonicalEmailLayoutSourceRow{HTMLContent: html}, nil
}

func normalizeEmailLocale(locale string) string {
	normalized := localization.NormalizeSupportedLocale(locale)
	if normalized == nil {
		return ""
	}
	return *normalized
}

func loadEmailSourceLocale(ctx context.Context, store Store, entityType, entityID string) string {
	var (
		sourceLocale string
		found        bool
		err          error
	)
	switch entityType {
	case "email_template":
		sourceLocale, found, err = store.TemplateSourceLocale(ctx, entityID)
	case "email_layout":
		sourceLocale, found, err = store.LayoutSourceLocale(ctx, entityID)
	default:
		return ""
	}
	if err != nil {
		slog.WarnContext(ctx, "failed to load email translation source locale", "entity_type", entityType, "entity_id", entityID, failurecause.Attr(err))
		return ""
	}
	if !found {
		return ""
	}
	return sourceLocale
}

func loadEmailTemplateTranslationRows(
	ctx context.Context,
	store Store,
	templateID string,
	requestedLocale string,
	sourceLocale string,
	policy emailLocalizationPolicy,
) (map[string]localizedEmailTemplateRow, error) {
	locales := localesForEmailLookupWithPolicy(requestedLocale, sourceLocale, policy)
	if len(locales) == 0 {
		return map[string]localizedEmailTemplateRow{}, nil
	}
	rows, err := store.TemplateTranslations(ctx, templateID, locales)
	if err != nil {
		return nil, err
	}
	result := make(map[string]localizedEmailTemplateRow, len(rows))
	for _, row := range rows {
		result[row.Locale] = localizedEmailTemplateRow(row)
	}
	return result, nil
}

func loadEmailLayoutTranslationRows(
	ctx context.Context,
	store Store,
	layoutID string,
	requestedLocale string,
	sourceLocale string,
	policy emailLocalizationPolicy,
) (map[string]localizedEmailLayoutRow, error) {
	locales := localesForEmailLookupWithPolicy(requestedLocale, sourceLocale, policy)
	if len(locales) == 0 {
		return map[string]localizedEmailLayoutRow{}, nil
	}
	rows, err := store.LayoutTranslations(ctx, layoutID, locales)
	if err != nil {
		return nil, err
	}
	result := make(map[string]localizedEmailLayoutRow, len(rows))
	for _, row := range rows {
		result[row.Locale] = localizedEmailLayoutRow{Locale: row.Locale, HTMLContent: row.HTMLContent}
	}
	return result, nil
}

func localesForEmailLookupWithPolicy(
	requestedLocale string,
	sourceLocale string,
	policy emailLocalizationPolicy,
) []string {
	requestedLocale = normalizeEmailLocale(requestedLocale)
	sourceLocale = normalizeEmailLocale(sourceLocale)
	if requestedLocale == "" {
		return nil
	}

	if requestedLocale == sourceLocale {
		return nil
	}
	return []string{requestedLocale}
}

func selectLocalizedEmailTemplateRowWithPolicy(
	requestedLocale string,
	sourceLocale string,
	rows map[string]localizedEmailTemplateRow,
	policy emailLocalizationPolicy,
) (*localizedEmailTemplateRow, string) {
	requestedLocale = normalizeEmailLocale(requestedLocale)
	sourceLocale = normalizeEmailLocale(sourceLocale)

	if requestedLocale == "" || requestedLocale == sourceLocale {
		return nil, sourceLocale
	}

	for _, locale := range localesForEmailLookupWithPolicy(requestedLocale, sourceLocale, policy) {
		if row, ok := rows[locale]; ok {
			return &row, locale
		}
	}
	return nil, sourceLocale
}

func selectLocalizedEmailLayoutRowWithPolicy(
	requestedLocale string,
	sourceLocale string,
	rows map[string]localizedEmailLayoutRow,
	policy emailLocalizationPolicy,
) (*localizedEmailLayoutRow, string) {
	requestedLocale = normalizeEmailLocale(requestedLocale)
	sourceLocale = normalizeEmailLocale(sourceLocale)

	if requestedLocale == "" || requestedLocale == sourceLocale {
		return nil, sourceLocale
	}

	for _, locale := range localesForEmailLookupWithPolicy(requestedLocale, sourceLocale, policy) {
		if row, ok := rows[locale]; ok {
			return &row, locale
		}
	}
	return nil, sourceLocale
}
