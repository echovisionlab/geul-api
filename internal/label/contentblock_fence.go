package label

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	"github.com/echovisionlab/geul-api/internal/translation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func loadLabelContentDocumentID(ctx context.Context, db *gorm.DB, labelID string) (uuid.UUID, error) {
	var row struct {
		DocumentID *uuid.UUID `gorm:"column:content_document_id"`
	}
	result := db.WithContext(ctx).Table("label").Select("content_document_id").Where("id = ?", labelID).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return uuid.Nil, errs.NotFound("label", labelID)
	}
	if result.Error != nil {
		return uuid.Nil, errs.Internal(result.Error)
	}
	if row.DocumentID == nil || *row.DocumentID == uuid.Nil {
		return uuid.Nil, errs.FailedPrecondition("label content document is not initialized")
	}
	return *row.DocumentID, nil
}

func lockLabelContentDocumentRoot(ctx context.Context, tx *gorm.DB, labelID string, expectedDocumentID uuid.UUID) error {
	var row struct {
		DocumentID *uuid.UUID `gorm:"column:content_document_id"`
	}
	result := tx.WithContext(ctx).Table("label").Clauses(clause.Locking{Strength: "UPDATE"}).Select("content_document_id").Where("id = ?", labelID).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return errs.NotFound("label", labelID)
	}
	if result.Error != nil {
		return errs.Internal(result.Error)
	}
	if row.DocumentID == nil || *row.DocumentID == uuid.Nil || *row.DocumentID != expectedDocumentID {
		return errs.FailedPrecondition("label content document changed; reload before saving")
	}
	return nil
}

func lockLabelTranslationSourceContext(ctx context.Context, tx *gorm.DB, labelID string) (contentblock.DomainContext, error) {
	var row struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	result := tx.WithContext(ctx).Table("label").Clauses(clause.Locking{Strength: "UPDATE"}).Select("source_locale").Where("id = ?", labelID).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return contentblock.DomainContext{}, errs.NotFound("label", labelID)
	}
	if result.Error != nil {
		return contentblock.DomainContext{}, errs.Internal(result.Error)
	}
	return contentblock.DomainContext{SourceLocale: row.SourceLocale}, nil
}

func internalLabelContentFence(labelID string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockLabelContentDocumentRoot(ctx, tx, labelID, documentID); err != nil {
			return contentblock.DomainContext{}, err
		}
		return lockLabelTranslationSourceContext(ctx, tx, labelID)
	}
}

func internalLabelCollaborationContentFence(
	checkpoints persistencecheckpoint.ContributorFence,
	labelID string,
	contributors []string,
) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockLabelContentDocumentRoot(ctx, tx, labelID, documentID); err != nil {
			return contentblock.DomainContext{}, err
		}
		var root labelAIDocumentRoot
		if err := tx.WithContext(ctx).Table("label").
			Select("id::text AS id", "status", "content_document_id").
			Where("id = ?::uuid", labelID).
			Take(&root).Error; err != nil {
			return contentblock.DomainContext{}, errs.Internal(err)
		}
		if !labelAIDocumentLifecycleValid(root.Status) {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Label lifecycle no longer permits collaboration editing")
		}
		if err := checkpoints.RequireCurrentContributors(
			ctx,
			tx,
			intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_LABEL,
			labelID,
			contributors,
		); err != nil {
			return contentblock.DomainContext{}, err
		}
		return lockLabelTranslationSourceContext(ctx, tx, labelID)
	}
}

func lockedLabelContentFence(
	documentID uuid.UUID,
	domain contentblock.DomainContext,
) contentblock.DomainFence {
	return func(_ context.Context, _ *gorm.DB, requestedDocumentID uuid.UUID) (contentblock.DomainContext, error) {
		if requestedDocumentID != documentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Label content document changed; reload before saving")
		}
		return domain, nil
	}
}

func labelContentDocumentFence(labelID string, authorize func(context.Context, *gorm.DB) error) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockLabelContentDocumentRoot(ctx, tx, labelID, documentID); err != nil {
			return contentblock.DomainContext{}, err
		}
		if authorize == nil {
			return contentblock.DomainContext{}, errs.Internal(errors.New("label content document authorization is required"))
		}
		if err := authorize(ctx, tx); err != nil {
			return contentblock.DomainContext{}, err
		}
		return contentblock.DomainContext{}, nil
	}
}

func labelContentCreationFence(labelID, sourceLocale string, authorize func(context.Context, *gorm.DB) error) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockLabelContentDocumentRoot(ctx, tx, labelID, documentID); err != nil {
			return contentblock.DomainContext{}, err
		}
		if authorize == nil {
			return contentblock.DomainContext{}, errs.Internal(errors.New("label content document authorization is required"))
		}
		if err := authorize(ctx, tx); err != nil {
			return contentblock.DomainContext{}, err
		}
		return contentblock.DomainContext{SourceLocale: sourceLocale}, nil
	}
}

func normalizeLabelContentBlockError(err error) error {
	if err == nil || connect.CodeOf(err) != connect.CodeUnknown {
		return err
	}
	var targetConflict *translation.TargetRevisionConflict
	switch {
	case errors.As(err, &targetConflict):
		return errs.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
			"Label target locale changed since it was loaded; reload before saving",
		)
	case errors.Is(err, contentblock.ErrDocumentNotFound):
		return errs.NotFoundMsg("label content document not found")
	case errors.Is(err, contentblock.ErrStaleRevision):
		return errs.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
			"Label document changed since it was loaded; reload before saving",
		)
	case errors.Is(err, contentblock.ErrCrossDocument):
		return errs.InvalidArgument("blocks", "a Block belongs to another document")
	case errors.Is(err, contentblock.ErrFileReference):
		return errs.InvalidArgument("blocks", "compact documents cannot contain File Blocks")
	case errors.Is(err, contentblock.ErrInvalidMutation):
		return errs.InvalidArgument("blocks", err.Error())
	default:
		return errs.Internal(fmt.Errorf("label content document: %w", err))
	}
}
