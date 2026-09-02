package publiccontent

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/translation"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"gorm.io/gorm"
)

const defaultLocale = localization.LocaleEnglish

// Spec describes one domain-owned public translation projection. The owning
// domain supplies its table and projection because localized fields differ by
// domain; this package owns only locale selection and source-identity policy.
type Spec struct {
	EntityType   string
	TableName    string
	SelectClause string
}

// Selection is the domain-neutral localized content chosen for a public row.
type Selection struct {
	RequestedLocale      string
	DisplayedLocale      string
	SourceLocale         string
	AvailableLocales     []string
	IsFallback           bool
	IsOriginal           bool
	FallbackReason       openv1.LocalizationFallbackReason
	Title                *string
	Summary              *string
	ContentJSON          []byte
	ContentHTML          *string
	ContentText          *string
	OgAssetID            *string
	OmitSourceOgFallback bool
}

type translationRow struct {
	Locale      string
	Title       *string
	Summary     *string
	ContentJSON []byte
	ContentHTML *string
	ContentText *string
	OgAssetID   *string
}

type batchTranslationRow struct {
	EntityID    string
	Locale      string
	Title       *string
	Summary     *string
	ContentJSON []byte
	ContentHTML *string
	ContentText *string
	OgAssetID   *string
}

type availabilityRow struct {
	Locale string
}

type batchAvailabilityRow struct {
	EntityID string
	Locale   string
}

// Resolve loads runtime policy and resolves one entity's public localization.
// Runtime-setting read failures retain the established default serving policy.
func Resolve(
	ctx context.Context,
	db *gorm.DB,
	spec Spec,
	entityID string,
	acceptLanguage string,
) (Selection, error) {
	settings, err := translation.LoadRuntimeSettings(ctx, db)
	if err != nil {
		settings = translation.DefaultRuntimeSettings()
	}
	return ResolveWithPolicy(ctx, db, spec, entityID, acceptLanguage, settings)
}

// ResolveWithPolicy resolves one entity using an explicit runtime policy.
func ResolveWithPolicy(
	ctx context.Context,
	db *gorm.DB,
	spec Spec,
	entityID string,
	acceptLanguage string,
	settings translation.RuntimeSettings,
) (Selection, error) {
	if err := validateSpec(spec); err != nil {
		return Selection{}, err
	}
	requestedLocale := requestedLocale(acceptLanguage, settings.DefaultLocale)
	sourceLocale, err := loadSourceLocale(ctx, db, spec, entityID, settings.DefaultLocale)
	if err != nil {
		fallback := normalizedLocale(settings.DefaultLocale, defaultLocale)
		return sourceFallback(requestedLocale, fallback), err
	}

	locales := []string{requestedLocale}
	if requestedLocale != sourceLocale {
		locales = append(locales, sourceLocale)
	}
	rows, err := loadRows(ctx, db, spec, entityID, locales)
	if err != nil {
		return sourceFallback(requestedLocale, sourceLocale), err
	}
	selection := selectWithPolicy(requestedLocale, sourceLocale, rows, settings)
	available, availableErr := AvailableLocales(ctx, db, spec, entityID, sourceLocale, settings)
	if availableErr == nil {
		selection.AvailableLocales = available
	}
	return selection, nil
}

// ResolveBatch loads runtime policy and resolves the same public locale for a
// set of entity IDs with bounded value rows and lightweight locale presence.
func ResolveBatch(
	ctx context.Context,
	db *gorm.DB,
	spec Spec,
	entityIDs []string,
	acceptLanguage string,
) (map[string]Selection, error) {
	settings, err := translation.LoadRuntimeSettings(ctx, db)
	if err != nil {
		settings = translation.DefaultRuntimeSettings()
	}
	return ResolveBatchWithPolicy(ctx, db, spec, entityIDs, acceptLanguage, settings)
}

