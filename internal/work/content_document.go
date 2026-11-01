package work

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
)

const workContentDocumentProfile = "work"

func parseWorkContentUUID(fieldName string, value string) (uuid.UUID, error) {
	normalized := strings.TrimSpace(value)
	parsed, err := uuid.Parse(normalized)
	if err != nil || parsed == uuid.Nil || parsed.String() != normalized {
		return uuid.Nil, domainerr.InvalidField(fieldName, "must be a canonical UUID")
	}
	return parsed, nil
}

func normalizeWorkContentBlockError(err error) error {
	if err == nil || mutation.IsClassified(err) {
		return err
	}
	// A revision conflict keeps its cause, so a conflict found while storing
	// takes the same errors.Is/As branches (AI document conflict result,
	// translation source no longer current) as one found by the pre-check.
	var targetConflict *translation.TargetRevisionConflict
	if errors.As(err, &targetConflict) {
		return domainerr.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
			targetConflict.Error(), domainerr.CollaborationRevisionsOf(err),
		).WithCause(err)
	}

	switch {
	case errors.Is(err, contentblock.ErrDocumentNotFound):
		return domainerr.New(domainerr.NotFound, "", "work content document not found").WithCause(err)
	case errors.Is(err, contentblock.ErrStaleRevision):
		return domainerr.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
			"work content revision changed; reload before saving", domainerr.CollaborationRevisionsOf(err),
		).WithCause(err)
	case errors.Is(err, contentblock.ErrCrossDocument):
		return domainerr.InvalidField("blocks", "a Block belongs to another document").WithCause(err)
	case errors.Is(err, contentblock.ErrFileReference):
		// The cause stays out of the chain: AI document callers would turn
		// ErrFileReference into an operation issue, and Work has always
		// answered FailedPrecondition here.
		return domainerr.Precondition("a Work Block File reference is not attachable")
	case errors.Is(err, contentblock.ErrInvalidMutation):
		return domainerr.InvalidField("blocks", err.Error())
	default:
		return fmt.Errorf("work content document: %w", err)
	}
}

// loadWorkContentDocumentID reads the Work's content document with lock; a
// missing Work answers not found and one without a document fails its
// precondition.
func loadWorkContentDocumentID(ctx context.Context, store Store, workID string, lock RowLock) (uuid.UUID, error) {
	root, err := store.ContentRoot(ctx, workID, lock)
	if errors.Is(err, ErrWorkNotFound) {
		return uuid.Nil, domainerr.ResourceNotFound("work", workID)
	}
	if err != nil {
		return uuid.Nil, err
	}
	if root.ContentDocumentID == nil || strings.TrimSpace(*root.ContentDocumentID) == "" {
		return uuid.Nil, domainerr.Precondition("work content document is not initialized")
	}
	documentID, err := uuid.Parse(*root.ContentDocumentID)
	if err != nil {
		return uuid.Nil, err
	}
	return documentID, nil
}

// LoadWorkContentDocumentIDForPublicRead share-locks the Work and reads its
// content document, through store bound to the public read's snapshot.
func LoadWorkContentDocumentIDForPublicRead(ctx context.Context, store Store, workID string) (uuid.UUID, error) {
	return loadWorkContentDocumentID(ctx, store, workID, ShareLock)
}

// readWorkContentDocumentID reads the Work's content document before its
// transaction.
func (s *Service) readWorkContentDocumentID(ctx context.Context, workID string) (uuid.UUID, error) {
	var documentID uuid.UUID
	err := s.read(ctx, func(store Store) error {
		var err error
		documentID, err = loadWorkContentDocumentID(ctx, store, workID, Unlocked)
		return err
	})
	return documentID, err
}

