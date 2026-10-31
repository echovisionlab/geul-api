package programevent

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
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
		return errs.WithCause(errs.InvalidArgument("resource.id", "must be a canonical resource UUID"), err)
	}
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	if err != nil {
		return errs.WithCause(errs.AuthenticationRequired(), err)
	}
	allowed, err := checker.CheckActorCan(ctx, actor, can)
	if err != nil {
		return errs.WithCause(errs.DependencyUnavailable("SpiceDB"), err)
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
		return errs.WithCause(errs.InvalidArgument("resource.id", "must be a canonical resource UUID"), err)
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return errs.WithCause(errs.AuthenticationRequired(), err)
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return errs.WithCause(errs.DependencyUnavailable("SpiceDB"), err)
	}
	if !allowed {
		return errs.NoPermission(can.Action().Name(), "program event")
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