// ResolveBatchWithPolicy resolves a set of entity IDs using explicit runtime
// locale policy. Target row existence is sufficient; each missing value falls
// back to the authoritative source value independently.
func ResolveBatchWithPolicy(
	ctx context.Context,
	db *gorm.DB,
	spec Spec,
	entityIDs []string,
	acceptLanguage string,
	settings translation.RuntimeSettings,
) (map[string]Selection, error) {
	if err := validateSpec(spec); err != nil {
		return nil, err
	}
	ids := normalizedEntityIDs(entityIDs)
	if len(ids) == 0 {
		return map[string]Selection{}, nil
	}
	requested := requestedLocale(acceptLanguage, settings.DefaultLocale)

	definition, ok := translation.DefinitionForKind(spec.EntityType)
	if !ok || definition.EntryTable != spec.TableName {
		return nil, fmt.Errorf("unsupported localized entity type %q", spec.EntityType)
	}
	type sourceRow struct {
		ID           string
		SourceLocale string
	}
	sourceByID := make(map[string]string, len(ids))
	var sourceRows []sourceRow
	if err := db.WithContext(ctx).
		Table(definition.RootTable).
		Select("id, source_locale").
		Where("id IN ?", ids).
		Scan(&sourceRows).Error; err != nil {
		return nil, err
	}
	for _, row := range sourceRows {
		sourceByID[row.ID] = normalizedLocale(row.SourceLocale, settings.DefaultLocale)
	}

	locales := []string{requested}
	for _, entityID := range ids {
		source := sourceByID[entityID]
		if source == "" {
			source = normalizedLocale(settings.DefaultLocale, defaultLocale)
			sourceByID[entityID] = source
		}
		if !slices.Contains(locales, source) {
			locales = append(locales, source)
		}
	}

	query, err := Rows(ctx, db, spec)
	if err != nil {
		return nil, err
	}
	var rows []batchTranslationRow
	if err := query.
		Select("entity_id, "+spec.SelectClause).
		Where("entity_id IN ? AND locale IN ?", ids, locales).
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	rowsByID := make(map[string]map[string]translationRow, len(ids))
	for _, row := range rows {
		if rowsByID[row.EntityID] == nil {
			rowsByID[row.EntityID] = make(map[string]translationRow, 2)
		}
		candidate := translationRow{
			Locale: row.Locale, Title: row.Title, Summary: row.Summary,
			ContentJSON: row.ContentJSON, ContentHTML: row.ContentHTML, ContentText: row.ContentText,
			OgAssetID: row.OgAssetID,
		}
		rowsByID[row.EntityID][row.Locale] = candidate
	}
	availabilityQuery, err := Rows(ctx, db, spec)
	if err != nil {
		return nil, err
	}
	var availableRows []batchAvailabilityRow
	if err := availabilityQuery.
		Select("entity_id, locale").
		Where("entity_id IN ?", ids).
		Scan(&availableRows).Error; err != nil {
		return nil, err
	}
	availableByID := make(map[string][]string, len(ids))
	for _, row := range availableRows {
		availableByID[row.EntityID] = append(availableByID[row.EntityID], row.Locale)
	}

	selections := make(map[string]Selection, len(ids))
	for _, entityID := range ids {
		selection := selectWithPolicy(
			requested, sourceByID[entityID], rowsByID[entityID], settings,
		)
		selection.AvailableLocales = localization.AvailableLocales(
			sourceByID[entityID], availableByID[entityID],
			localization.ServingPolicy{DefaultLocale: settings.DefaultLocale},
		)
		selections[entityID] = selection
	}
	return selections, nil
}

// AvailableLocales returns the source locale plus every stored target locale.
func AvailableLocales(
	ctx context.Context,
	db *gorm.DB,
	spec Spec,
	entityID string,
	sourceLocale string,
	settings translation.RuntimeSettings,
) ([]string, error) {
	if err := validateSpec(spec); err != nil {
		return []string{normalizedLocale(sourceLocale, settings.DefaultLocale)}, err
	}
	query, err := Rows(ctx, db, spec)
	if err != nil {
		return []string{normalizedLocale(sourceLocale, settings.DefaultLocale)}, err
	}
	var rows []availabilityRow
	if err := query.
		Select("locale").
		Where("entity_id = ?", entityID).
		Scan(&rows).Error; err != nil {
		return []string{normalizedLocale(sourceLocale, settings.DefaultLocale)}, err
	}
	storedLocales := make([]string, 0, len(rows))
	for _, row := range rows {
		storedLocales = append(storedLocales, row.Locale)
	}
	return localization.AvailableLocales(
		sourceLocale, storedLocales, localization.ServingPolicy{DefaultLocale: settings.DefaultLocale},
	), nil
}

