package email

import (
	"context"
	"log/slog"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
)

type localizedEmailTemplateRow struct {
	Locale      string  `gorm:"column:locale"`
	Subject     *string `gorm:"column:subject"`
	ContentHTML *string `gorm:"column:content_html"`
}

type canonicalEmailTemplateSourceRow struct {
	Subject     *string `gorm:"column:subject"`
	ContentHTML *string `gorm:"column:content_html"`
}

type canonicalEmailLayoutSourceRow struct {
	HTMLContent *string `gorm:"column:html_content"`
}

type localizedEmailLayoutRow struct {
	Locale      string  `gorm:"column:locale"`
	HTMLContent *string `gorm:"column:html_content"`
}

type emailLocalizationPolicy struct{}

func defaultEmailLocalizationPolicy() emailLocalizationPolicy {
	return emailLocalizationPolicy{}
}

func loadEmailLocalizationPolicy(context.Context, *gorm.DB) emailLocalizationPolicy {
	return defaultEmailLocalizationPolicy()
}

// ResolveLocalizedEmailTemplate overlays a locale-specific translation row onto a
// source email template when an exact or fallback locale variant exists.
func ResolveLocalizedEmailTemplate(
	ctx context.Context,
	db *gorm.DB,
	source model.EmailTemplate,
	requestedLocale string,
) (model.EmailTemplate, string, error) {
	sourceLocale := loadCanonicalEmailTemplateSourceLocale(ctx, db, source.ID)
	source, err := applyCanonicalEmailTemplateSourceContent(ctx, db, source, sourceLocale)
	if err != nil {
		return source, sourceLocale, err
	}
	requestedLocale = normalizeEmailLocale(requestedLocale)
	if requestedLocale == "" || requestedLocale == sourceLocale {
		return source, sourceLocale, nil
	}
	policy := loadEmailLocalizationPolicy(ctx, db)

	rows, err := loadEmailTemplateTranslationRows(ctx, db, source.ID, requestedLocale, sourceLocale, policy)
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

func loadCanonicalEmailTemplateSourceLocale(ctx context.Context, db *gorm.DB, templateID string) string {
	return loadEmailSourceLocale(ctx, db, "email_template", templateID)
}

func applyCanonicalEmailTemplateSourceContent(
	ctx context.Context,
	db *gorm.DB,
	source model.EmailTemplate,
	sourceLocale string,
) (model.EmailTemplate, error) {
	sourceLocale = normalizeEmailLocale(sourceLocale)
	if sourceLocale == "" {
		return source, errs.NotFound("email template source translation", source.ID)
	}

	row, err := loadExactCanonicalEmailTemplateSourceRow(ctx, db, source.ID, sourceLocale)
	if err != nil {
		slog.Warn("failed to load canonical email template source row", "template_id", source.ID, "source_locale", sourceLocale, "error", err)
		return source, err
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
	db *gorm.DB,
	templateID string,
	locale string,
) (*canonicalEmailTemplateSourceRow, error) {
	locale = normalizeEmailLocale(locale)
	if locale == "" {
		return nil, nil
	}

	var row canonicalEmailTemplateSourceRow
	if err := db.WithContext(ctx).
		Table("email_template_translation").
		Select("subject, content_html").
		Where("entity_id = ? AND locale = ?", templateID, locale).
		Where("content_html IS NOT NULL").
		Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}

	return &row, nil
}

// ResolveLocalizedEmailLayoutHTML overlays a locale-specific translation row onto
// an email layout and returns the HTML content that should be rendered.
func ResolveLocalizedEmailLayoutHTML(
	ctx context.Context,
	db *gorm.DB,
	layoutID string,
	requestedLocale string,
) (string, string, error) {
	sourceLocale := loadCanonicalEmailLayoutSourceLocale(ctx, db, layoutID)
	sourceHTML, err := loadCanonicalEmailLayoutSourceHTML(ctx, db, layoutID, sourceLocale)
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
	policy := loadEmailLocalizationPolicy(ctx, db)

	rows, err := loadEmailLayoutTranslationRows(ctx, db, layoutID, requestedLocale, sourceLocale, policy)
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
		slog.Warn("invalid localized email layout html, falling back to source layout",
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

func loadCanonicalEmailLayoutSourceLocale(ctx context.Context, db *gorm.DB, layoutID string) string {
	locale, found, err := loadLayoutSourceLocaleFlag(ctx, db, layoutID)
	if err == nil && found {
		if normalized := normalizeEmailLocale(locale); normalized != "" {
			return normalized
		}
	} else if err != nil {
		slog.Warn("failed to load canonical email layout source locale", "layout_id", layoutID, "error", err)
	}

	return loadEmailSourceLocale(ctx, db, "email_layout", layoutID)
}

func loadCanonicalEmailLayoutSourceHTML(
	ctx context.Context,
	db *gorm.DB,
	layoutID string,
	sourceLocale string,
) (string, error) {
	sourceLocale = normalizeEmailLocale(sourceLocale)
	if sourceLocale == "" {
		return "", errs.NotFound("email layout source translation", layoutID)
	}

	row, err := loadExactCanonicalEmailLayoutSourceRow(ctx, db, layoutID, sourceLocale)
	if err != nil {
		slog.Warn("failed to load canonical email layout source row", "layout_id", layoutID, "source_locale", sourceLocale, "error", err)
		return "", err
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
	db *gorm.DB,
	layoutID string,
	locale string,
) (*canonicalEmailLayoutSourceRow, error) {
	locale = normalizeEmailLocale(locale)
	if locale == "" {
		return nil, nil
	}

	var row canonicalEmailLayoutSourceRow
	if err := db.WithContext(ctx).
		Table("email_layout_translation").
		Select("html_content").
		Where("entity_id = ? AND locale = ?", layoutID, locale).
		Where("html_content IS NOT NULL").
		Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}

	return &row, nil
}

func normalizeEmailLocale(locale string) string {
	normalized := localization.NormalizeSupportedLocale(locale)
	if normalized == nil {
		return ""
	}
	return *normalized
}

func loadEmailSourceLocale(ctx context.Context, db *gorm.DB, entityType, entityID string) string {
	var sourceState struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	table := ""
	switch entityType {
	case "email_template":
		table = "email_template"
	case "email_layout":
		table = "email_layout"
	default:
		return ""
	}
	if err := db.WithContext(ctx).
		Table(table).
		Select("source_locale").
		Where("id = ?", entityID).
		Take(&sourceState).Error; err != nil {
		if err != gorm.ErrRecordNotFound {
			slog.Warn("failed to load email translation source locale", "entity_type", entityType, "entity_id", entityID, "error", err)
		}
		return ""
	}
	if sourceState.SourceLocale == "" {
		return ""
	}
	return sourceState.SourceLocale
}

func loadEmailTemplateTranslationRows(
	ctx context.Context,
	db *gorm.DB,
	templateID string,
	requestedLocale string,
	sourceLocale string,
	policy emailLocalizationPolicy,
) (map[string]localizedEmailTemplateRow, error) {
	locales := localesForEmailLookupWithPolicy(requestedLocale, sourceLocale, policy)
	if len(locales) == 0 {
		return map[string]localizedEmailTemplateRow{}, nil
	}
	var rows []localizedEmailTemplateRow
	if err := db.WithContext(ctx).Table("email_template_translation").
		Select("locale, subject, content_html").
		Where("entity_id = ? AND locale IN ?", templateID, locales).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[string]localizedEmailTemplateRow, len(rows))
	for _, row := range rows {
		result[row.Locale] = row
	}
	return result, nil
}

func loadEmailLayoutTranslationRows(
	ctx context.Context,
	db *gorm.DB,
	layoutID string,
	requestedLocale string,
	sourceLocale string,
	policy emailLocalizationPolicy,
) (map[string]localizedEmailLayoutRow, error) {
	locales := localesForEmailLookupWithPolicy(requestedLocale, sourceLocale, policy)
	if len(locales) == 0 {
		return map[string]localizedEmailLayoutRow{}, nil
	}
	var rows []localizedEmailLayoutRow
	if err := db.WithContext(ctx).Table("email_layout_translation").
		Select("locale, html_content").
		Where("entity_id = ? AND locale IN ?", layoutID, locales).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[string]localizedEmailLayoutRow, len(rows))
	for _, row := range rows {
		result[row.Locale] = row
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
