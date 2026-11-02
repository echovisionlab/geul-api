package page

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// LoadTranslationSourceDocument returns the Page-owned typed document used to
// extract translation units, read through store in the translation domain's
// transaction.
func LoadTranslationSourceDocument(ctx context.Context, store Store, pageID string) (*translation.SourceDocument, error) {
	if store == nil {
		return nil, errors.New("page translation Content Block store is not configured")
	}
	metadata, err := loadRequiredPageSourceLocaleMetadata(ctx, store, pageID)
	if err != nil {
		return nil, err
	}
	documentID, err := loadPageContentDocumentID(ctx, store, pageID, Unlocked)
	if err != nil {
		return nil, err
	}
	snapshot, err := store.ContentSnapshot(ctx, documentID, metadata.Locale)
	if err != nil {
		return nil, normalizePageContentBlockError(err)
	}
	document, err := contentblock.SnapshotToLocalizedPageDocument(snapshot, metadata.Locale)
	if err != nil {
		return nil, normalizePageContentBlockError(err)
	}
	return &translation.SourceDocument{
		Title:                   derefString(metadata.Title),
		Summary:                 metadata.Summary,
		ContentDocumentRevision: snapshot.Document.Revision.String(),
		PageDocument:            document,
	}, nil
}

// validatePageSourceLocaleMutations checks explicit locale writes in a source
// request. Structural deletion may also remove target overlays, so the Store's
// ChangedLocales result is not an authorization input.
func validatePageSourceLocaleMutations(
	pageID string,
	sourceLocale string,
	groups []contentblock.LocaleMutationGroup,
) error {
	if strings.TrimSpace(pageID) == "" || strings.TrimSpace(sourceLocale) == "" {
		return errors.New("invalid Page source locale")
	}

	for _, group := range groups {
		locale := strings.TrimSpace(group.Locale)
		if locale == "" {
			return errors.New("page target locale is empty")
		}
		if locale != sourceLocale {
			return domainerr.Precondition("target Page locale documents are read-only")
		}
	}
	return nil
}

// upsertTranslationMetadataEntry persists Page target metadata. The Page body
// remains exclusively in typed Content Blocks.
func upsertTranslationMetadataEntry(
	ctx context.Context,
	store Store,
	pageID string,
	locale string,
	input translation.EntryWrite,
) error {
	if len(input.ContentJSON) > 0 || input.ContentHTML != nil || input.ContentText != nil {
		return domainerr.Precondition("Page translation body is stored as typed Content Blocks")
	}
	now := input.Now.UTC()
	return store.UpsertLocale(ctx, pageID, locale, input.Title, input.Summary, now)
}
