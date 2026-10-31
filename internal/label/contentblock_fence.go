package label

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/collaborationconflict"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
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
		return uuid.Nil, domainerr.ResourceNotFound("label", labelID)
	}
	if result.Error != nil {
		return uuid.Nil, result.Error
	}
	if row.DocumentID == nil || *row.DocumentID == uuid.Nil {
		return uuid.Nil, domainerr.Precondition("label content document is not initialized")
	}
	return *row.DocumentID, nil
}

func lockLabelContentDocumentRoot(ctx context.Context, tx *gorm.DB, labelID string, expectedDocumentID uuid.UUID) error {
	var row struct {
		DocumentID *uuid.UUID `gorm:"column:content_document_id"`
	}
	result := tx.WithContext(ctx).Table("label").Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("content_document_id").Where("id = ?", labelID).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return domainerr.ResourceNotFound("label", labelID)
	}
	if result.Error != nil {
		return result.Error
	}
	if row.DocumentID == nil || *row.DocumentID == uuid.Nil || *row.DocumentID != expectedDocumentID {
		return domainerr.Precondition("label content document changed; reload before saving")
	}
	return nil
}

func lockLabelTranslationSourceContext(ctx context.Context, tx *gorm.DB, labelID string) (contentblock.DomainContext, error) {
	var row struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	result := tx.WithContext(ctx).Table("label").Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("source_locale").Where("id = ?", labelID).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return contentblock.DomainContext{}, domainerr.ResourceNotFound("label", labelID)
	}
	if result.Error != nil {
		return contentblock.DomainContext{}, result.Error
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
			return contentblock.DomainContext{}, err
		}
		if !labelAIDocumentLifecycleValid(root.Status) {
			return contentblock.DomainContext{}, domainerr.Precondition("Label lifecycle no longer permits collaboration editing")
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

// labelContentDocumentFence locks the Label row of the document the
// mutation, already authorized, removes.
func labelContentDocumentFence(labelID string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		return contentblock.DomainContext{}, lockLabelContentDocumentRoot(ctx, tx, labelID, documentID)
	}
}

// labelContentCreationFence locks the new Label row and names its source
// locale while the creation, already authorized, fills its document.
func labelContentCreationFence(labelID, sourceLocale string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockLabelContentDocumentRoot(ctx, tx, labelID, documentID); err != nil {
			return contentblock.DomainContext{}, err
		}
		return contentblock.DomainContext{SourceLocale: sourceLocale}, nil
	}
}

// normalizeLabelContentBlockError gives a raw content document failure its
// client answer; any other error keeps its own.
func normalizeLabelContentBlockError(err error) error {
	if err == nil {
		return nil
	}
	if _, classified := errors.AsType[*domainerr.Error](err); classified {
		return err
	}
	// A revision conflict carries its current revisions in the conflict
	// detail and does not keep its cause: callers branch on errors.Is/As for
	// raw stale and target-revision errors, and an already-normalized
	// conflict must not take those branches.
	var targetConflict *translation.TargetRevisionConflict
	switch {
	case errors.As(err, &targetConflict):
		return collaborationconflict.Conflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
			"Label target locale changed since it was loaded; reload before saving", collaborationState(err),
		)
	case errors.Is(err, contentblock.ErrDocumentNotFound):
		return domainerr.New(domainerr.NotFound, "", "label content document not found").WithCause(err)
	case errors.Is(err, contentblock.ErrStaleRevision):
		return collaborationconflict.Conflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
			"Label document changed since it was loaded; reload before saving", collaborationState(err),
		)
	case errors.Is(err, contentblock.ErrCrossDocument):
		return domainerr.InvalidField("blocks", "a Block belongs to another document").WithCause(err)
	case errors.Is(err, contentblock.ErrFileReference):
		return domainerr.InvalidField("blocks", "compact documents cannot contain File Blocks").WithCause(err)
	case errors.Is(err, contentblock.ErrInvalidMutation):
		return domainerr.InvalidField("blocks", err.Error())
	default:
		return fmt.Errorf("label content document: %w", err)
	}
}

// collaborationState is the revision state a conflict cause reports.
func collaborationState(err error) collaborationconflict.State {
	var revisions interface {
		CollaborationRevisions() (string, string, *bool)
	}
	if !errors.As(err, &revisions) {
		return collaborationconflict.State{}
	}
	document, target, exists := revisions.CollaborationRevisions()
	return collaborationconflict.State{Document: document, Target: target, TargetExists: exists}
}
