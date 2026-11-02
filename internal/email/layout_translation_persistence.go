package email

import (
	"context"
	"strings"
	"time"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// LayoutTranslationDocument is the locale-owned raw wrapper state for one
// Email Layout.
type LayoutTranslationDocument struct {
	ContentHTML *string
	ContentText *string
	UpdatedAt   time.Time
}

// LayoutTranslationEntry is the persisted Email Layout translation row used
// by common Translation lifecycle orchestration.
type LayoutTranslationEntry struct {
	LayoutTranslationDocument
}

// CanonicalLayoutProjection is the source wrapper used by Email Layout list
// and detail projections.
type CanonicalLayoutProjection struct {
	LayoutID    string
	ContentHTML string
}

// LayoutLocaleSnapshot is one immutable delivery-snapshot input row.
type LayoutLocaleSnapshot struct {
	Locale         string
	ContentHTML    *string
	IsSourceLocale bool
}

// LoadCanonicalLayoutProjections loads the exact source row for every requested
// layout. Missing source rows are omitted.
func LoadCanonicalLayoutProjections(
	ctx context.Context,
	store AuthoringStore,
	layoutIDs []string,
) (map[string]CanonicalLayoutProjection, error) {
	if len(layoutIDs) == 0 {
		return map[string]CanonicalLayoutProjection{}, nil
	}
	rows, err := store.SourceLayouts(ctx, layoutIDs)
	if err != nil {
		return nil, errs.Internal(err)
	}
	projections := make(map[string]CanonicalLayoutProjection, len(rows))
	for _, row := range rows {
		if row.HTMLContent == nil {
			continue
		}
		canonical, _, err := MaterializeLayoutSourceLocale(*row.HTMLContent)
		if err != nil {
			return nil, errs.Internal(err)
		}
		projections[row.LayoutID] = CanonicalLayoutProjection{
			LayoutID: row.LayoutID, ContentHTML: NormalizeTemplatePlaceholders(dereferenceString(canonical)),
		}
	}
	return projections, nil
}

// LoadLayoutLocaleSnapshots returns every existing locale row in the exact
// deterministic order used to seal delivery snapshots.
func LoadLayoutLocaleSnapshots(
	ctx context.Context,
	store AuthoringStore,
	layoutID string,
) ([]LayoutLocaleSnapshot, error) {
	rows, err := store.LayoutLocales(ctx, layoutID)
	if err != nil {
		return nil, err
	}
	var storedSourceHTML *string
	for _, row := range rows {
		if row.IsSourceLocale {
			storedSourceHTML = row.HTMLContent
			break
		}
	}
	if storedSourceHTML == nil {
		return nil, errs.NotFound("email layout source translation", layoutID)
	}
	sourceHTML, _, err := MaterializeLayoutSourceLocale(*storedSourceHTML)
	if err != nil {
		return nil, errs.Internal(err)
	}
	snapshots := make([]LayoutLocaleSnapshot, 0, len(rows))
	for _, row := range rows {
		resolved, err := ResolveLayoutLocaleMarkup(dereferenceString(sourceHTML), func() *string {
			if row.IsSourceLocale {
				return nil
			}
			return row.HTMLContent
		}())
		if err != nil {
			return nil, errs.Internal(err)
		}
		snapshots = append(snapshots, LayoutLocaleSnapshot{
			Locale: row.Locale, ContentHTML: stringPointer(resolved),
			IsSourceLocale: row.IsSourceLocale,
		})
	}
	return snapshots, nil
}

// LoadLayoutTranslationEntry loads one locale row, returning nil when absent.
func LoadLayoutTranslationEntry(
	ctx context.Context,
	store AuthoringStore,
	layoutID string,
	locale string,
) (*LayoutTranslationEntry, error) {
	row, err := store.LayoutEntry(ctx, layoutID, locale)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if row == nil {
		return nil, nil
	}

	entry := &LayoutTranslationEntry{
		LayoutTranslationDocument: LayoutTranslationDocument{
			UpdatedAt: row.UpdatedAt,
		},
	}
	if row.HTMLContent != nil {
		normalized := NormalizeTemplatePlaceholders(*row.HTMLContent)
		entry.ContentHTML = &normalized
	}
	if row.ContentText != nil {
		entry.ContentText = stringPointer(*row.ContentText)
	}
	return entry, nil
}

// LoadLayoutTranslationDocument returns one required locale document.
func LoadLayoutTranslationDocument(
	ctx context.Context,
	store AuthoringStore,
	layoutID string,
	locale string,
) (*LayoutTranslationDocument, error) {
	entry, err := LoadLayoutTranslationEntry(ctx, store, layoutID, locale)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, errs.NotFound("email layout translation", layoutID+":"+locale)
	}
	document := entry.LayoutTranslationDocument
	if document.ContentText == nil && document.ContentHTML != nil {
		document.ContentText = stringPointer(StripHTML(*document.ContentHTML))
	}
	return &document, nil
}

// ResolveLayoutTranslationSourceLocale resolves the exact authoring source
// authority. The caller owns fallback policy when no persisted authority exists.
func ResolveLayoutTranslationSourceLocale(
	ctx context.Context,
	store AuthoringStore,
	layoutID string,
) (string, bool, error) {
	locale, found, err := store.LayoutSourceLocale(ctx, layoutID)
	if err != nil {
		return "", false, errs.Internal(err)
	}
	if found && strings.TrimSpace(locale) != "" {
		return locale, true, nil
	}
	return "", false, nil
}

// LoadCanonicalLayoutTranslationDocument loads the required source-locale row.
func LoadCanonicalLayoutTranslationDocument(
	ctx context.Context,
	store AuthoringStore,
	layoutID string,
	sourceLocale string,
) (*LayoutTranslationDocument, error) {
	sourceLocale = strings.TrimSpace(sourceLocale)
	if sourceLocale == "" {
		return nil, errs.NotFound("email layout source translation", layoutID)
	}
	document, err := LoadLayoutTranslationDocument(ctx, store, layoutID, sourceLocale)
	if err != nil {
		return nil, err
	}
	if document.ContentHTML == nil {
		return nil, errs.FailedPrecondition("Email Layout source locale content is missing")
	}
	contentHTML, contentText, err := MaterializeLayoutSourceLocale(*document.ContentHTML)
	if err != nil {
		return nil, errs.WithCause(errs.FailedPrecondition("Email Layout source locale markers are invalid"), failurecause.ServerFault(err))
	}
	document.ContentHTML = contentHTML
	document.ContentText = contentText
	return document, nil
}

// LoadLayoutTranslationSourceDocument returns the provider-visible source.
func LoadLayoutTranslationSourceDocument(
	ctx context.Context,
	store AuthoringStore,
	layoutID string,
	sourceLocale string,
) (*translation.SourceDocument, error) {
	document, err := LoadCanonicalLayoutTranslationDocument(ctx, store, layoutID, sourceLocale)
	if err != nil {
		return nil, err
	}
	return &translation.SourceDocument{
		ContentHTML: document.ContentHTML,
		ContentText: document.ContentText,
	}, nil
}

func loadLayoutSourceLocaleFlag(
	ctx context.Context,
	store AuthoringStore,
	layoutID string,
) (string, bool, error) {
	locale, found, err := store.LayoutSourceLocale(ctx, layoutID)
	if err != nil {
		return "", false, errs.Internal(err)
	}
	if !found {
		return "", false, nil
	}
	locale = strings.TrimSpace(locale)
	return locale, locale != "", nil
}

func stringPointer(value string) *string {
	return &value
}
