package page

import (
	"context"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/translation"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"gorm.io/gorm"
)

// LoadTranslationSourceDocument returns the Page-owned typed document used to
// extract translation units.
func LoadTranslationSourceDocument(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	pageID string,
) (*translation.SourceDocument, error) {
	if store == nil {
		return nil, errs.Internal(fmt.Errorf("page translation Content Block store is not configured"))
	}
	metadata, err := loadRequiredPageSourceLocaleMetadata(ctx, db, pageID)
	if err != nil {
		return nil, err
	}
	documentID, err := loadPageContentDocumentID(ctx, db, pageID)
	if err != nil {
		return nil, err
	}
	snapshot, err := store.LoadSnapshot(ctx, db, documentID, metadata.Locale)
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

// ValidateSourceLocaleChanges rejects shared-runtime attempts to write Page
// target documents through the source synchronization path.
func ValidateSourceLocaleChanges(
	_ context.Context,
	_ *gorm.DB,
	pageID string,
	sourceLocale string,
	changedLocales []string,
) error {
	if strings.TrimSpace(pageID) == "" || strings.TrimSpace(sourceLocale) == "" {
		return errs.Internal(fmt.Errorf("invalid Page source locale"))
	}

	for _, locale := range changedLocales {
		locale = strings.TrimSpace(locale)
		if locale == "" {
			return errs.Internal(fmt.Errorf("page target locale is empty"))
		}
		if locale != sourceLocale {
			return errs.FailedPrecondition("target Page locale documents are read-only")
		}
	}
	return nil
}

// UpsertTranslationMetadataEntry persists Page target metadata. The Page body
// remains exclusively in typed Content Blocks.
func UpsertTranslationMetadataEntry(
	ctx context.Context,
	db *gorm.DB,
	pageID string,
	locale string,
	input translation.EntryWrite,
) error {
	if len(input.ContentJSON) > 0 || input.ContentHTML != nil || input.ContentText != nil {
		return errs.FailedPrecondition("Page translation body is stored as typed Content Blocks")
	}
	now := input.Now.UTC()
	if err := db.WithContext(ctx).Exec(
		`INSERT INTO page_translation (
			entity_id, locale, title, summary, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO UPDATE SET
			title = EXCLUDED.title,
			summary = EXCLUDED.summary,
			updated_at = EXCLUDED.updated_at`,
		pageID, locale, input.Title, input.Summary, now, now,
	).Error; err != nil {
		return errs.Internal(err)
	}
	return nil
}
