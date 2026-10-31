package programevent

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type programEventAction = auth.ResourceAction

func authenticationRequired() *domainerr.Error {
	return domainerr.New(domainerr.Unauthenticated, "", "authentication required")
}

func noPermission(action string) *domainerr.Error {
	return domainerr.Denied(fmt.Sprintf("no permission to %s this program event", action))
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
	checker CollaborationPermissionChecker,
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
	checker CollaborationPermissionChecker,
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

func programEventViewAction(status model.ProgramEventStatus) programEventAction {
	if status == model.ProgramEventStatusArchived {
		return policyv1.ProgramEvent.ViewArchived
	}
	return policyv1.ProgramEvent.View
}

func programEventMutationAction(status model.ProgramEventStatus, normal programEventAction) programEventAction {
	if status == model.ProgramEventStatusArchived {
		return policyv1.ProgramEvent.EditArchived
	}
	return normal
}

// requireActiveProgramEventPrincipal locks the principal's Member and
// Identity and requires them to stay active. maskNotFound answers an
// inactive principal as a missing event, for reads.
func requireActiveProgramEventPrincipal(
	ctx context.Context,
	tx *gorm.DB,
	actionName string,
	maskNotFound bool,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return authenticationRequired()
	}
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
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

// authorizeLockedProgramEvent requires the active principal to hold normal,
// or EditArchived while the locked event is archived.
func authorizeLockedProgramEvent(
	ctx context.Context,
	tx *gorm.DB,
	checker CollaborationPermissionChecker,
	eventID string,
	status model.ProgramEventStatus,
	actionName string,
	normal programEventAction,
) error {
	if err := requireActiveProgramEventPrincipal(ctx, tx, actionName, false); err != nil {
		return err
	}
	return requireProgramEventPermission(ctx, checker, eventID, programEventMutationAction(status, normal))
}

// lockProgramEventForMutation serializes a Program Event mutation with
// lifecycle transitions. Archived is a visibility state; its general
// mutations remain permitted with EditArchived.
func lockProgramEventForMutation(ctx context.Context, db *gorm.DB, eventID string) error {
	var event struct {
		ID string `gorm:"column:id"`
	}
	if err := db.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Table("program_event").
		Select("id").
		Where("id = ?", eventID).
		Take(&event).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.ResourceNotFound("program event", eventID)
		}
		return err
	}
	return nil
}

// requireLockedProgramEventAction reads the locked event's lifecycle and
// requires the active principal to hold normal on it.
func requireLockedProgramEventAction(
	ctx context.Context,
	db *gorm.DB,
	checker CollaborationPermissionChecker,
	eventID string,
	normal programEventAction,
) error {
	var event struct {
		Status model.ProgramEventStatus `gorm:"column:status"`
	}
	if err := db.WithContext(ctx).Table("program_event").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("status").Where("id = ?", eventID).Take(&event).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.ResourceNotFound("program event", eventID)
		}
		return err
	}
	can, err := normal(eventID)
	if err != nil {
		return domainerr.InvalidField("resource.id", "must be a canonical resource UUID").WithCause(err)
	}
	return authorizeLockedProgramEvent(ctx, db, checker, eventID, event.Status, can.Action().Name(), normal)
}

// lockedEventEdit is the Lock and Access of a mutation of one Program Event
// and its children: the event row is locked before the fresh check of
// normal, so a role revoked while the mutation waited on the row is
// observed and a missing event answers not found first.
func (s *Service) lockedEventEdit(eventID string, normal programEventAction) (func(context.Context, *gorm.DB) error, mutation.Access) {
	lock := func(ctx context.Context, tx *gorm.DB) error {
		return lockProgramEventForMutation(ctx, tx, eventID)
	}
	return lock, mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
		return requireLockedProgramEventAction(ctx, tx, s.spiceDB, eventID, normal)
	})
}

// RequireLockedSourceLocaleEdit authorizes a source locale edit of the event
// inside another domain's transaction.
func RequireLockedSourceLocaleEdit(
	ctx context.Context,
	tx *gorm.DB,
	checker CollaborationPermissionChecker,
	eventID string,
) error {
	return requireLockedProgramEventAction(ctx, tx, checker, eventID, policyv1.ProgramEvent.Edit)
}

// RequireLockedView authorizes a read of the event inside another domain's
// transaction; an unreadable event answers not found.
func RequireLockedView(
	ctx context.Context,
	tx *gorm.DB,
	checker CollaborationPermissionChecker,
	eventID string,
) error {
	var event struct {
		Status model.ProgramEventStatus `gorm:"column:status"`
	}
	if err := tx.WithContext(ctx).Table("program_event").
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Select("status").Where("id = ?", eventID).Take(&event).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.ResourceNotFound("program event", eventID)
		}
		return err
	}
	if err := requireActiveProgramEventPrincipal(ctx, tx, "view", true); err != nil {
		return err
	}
	if err := requireProgramEventPermission(
		ctx, checker, eventID, programEventViewAction(event.Status),
	); err != nil {
		if rejected(err, domainerr.PermissionDenied) {
			return domainerr.ResourceNotFound("program event", eventID).WithCause(err)
		}
		return err
	}
	return nil
}
