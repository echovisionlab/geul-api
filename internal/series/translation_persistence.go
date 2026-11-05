package series

import (
	"context"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
)

// ProviderTranslationJob is the translation job a provider candidate
// answers.
type ProviderTranslationJob struct {
	EntityType          string
	EntityID            string
	TargetLocale        string
	RequestedByMemberID string
}

// sameCopy reports whether next carries the same title, summary and text.
func (t Translation) sameCopy(next Translation) bool {
	return equalOptionalString(t.Title, next.Title) &&
		equalOptionalString(t.Summary, next.Summary) &&
		equalOptionalString(t.ContentText, next.ContentText)
}

// saveSourceCopy persists the authoritative Series copy, makes it the only
// source-locale row, and keeps the Series root projection aligned in the
// same transaction.
func saveSourceCopy(
	ctx context.Context,
	store TranslationStore,
	seriesID string,
	locale string,
	title *string,
	summary *string,
	now time.Time,
) error {
	if title == nil || strings.TrimSpace(*title) == "" {
		return domainerr.Required("title")
	}
	if err := store.UpsertTranslation(ctx, seriesID, locale, TranslationWrite{
		Title:       title,
		Summary:     summary,
		ContentText: summary,
		Now:         now,
	}); err != nil {
		return err
	}
	return store.SetSourceLocale(ctx, seriesID, locale, now)
}
