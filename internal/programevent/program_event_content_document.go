package programevent

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/translation"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
)

const programEventContentProfile = "program_event"

func loadProgramEventContentDocumentID(
	ctx context.Context,
	store Store,
	eventID string,
	lock RowLock,
) (uuid.UUID, error) {
	reference, err := store.ContentDocumentID(ctx, eventID, lock)
	if err != nil {
		return uuid.Nil, notFoundAs(err, domainerr.ResourceNotFound("program event", eventID))
	}
	if reference == nil || *reference == "" {
		return uuid.Nil, domainerr.Precondition("Program Event content document is not initialized")
	}
	documentID, err := uuid.Parse(*reference)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid Program Event content_document_id: %w", err)
	}
	return documentID, nil
}

// programEventContentDocumentFence locks the event root, requires it to
// still own the document and runs authorize before the content rows lock.
func programEventContentDocumentFence(
	eventID string,
	authorize func(context.Context, Store) error,
) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		reference, err := store.ContentDocumentID(ctx, eventID, UpdateLock)
		if err != nil {
			return contentblock.DomainContext{}, notFoundAs(err, domainerr.ResourceNotFound("program event", eventID))
		}
		if reference == nil || *reference != documentID.String() {
			return contentblock.DomainContext{}, domainerr.Precondition("Program Event content document ownership changed; reload before saving")
		}
		if authorize == nil {
			return contentblock.DomainContext{}, errors.New("program Event content document authorization is required")
		}
		if err := authorize(ctx, store); err != nil {
			return contentblock.DomainContext{}, err
		}
		sourceState, err := loadProgramEventSourceLocale(ctx, store, eventID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		return contentblock.DomainContext{SourceLocale: sourceState.SourceLocale}, nil
	}
}

func requireProgramEventBlockBootstrap(
	ctx context.Context,
	store Store,
	spiceDB ActorChecker,
	eventID string,
	sessionID *string,
) (uuid.UUID, *auth.UserInfo, error) {
	if sessionID == nil {
		return uuid.Nil, nil, authenticationRequired()
	}
	documentID, err := loadProgramEventContentDocumentID(ctx, store, eventID, UpdateLock)
	if err != nil {
		return uuid.Nil, nil, err
	}
	if err := RequireExists(ctx, store, eventID); err != nil {
		return uuid.Nil, nil, err
	}
	principal, err := store.ResolveSessionPrincipal(ctx, *sessionID)
	if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
		return uuid.Nil, nil, authenticationRequired().WithCause(err)
	}
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("resolve Program Event collaboration principal: %w", err)
	}
	if principal == nil || !principal.Authenticated {
		return uuid.Nil, nil, authenticationRequired()
	}
	if principal.Banned {
		return uuid.Nil, nil, domainerr.Denied("account is banned")
	}
	if !principal.Onboarded {
		return uuid.Nil, nil, noPermission("edit")
	}
	active, err := store.LockActivePrincipal(ctx, principal)
	if err != nil {
		return uuid.Nil, nil, err
	}
	if !active {
		return uuid.Nil, nil, noPermission("edit")
	}
	subject, err := auth.NewAccountIdentitySubject(principal.IdentityID)
	if err != nil {
		return uuid.Nil, nil, authenticationRequired().WithCause(err)
	}
	status, err := store.EventStatus(ctx, eventID, NoLock)
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("load Program Event collaboration status: %w", err)
	}
	if err := requireProgramEventPermissionForSubject(
		ctx, spiceDB, eventID, programEventViewAction(status), subject,
	); err != nil {
		return uuid.Nil, nil, err
	}
	if err := store.LockSession(ctx, *sessionID, principal); err != nil {
		if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
			return uuid.Nil, nil, authenticationRequired().WithCause(err)
		}
		return uuid.Nil, nil, fmt.Errorf("lock Program Event collaboration session: %w", err)
	}
	return documentID, principal, nil
}

func normalizeProgramEventContentBlockError(err error) error {
	if err == nil || rejected(err, domainerr.Invalid, domainerr.NotFound, domainerr.AlreadyExists,
		domainerr.FailedPrecondition, domainerr.PermissionDenied, domainerr.Unauthenticated, domainerr.Unavailable) {
		return err
	}
	// A revision conflict keeps its cause, so a conflict found while storing
	// takes the same errors.Is/As branches (AI document conflict result,
	// translation source no longer current) as one found by the pre-check.
	var targetConflict *translation.TargetRevisionConflict
	if errors.As(err, &targetConflict) {
		return errors.Join(domainerr.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
			targetConflict.Error(), domainerr.CollaborationRevisionsOf(err),
		), err)
	}

	switch {
	case errors.Is(err, contentblock.ErrDocumentNotFound):
		return domainerr.New(domainerr.NotFound, "", "Program Event content document not found").WithCause(err)
	case errors.Is(err, contentblock.ErrStaleRevision):
		return errors.Join(domainerr.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
			"Program Event content revision changed; reload before saving", domainerr.CollaborationRevisionsOf(err),
		), err)
	case errors.Is(err, contentblock.ErrCrossDocument):
		return domainerr.InvalidField("blocks", "a Block belongs to another document").WithCause(err)
	case errors.Is(err, contentblock.ErrFileReference):
		return domainerr.InvalidField("blocks", err.Error())
	case errors.Is(err, contentblock.ErrInvalidMutation):
		return domainerr.InvalidField("blocks", err.Error())
	default:
		return fmt.Errorf("program Event content document: %w", err)
	}
}