// Rows returns stored locale rows. Removed legacy source metadata and entry
// status are not target visibility or freshness authorities.
func Rows(ctx context.Context, db *gorm.DB, spec Spec) (*gorm.DB, error) {
	if err := validateSpec(spec); err != nil {
		return nil, err
	}
	definition, ok := translation.DefinitionForKind(spec.EntityType)
	if !ok || definition.EntryTable != spec.TableName {
		return nil, fmt.Errorf("unsupported localized entity type %q", spec.EntityType)
	}
	return db.WithContext(ctx).Table(spec.TableName), nil
}

func validateSpec(spec Spec) error {
	if strings.TrimSpace(spec.EntityType) == "" || strings.TrimSpace(spec.TableName) == "" ||
		strings.TrimSpace(spec.SelectClause) == "" {
		return fmt.Errorf("public content localization spec is incomplete")
	}
	selectClause := strings.ToLower(spec.SelectClause)
	for _, forbidden := range []string{
		"status", "machine_generated", "provider", "model",
		"source_hash", "source_revision", "source_epoch", "published_at",
		"structure_hash", "materialization_hash",
	} {
		if strings.Contains(selectClause, forbidden) {
			return fmt.Errorf("public content localization spec projects forbidden target metadata %q", forbidden)
		}
	}
	return nil
}

func loadSourceLocale(ctx context.Context, db *gorm.DB, spec Spec, entityID, fallback string) (string, error) {
	definition, ok := translation.DefinitionForKind(spec.EntityType)
	if !ok || definition.EntryTable != spec.TableName {
		return "", fmt.Errorf("unsupported localized entity type %q", spec.EntityType)
	}
	var row struct {
		SourceLocale string
	}
	if err := db.WithContext(ctx).
		Table(definition.RootTable).
		Select("source_locale").
		Where("id = ?", entityID).
		Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return normalizedLocale(fallback, defaultLocale), nil
		}
		return "", err
	}
	return normalizedLocale(row.SourceLocale, fallback), nil
}

func loadRows(
	ctx context.Context,
	db *gorm.DB,
	spec Spec,
	entityID string,
	locales []string,
) (map[string]translationRow, error) {
	locales = normalizedLocales(locales)
	if len(locales) == 0 {
		return map[string]translationRow{}, nil
	}
	query, err := Rows(ctx, db, spec)
	if err != nil {
		return nil, err
	}
	var rows []translationRow
	if err := query.
		Select(spec.SelectClause).
		Where("entity_id = ? AND locale IN ?", entityID, locales).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[string]translationRow, len(rows))
	for _, row := range rows {
		result[row.Locale] = row
	}
	return result, nil
}

func selectWithPolicy(
	requested string,
	source string,
	rows map[string]translationRow,
	settings translation.RuntimeSettings,
) Selection {
	canonicalRows := make(map[string]translationRow, len(rows))
	storedLocales := make(map[string]struct{}, len(rows))
	for key, row := range rows {
		locale := row.Locale
		if strings.TrimSpace(locale) == "" {
			locale = key
		}
		normalized := localization.NormalizeSupportedLocale(locale)
		if normalized == nil {
			continue
		}
		row.Locale = *normalized
		canonicalRows[*normalized] = row
		storedLocales[*normalized] = struct{}{}
	}
	requested = normalizedLocale(requested, settings.DefaultLocale)
	source = normalizedLocale(source, settings.DefaultLocale)
	decision := localization.Select(
		requested,
		source,
		storedLocales,
		localization.ServingPolicy{DefaultLocale: settings.DefaultLocale},
	)
	selection := Selection{
		RequestedLocale: decision.RequestedLocale,
		DisplayedLocale: decision.DisplayedLocale,
		SourceLocale:    decision.SourceLocale,
		IsFallback:      decision.IsFallback,
		IsOriginal:      decision.IsOriginal,
		FallbackReason:  fallbackReason(decision.FallbackReason),
		AvailableLocales: []string{
			source,
		},
	}
	if !decision.HasCandidate {
		return selection
	}
	selectedRow := canonicalRows[decision.DisplayedLocale]
	applyRow(&selection, selectedRow, decision.DisplayedLocale)
	if decision.DisplayedLocale != source {
		sourceRow, hasSource := canonicalRows[source]
		if hasSource && applyMissingSourceFields(&selection, selectedRow, sourceRow) {
			selection.IsFallback = true
			selection.FallbackReason = openv1.LocalizationFallbackReason_LOCALIZATION_FALLBACK_REASON_SOURCE
		}
	}
	if selection.IsFallback {
		selection.IsFallback = true
		selection.FallbackReason = openv1.LocalizationFallbackReason_LOCALIZATION_FALLBACK_REASON_SOURCE
	}
	return selection
}

