package page

import (
	"context"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// switchBlockVersionRestoreSourceLocale changes only the Page source-locale
// pointer before restoring the immutable Version. Existing locale values,
// Block overlays, and Translation jobs remain unchanged.
func switchBlockVersionRestoreSourceLocale(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	entityType string,
	pageID string,
	requestedLocale string,
	expectedDocumentRevision uuid.UUID,
	previousLocale string,
	now time.Time,
) error {
	if tx == nil || store == nil {
		return errs.FailedPrecondition("Page content document is not initialized")
	}
	if strings.TrimSpace(entityType) != "page" || strings.TrimSpace(pageID) == "" ||
		strings.TrimSpace(previousLocale) == "" || strings.TrimSpace(requestedLocale) == "" ||
		expectedDocumentRevision == uuid.Nil {
		return errs.InvalidArgument("source_locale", "source-locale switch identity is required")
	}
	if previousLocale == requestedLocale {
		return errs.FailedPrecondition("requested locale is already the source locale")
	}
	var root struct {
		ContentDocumentID *uuid.UUID `gorm:"column:content_document_id"`
		SourceLocale      string     `gorm:"column:source_locale"`
	}
	if err := tx.WithContext(ctx).Table("page").
		Select("content_document_id", "source_locale").Where("id = ?", pageID).Take(&root).Error; err != nil {
		return errs.Internal(err)
	}
	if root.ContentDocumentID == nil || *root.ContentDocumentID == uuid.Nil {
		return errs.FailedPrecondition("Page content document is not initialized")
	}
	if root.SourceLocale != previousLocale {
		return translation.ErrSourceNoLongerCurrent
	}
	_, err := store.AdvanceRevision(
		ctx,
		tx,
		contentblock.AdvanceInput{
			DocumentID:       *root.ContentDocumentID,
			ExpectedRevision: expectedDocumentRevision,
		},
		func(_ context.Context, _ *gorm.DB, _ uuid.UUID) (contentblock.DomainContext, error) {
			return contentblock.DomainContext{SourceLocale: previousLocale}, nil
		},
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			updated := tx.WithContext(ctx).Exec(
				"UPDATE page SET source_locale = ?, updated_at = ? WHERE id = ? AND source_locale = ?",
				requestedLocale, now, pageID, previousLocale,
			)
			if updated.Error != nil {
				return contentblock.MetadataEffect{}, errs.Internal(updated.Error)
			}
			if updated.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, translation.ErrSourceNoLongerCurrent
			}
			return contentblock.MetadataEffect{
				Changed:                  true,
				AffectsTranslationSource: true,
				SourceLocale:             requestedLocale,
			}, nil
		},
	)
	return err
}
