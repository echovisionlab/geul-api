package work

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// switchWorkVersionRestoreSourceLocale changes the Work-owned source-locale
// pointer under the same content-document CAS used by the restore. The
// owning root is the only source-locale authority; no translation lifecycle
// row is created or updated here.
func switchWorkVersionRestoreSourceLocale(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	workID string,
	requestedLocale string,
	expectedDocumentRevision uuid.UUID,
	previousLocale string,
	now time.Time,
) error {
	if tx == nil || store == nil {
		return errs.FailedPrecondition("Work content document is not initialized")
	}
	if strings.TrimSpace(workID) == "" || strings.TrimSpace(requestedLocale) == "" ||
		strings.TrimSpace(previousLocale) == "" || expectedDocumentRevision == uuid.Nil {
		return errs.InvalidArgument("source_locale", "source-locale switch identity is required")
	}
	if previousLocale == requestedLocale {
		return errs.FailedPrecondition("requested locale is already the source locale")
	}
	var root struct {
		ContentDocumentID uuid.UUID `gorm:"column:content_document_id"`
		SourceLocale      string    `gorm:"column:source_locale"`
	}
	result := tx.WithContext(ctx).
		Table("work").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("content_document_id", "source_locale").
		Where("id = ?", workID).
		Take(&root)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return errs.NotFound("work", workID)
	}
	if result.Error != nil {
		return errs.Internal(result.Error)
	}
	if root.ContentDocumentID == uuid.Nil {
		return errs.FailedPrecondition("Work content document is not initialized")
	}
	if root.SourceLocale != previousLocale {
		return errs.FailedPrecondition("Work source locale changed during version restore")
	}
	_, err := store.AdvanceRevision(
		ctx,
		tx,
		contentblock.AdvanceInput{
			DocumentID:       root.ContentDocumentID,
			ExpectedRevision: expectedDocumentRevision,
		},
		func(context.Context, *gorm.DB, uuid.UUID) (contentblock.DomainContext, error) {
			return contentblock.DomainContext{SourceLocale: previousLocale}, nil
		},
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			updated := tx.WithContext(ctx).Table("work").
				Where("id = ? AND source_locale = ?", workID, previousLocale).
				Updates(map[string]any{"source_locale": requestedLocale, "updated_at": now})
			if updated.Error != nil {
				return contentblock.MetadataEffect{}, errs.Internal(updated.Error)
			}
			if updated.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, errs.FailedPrecondition("Work source locale changed during version restore")
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
