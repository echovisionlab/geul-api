package email

import (
	"context"
	"strings"
	"time"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// UpsertLayoutTranslationEntry persists one Email Layout locale entry.
func UpsertLayoutTranslationEntry(
	ctx context.Context,
	store Store,
	layoutID string,
	locale string,
	input translation.EntryWrite,
) error {
	if input.ContentHTML != nil {
		normalized := NormalizeTemplatePlaceholders(*input.ContentHTML)
		if err := ValidateLayoutHTMLContentError(normalized); err != nil {
			return errs.InvalidArgument("html_content", err.Error())
		}
		input.ContentHTML = &normalized
	}

	return store.UpsertLayoutEntry(ctx, layoutID, locale, input.ContentHTML, input.ContentText, input.Now)
}

// SaveLayoutSourceLocaleDocument persists the canonical source wrapper and
// makes it the only source-locale row.
func SaveLayoutSourceLocaleDocument(
	ctx context.Context,
	store Store,
	layoutID string,
	locale string,
	document LayoutTranslationDocument,
	now time.Time,
) error {
	htmlContent := NormalizeTemplatePlaceholders(dereferenceString(document.ContentHTML))
	canonical, err := CanonicalizeLayoutSourceMarkers(htmlContent)
	if err != nil {
		return errs.InvalidArgument("html_content", err.Error())
	}
	htmlContent = canonical
	contentText := document.ContentText
	if contentText == nil || strings.TrimSpace(dereferenceString(contentText)) == "" {
		contentText = stringPointer(StripHTML(htmlContent))
	}
	if err := UpsertLayoutTranslationEntry(ctx, store, layoutID, locale, translation.EntryWrite{
		ContentHTML: &htmlContent,
		ContentText: contentText,
		Now:         now,
	}); err != nil {
		return err
	}
	return normalizeLayoutTargetRepresentations(ctx, store, layoutID, locale, htmlContent)
}

// PrepareLayoutSourceLocaleSwitch makes only the requested locale safe to
// assume the source role. Existing locale values and updated_at are preserved;
// an absent row receives one complete explicit-empty value wrapper.
func PrepareLayoutSourceLocaleSwitch(
	ctx context.Context,
	store Store,
	layoutID string,
	currentSourceLocale string,
	requestedLocale string,
	now time.Time,
) error {
	current, err := LoadCanonicalLayoutTranslationDocument(
		ctx,
		store,
		layoutID,
		currentSourceLocale,
	)
	if err != nil {
		return err
	}
	if current.ContentHTML == nil {
		return errs.FailedPrecondition("Email Layout source locale content is missing")
	}
	entry, err := LoadLayoutTranslationEntry(ctx, store, layoutID, requestedLocale)
	if err != nil {
		return err
	}
	var contentHTML, contentText *string
	if entry == nil || entry.ContentHTML == nil {
		contentHTML, contentText, err = EmptyLayoutLocaleFromSource(*current.ContentHTML)
	} else {
		contentHTML, contentText, err = NormalizeLayoutLocaleRepresentation(
			*current.ContentHTML,
			*entry.ContentHTML,
		)
	}
	if err != nil {
		return errs.WithCause(errs.FailedPrecondition("Email Layout locale markers require repair before source-locale switching"), failurecause.ServerFault(err))
	}
	if entry == nil {
		return UpsertLayoutTranslationEntry(ctx, store, layoutID, requestedLocale, translation.EntryWrite{
			ContentHTML: contentHTML,
			ContentText: contentText,
			Now:         now.UTC(),
		})
	}
	if dereferenceString(entry.ContentHTML) == dereferenceString(contentHTML) &&
		dereferenceString(entry.ContentText) == dereferenceString(contentText) {
		return nil
	}
	return store.UpdateLayoutEntry(ctx, layoutID, requestedLocale, contentHTML, contentText)
}

func normalizeLayoutTargetRepresentations(
	ctx context.Context,
	store Store,
	layoutID string,
	sourceLocale string,
	canonicalSource string,
) error {
	rows, err := store.LayoutTargets(ctx, layoutID, sourceLocale)
	if err != nil {
		return errs.Internal(err)
	}
	for _, row := range rows {
		if row.HTMLContent == nil {
			continue
		}
		nextHTML, nextText, err := NormalizeLayoutLocaleRepresentation(
			canonicalSource,
			*row.HTMLContent,
		)
		if err != nil {
			return errs.WithCause(errs.FailedPrecondition("Email Layout target markers require backfill before source editing"), failurecause.ServerFault(err))
		}
		if dereferenceString(nextHTML) == *row.HTMLContent &&
			row.ContentText != nil && dereferenceString(nextText) == *row.ContentText {
			continue
		}
		if err := store.UpdateLayoutEntry(ctx, layoutID, row.Locale, nextHTML, nextText); err != nil {
			return errs.Internal(err)
		}
	}
	return nil
}

// SeedLayoutTranslationEntryFromSource creates one target row whose locale
// values are an explicit copy of the current source values. Public fallback
// never calls this function; explicit target creation does.
func SeedLayoutTranslationEntryFromSource(
	ctx context.Context,
	store Store,
	layoutID string,
	locale string,
	sourceLocale string,
	now time.Time,
) error {
	if strings.TrimSpace(sourceLocale) == "" {
		return errs.InvalidArgument("source_locale", "source locale is required")
	}
	if locale == sourceLocale {
		return errs.FailedPrecondition("Email Layout source locale changed while seeding target metadata")
	}
	source, err := LoadCanonicalLayoutTranslationDocument(ctx, store, layoutID, sourceLocale)
	if err != nil {
		return err
	}
	sourceHTML := dereferenceString(source.ContentHTML)
	units, err := ExtractLayoutContentUnits(sourceHTML)
	if err != nil {
		return errs.WithCause(errs.FailedPrecondition("Email Layout source unit markers require repair before creating a target"), failurecause.ServerFault(err))
	}
	values := make(map[string]string, len(units))
	for _, unit := range units {
		values[unit.Handle] = unit.SourceValue
	}
	targetHTML, targetText, err := ApplyLayoutLocaleValues(sourceHTML, values)
	if err != nil {
		return errs.Internal(err)
	}
	return store.InsertLayoutEntry(ctx, layoutID, locale, targetHTML, targetText, now)
}
