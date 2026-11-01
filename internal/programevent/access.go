package programevent

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type programEventAction = auth.ResourceAction

// LockActive locks the principal's Member and Identity in the caller's
// transaction and reports whether they are active.
type LockActive func(*auth.UserInfo) (bool, error)

func authenticationRequired() *domainerr.Error {
	return domainerr.New(domainerr.Unauthenticated, "", "authentication required")
}

func noPermission(action string) *domainerr.Error {
	return domainerr.Denied(fmt.Sprintf("no permission to %s this program event", action))
}

func eventNotFound(eventID string) *domainerr.Error {
	return domainerr.ResourceNotFound("program event", eventID)
}

// notFoundAs answers a missing event as answer and passes every other error
// through.
func notFoundAs(err error, answer error) error {
	if errors.Is(err, ErrEventNotFound) {
		return answer
	}
	return err
}

// rejected reports whether err is a use-case rejection of kind, so a caller
// can answer it differently (an unreadable event answers not found).
func rejected(err error, kinds ...domainerr.Kind) bool {
	var outcome *domainerr.Error
	if !errors.As(err, &outcome) {
		return false
	}
	for _, kind := range kinds {
		if outcome.Kind == kind {
			return true
		}
	}
	return false
}

func requireProgramEventPermissionForSubject(
	ctx context.Context,
	checker ActorChecker,
	eventID string,
	action programEventAction,
	subject auth.AccountIdentitySubject,
) error {
	can, err := action(eventID)
	if err != nil {
		return domainerr.InvalidField("resource.id", "must be a canonical resource UUID").WithCause(err)
	}
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	if err != nil {
		return authenticationRequired().WithCause(err)
	}
	allowed, err := checker.CheckActorCan(ctx, actor, can)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return noPermission(can.Action().Name())
	}
	return nil
}

func requireProgramEventPermission(
	ctx context.Context,
	checker authz.AuthorizationDecisionChecker,
	eventID string,
	action programEventAction,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || !principal.Onboarded || principal.Banned {
		return authenticationRequired()
	}
	can, err := action(eventID)
	if err != nil {
		return domainerr.InvalidField("resource.id", "must be a canonical resource UUID").WithCause(err)
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return authenticationRequired().WithCause(err)
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return noPermission(can.Action().Name())
	}
	return nil
}

func programEventViewAction(status Status) programEventAction {
	if status == StatusArchived {
		return policyv1.ProgramEvent.ViewArchived
	}
	return policyv1.ProgramEvent.View
}

func programEventMutationAction(status Status, normal programEventAction) programEventAction {
	if status == StatusArchived {
		return policyv1.ProgramEvent.EditArchived
	}
	return normal
}

// requireActiveProgramEventPrincipal locks the principal's Member and
// Identity and requires them to stay active. maskNotFound answers an
// inactive principal as a missing event, for reads.
func requireActiveProgramEventPrincipal(
	ctx context.Context,
	lockActive LockActive,
	actionName string,
	maskNotFound bool,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return authenticationRequired()
	}
	active, err := lockActive(principal)
	if err != nil {
		return err
	}
	if active {
		return nil
	}
	if maskNotFound {
		return domainerr.New(domainerr.NotFound, "", "program event not found")
	}
	return noPermission(actionName)
}

// readLockActive is the identity fence of a read, through the Store of its
// snapshot.
func readLockActive(ctx context.Context, store Store) LockActive {
	return func(principal *auth.UserInfo) (bool, error) {
		return store.LockActivePrincipal(ctx, principal)
	}
}

// authorizeLockedProgramEvent requires the active principal to hold normal,
// or EditArchived while the locked event is archived.
func authorizeLockedProgramEvent(
	ctx context.Context,
	lockActive LockActive,
	checker authz.AuthorizationDecisionChecker,
	eventID string,
	status Status,
	actionName string,
	normal programEventAction,
) error {
	if err := requireActiveProgramEventPrincipal(ctx, lockActive, actionName, false); err != nil {
		return err
	}
	return requireProgramEventPermission(ctx, checker, eventID, programEventMutationAction(status, normal))
}

// lockProgramEventForMutation serializes a Program Event mutation with
// lifecycle transitions. Archived is a visibility state; its general
// mutations remain permitted with EditArchived.
func lockProgramEventForMutation(ctx context.Context, store Store, eventID string) error {
	return notFoundAs(store.LockEventID(ctx, eventID), eventNotFound(eventID))
}

// requireLockedProgramEventAction reads the locked event's lifecycle and
// requires the active principal to hold normal on it.
func requireLockedProgramEventAction(
	ctx context.Context,
	store Store,
	lockActive LockActive,
	checker authz.AuthorizationDecisionChecker,
	eventID string,
	normal programEventAction,
) error {
	status, err := store.EventStatus(ctx, eventID, UpdateLock)
	if err != nil {
		return notFoundAs(err, eventNotFound(eventID))
	}
	can, err := normal(eventID)
	if err != nil {
		return domainerr.InvalidField("resource.id", "must be a canonical resource UUID").WithCause(err)
	}
	return authorizeLockedProgramEvent(ctx, lockActive, checker, eventID, status, can.Action().Name(), normal)
}

// lockedEventEdit is the Lock and Access of a mutation of one Program Event
// and its children: the event row is locked before the fresh check of
// normal, so a role revoked while the mutation waited on the row is
// observed and a missing event answers not found first.
func (s *Service) lockedEventEdit(eventID string, normal programEventAction) (func(context.Context, Store) error, mutation.Access) {
	lock := func(ctx context.Context, store Store) error {
		return lockProgramEventForMutation(ctx, store, eventID)
	}
	return lock, mutation.Fenced(func(ctx context.Context, store Store, lockActive func(*auth.UserInfo) (bool, error)) error {
		return requireLockedProgramEventAction(ctx, store, lockActive, s.spiceDB, eventID, normal)
	})
}

// RequireLockedSourceLocaleEdit authorizes a source locale edit of the event
// inside another domain's transaction; lockActive is that transaction's
// identity fence.
func RequireLockedSourceLocaleEdit(
	ctx context.Context,
	store Store,
	checker authz.AuthorizationDecisionChecker,
	lockActive LockActive,
	eventID string,
) error {
	return requireLockedProgramEventAction(ctx, store, lockActive, checker, eventID, policyv1.ProgramEvent.Edit)
}

// RequireLockedView authorizes a read of the event inside another domain's
// transaction; an unreadable event answers not found.
func RequireLockedView(
	ctx context.Context,
	store Store,
	checker authz.AuthorizationDecisionChecker,
	lockActive LockActive,
	eventID string,
) error {
	status, err := store.EventStatus(ctx, eventID, ShareLock)
	if err != nil {
		return notFoundAs(err, eventNotFound(eventID))
	}
	if err := requireActiveProgramEventPrincipal(ctx, lockActive, "view", true); err != nil {
		return err
	}
	if err := requireProgramEventPermission(ctx, checker, eventID, programEventViewAction(status)); err != nil {
		if rejected(err, domainerr.PermissionDenied) {
			return eventNotFound(eventID).WithCause(err)
		}
		return err
	}
	return nil
}
