package programevent

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type programEventAction = auth.ResourceAction

func requireProgramEventPermissionForSubject(
	ctx context.Context,
	checker CollaborationPermissionChecker,
	eventID string,
	action programEventAction,
	subject auth.AccountIdentitySubject,
) error {
	if checker == nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	can, err := action(eventID)
	if err != nil {
		return errs.InvalidArgument("resource.id", "must be a canonical resource UUID")
	}
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	if err != nil {
		return errs.AuthenticationRequired()
	}
	allowed, err := checker.CheckActorCan(ctx, actor, can)
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return errs.NoPermission(can.Action().Name(), "program event")
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
		return errs.AuthenticationRequired()
	}
	can, err := action(eventID)
	if err != nil {
		return errs.InvalidArgument("resource.id", "must be a canonical resource UUID")
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return errs.AuthenticationRequired()
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return errs.NoPermission(can.Action().Name(), "program event")
	}
	return nil
}

func programEventViewAction(status string) programEventAction {
	if status == managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED.String() {
		return policyv1.ProgramEvent.ViewArchived
	}
	return policyv1.ProgramEvent.View
}

func programEventMutationAction(status string, normal programEventAction) programEventAction {
	if status == managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED.String() {
		return policyv1.ProgramEvent.EditArchived
	}
	return normal
}