// requireWorkLoadPrincipal locks the room's Work, resolves and fences the
// session's principal through lockActive and requires its Edit (or
// EditArchived) before it locks the session.
func requireWorkLoadPrincipal(
	ctx context.Context,
	store Store,
	lockActive func(*auth.UserInfo) (bool, error),
	checker authz.AuthorizationDecisionChecker,
	workID string,
	documentID uuid.UUID,
	sessionID string,
) (*auth.UserInfo, error) {
	if sessionID == "" {
		return nil, domainerr.AuthenticationRequired()
	}
	work, err := lockWorkForContentDocument(ctx, store, documentID)
	if err != nil {
		return nil, err
	}
	if work.ID != workID {
		return nil, domainerr.Precondition("Work content document changed; reload before editing")
	}
	principal, err := store.ResolveSessionPrincipal(ctx, sessionID)
	if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
		return nil, domainerr.AuthenticationRequired().WithCause(err)
	}
	if err != nil {
		return nil, err
	}
	if principal == nil || !principal.Authenticated {
		return nil, domainerr.AuthenticationRequired()
	}
	if principal.Banned {
		return nil, domainerr.AccountBanned()
	}
	if !principal.Onboarded {
		return nil, domainerr.NoPermission("edit", "work")
	}
	active, err := lockActive(principal)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, domainerr.NoPermission("edit", "work")
	}
	action := workLifecycleAction(work.Status, policyv1.Work.Edit, workAuthorizationRead)
	if err := requireWorkPermissionForCurrentActor(auth.WithUser(ctx, principal), checker, workID, action); err != nil {
		return nil, err
	}
	if err := store.LockSession(ctx, sessionID, principal); err != nil {
		if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
			return nil, domainerr.AuthenticationRequired().WithCause(err)
		}
		return nil, fmt.Errorf("lock Work collaboration session: %w", err)
	}
	return principal, nil
}

func lockWorkForContentDocument(ctx context.Context, store Store, documentID uuid.UUID) (Root, error) {
	work, err := store.LockDocumentRoot(ctx, documentID)
	if errors.Is(err, ErrWorkNotFound) {
		return Root{}, domainerr.ResourceNotFound("work", documentID.String())
	}
	return work, err
}

func internalWorkContentFence(contributorMemberIDs []string) ContentFence {
	contributors := append([]string(nil), contributorMemberIDs...)
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		work, err := lockWorkForContentDocument(ctx, store, documentID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if err := store.RequireCurrentContributors(ctx, work.ID, contributors); err != nil {
			return contentblock.DomainContext{}, err
		}
		return loadWorkContentDomainContext(ctx, store, work.ID)
	}
}

func lockedWorkContentFence() ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		work, err := lockWorkForContentDocument(ctx, store, documentID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		return loadWorkContentDomainContext(ctx, store, work.ID)
	}
}

func workContentCreationFence(workID string, sourceLocale string) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		work, err := lockWorkForContentDocument(ctx, store, documentID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if work.ID != workID {
			return contentblock.DomainContext{}, domainerr.Precondition("Work content document changed during creation")
		}
		if strings.TrimSpace(sourceLocale) == "" {
			return contentblock.DomainContext{}, domainerr.Precondition("Work source locale is invalid")
		}
		return contentblock.DomainContext{SourceLocale: sourceLocale}, nil
	}
}

// lockedContentFence answers the domain context a mutation already locked,
// for its own content document only (contentblock.LockedFence).
func lockedContentFence(documentID uuid.UUID, domain contentblock.DomainContext) ContentFence {
	return func(_ context.Context, _ Store, requested uuid.UUID) (contentblock.DomainContext, error) {
		if requested != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition(workEntityName + " content document changed; reload before saving")
		}
		return domain, nil
	}
}

func loadWorkContentDomainContext(ctx context.Context, store Store, workID string) (contentblock.DomainContext, error) {
	sourceLocale, err := store.SourceLocale(ctx, workID)
	if errors.Is(err, ErrWorkNotFound) {
		return contentblock.DomainContext{}, domainerr.Precondition("work translation source is not initialized")
	}
	if err != nil {
		return contentblock.DomainContext{}, err
	}
	if strings.TrimSpace(sourceLocale) == "" {
		return contentblock.DomainContext{}, domainerr.Precondition("work source locale is empty")
	}
	return contentblock.DomainContext{SourceLocale: sourceLocale}, nil
}
