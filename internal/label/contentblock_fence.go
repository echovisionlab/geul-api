package label

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/translation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
)

// ContentFence runs before the content store locks or changes the Label's
// document rows (contentblock.DomainFence): it locks the Label root in the
// same transaction, rechecks lifecycle and authority, and names the source
// locale.
type ContentFence func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error)

// lockedContentFence admits only documentID and answers with the domain
// context the caller already authorized under its lock
// (contentblock.LockedFence).
func lockedContentFence(documentID uuid.UUID, domain contentblock.DomainContext) ContentFence {
	return func(_ context.Context, _ Store, requested uuid.UUID) (contentblock.DomainContext, error) {
		if requested != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition(labelEntityName + " content document changed; reload before saving")
		}
		return domain, nil
	}
}

// loadLabelContentDocumentID reads the Label's content document; a missing
// Label answers not found.
func loadLabelContentDocumentID(ctx context.Context, store Store, labelID string) (uuid.UUID, error) {
	documentID, err := store.ContentDocumentID(ctx, labelID, Unlocked)
	if errors.Is(err, ErrLabelNotFound) {
		return uuid.Nil, domainerr.ResourceNotFound("label", labelID)
	}
	if err != nil {
		return uuid.Nil, err
	}
	if documentID == nil || *documentID == uuid.Nil {
		return uuid.Nil, domainerr.Precondition("label content document is not initialized")
	}
	return *documentID, nil
}

// readLabelContentDocumentID is loadLabelContentDocumentID in its own read
// snapshot, for a use case that names the document before its mutation.
func (s *Service) readLabelContentDocumentID(ctx context.Context, labelID string) (uuid.UUID, error) {
	var documentID uuid.UUID
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		documentID, err = loadLabelContentDocumentID(ctx, store, labelID)
		return err
	})
	return documentID, err
}

func lockLabelContentDocumentRoot(ctx context.Context, store Store, labelID string, expectedDocumentID uuid.UUID) error {
	documentID, err := store.ContentDocumentID(ctx, labelID, UpdateLock)
	if errors.Is(err, ErrLabelNotFound) {
		return domainerr.ResourceNotFound("label", labelID)
	}
	if err != nil {
		return err
	}
	if documentID == nil || *documentID == uuid.Nil || *documentID != expectedDocumentID {
		return domainerr.Precondition("label content document changed; reload before saving")
	}
	return nil
}

func lockLabelTranslationSourceContext(ctx context.Context, store Store, labelID string) (contentblock.DomainContext, error) {
	sourceLocale, err := loadLabelSourceLocale(ctx, store, labelID, UpdateLock)
	if err != nil {
		return contentblock.DomainContext{}, err
	}
	return contentblock.DomainContext{SourceLocale: sourceLocale}, nil
}

func internalLabelContentFence(labelID string) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockLabelContentDocumentRoot(ctx, store, labelID, documentID); err != nil {
			return contentblock.DomainContext{}, err
		}
		return lockLabelTranslationSourceContext(ctx, store, labelID)
	}
}

func internalLabelCollaborationContentFence(labelID string, contributors []string) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockLabelContentDocumentRoot(ctx, store, labelID, documentID); err != nil {
			return contentblock.DomainContext{}, err
		}
		root, err := store.DocumentRoot(ctx, labelID, Unlocked)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if !labelAIDocumentLifecycleValid(root.Status) {
			return contentblock.DomainContext{}, domainerr.Precondition("Label lifecycle no longer permits collaboration editing")
		}
		if err := store.RequireCurrentContributors(ctx, labelID, contributors); err != nil {
			return contentblock.DomainContext{}, err
		}
		return lockLabelTranslationSourceContext(ctx, store, labelID)
	}
}

// labelContentDocumentFence locks the Label row of the document the
// mutation, already authorized, removes.
func labelContentDocumentFence(labelID string) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		return contentblock.DomainContext{}, lockLabelContentDocumentRoot(ctx, store, labelID, documentID)
	}
}

// labelContentCreationFence locks the new Label row and names its source
// locale while the creation, already authorized, fills its document.
func labelContentCreationFence(labelID, sourceLocale string) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockLabelContentDocumentRoot(ctx, store, labelID, documentID); err != nil {
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
		return domainerr.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
			"Label target locale changed since it was loaded; reload before saving", domainerr.CollaborationRevisionsOf(err),
		)
	case errors.Is(err, contentblock.ErrDocumentNotFound):
		return domainerr.New(domainerr.NotFound, "", "label content document not found").WithCause(err)
	case errors.Is(err, contentblock.ErrStaleRevision):
		return domainerr.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
			"Label document changed since it was loaded; reload before saving", domainerr.CollaborationRevisionsOf(err),
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