func applyMissingSourceFields(selection *Selection, target translationRow, source translationRow) bool {
	fallback := false
	if target.Title == nil && source.Title != nil {
		selection.Title = source.Title
		fallback = true
	}
	if target.Summary == nil && source.Summary != nil {
		selection.Summary = source.Summary
		fallback = true
	}
	if target.ContentJSON == nil && source.ContentJSON != nil {
		selection.ContentJSON = source.ContentJSON
		fallback = true
	}
	if target.ContentHTML == nil && source.ContentHTML != nil {
		selection.ContentHTML = source.ContentHTML
		fallback = true
	}
	if target.ContentText == nil && source.ContentText != nil {
		selection.ContentText = source.ContentText
		fallback = true
	}
	return fallback
}

func sourceFallback(requested, source string) Selection {
	return Selection{
		RequestedLocale: requested, DisplayedLocale: source, SourceLocale: source,
		AvailableLocales: []string{source}, IsOriginal: true, IsFallback: requested != source,
		FallbackReason: openv1.LocalizationFallbackReason_LOCALIZATION_FALLBACK_REASON_SOURCE,
	}
}

func applyRow(selection *Selection, row translationRow, locale string) {
	selection.DisplayedLocale = locale
	selection.Title = row.Title
	selection.Summary = row.Summary
	selection.ContentJSON = row.ContentJSON
	selection.ContentHTML = row.ContentHTML
	selection.ContentText = row.ContentText
	selection.OgAssetID = row.OgAssetID
}

func requestedLocale(acceptLanguage, fallback string) string {
	if locale := localization.InferPreferredLocaleFromAcceptLanguage(acceptLanguage); locale != nil {
		return *locale
	}
	return normalizedLocale(fallback, defaultLocale)
}

func normalizedLocale(locale, fallback string) string {
	return localization.NormalizeWithDefault(locale, fallback)
}

func normalizedLocales(locales []string) []string {
	result := make([]string, 0, len(locales))
	seen := make(map[string]struct{}, len(locales))
	for _, locale := range locales {
		locale = strings.TrimSpace(locale)
		if locale == "" {
			continue
		}
		if _, exists := seen[locale]; exists {
			continue
		}
		seen[locale] = struct{}{}
		result = append(result, locale)
	}
	return result
}

func normalizedEntityIDs(entityIDs []string) []string {
	result := make([]string, 0, len(entityIDs))
	seen := make(map[string]struct{}, len(entityIDs))
	for _, entityID := range entityIDs {
		entityID = strings.TrimSpace(entityID)
		if entityID == "" {
			continue
		}
		if _, exists := seen[entityID]; exists {
			continue
		}
		seen[entityID] = struct{}{}
		result = append(result, entityID)
	}
	return result
}

func fallbackReason(reason localization.FallbackReason) openv1.LocalizationFallbackReason {
	if reason == localization.FallbackSource {
		return openv1.LocalizationFallbackReason_LOCALIZATION_FALLBACK_REASON_SOURCE
	}
	return openv1.LocalizationFallbackReason_LOCALIZATION_FALLBACK_REASON_NONE
}

// ToProtoLocalizationInfo projects the stable public localization metadata.
func ToProtoLocalizationInfo(selection Selection) *openv1.LocalizationInfo {
	return &openv1.LocalizationInfo{
		RequestedLocale: selection.RequestedLocale, DisplayedLocale: selection.DisplayedLocale,
		SourceLocale: selection.SourceLocale, IsFallback: selection.IsFallback, IsOriginal: selection.IsOriginal,
		FallbackReason: selection.FallbackReason, AvailableLocales: selection.AvailableLocales,
	}
}
