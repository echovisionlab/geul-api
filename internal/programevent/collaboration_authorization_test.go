package programevent

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

type programEventPermissionCheck struct {
	actor    policyv1.Actor
	resource policyv1.Resource
	action   policyv1.Action
}

type recordingProgramEventPermissionChecker struct {
	calls []programEventPermissionCheck
}

func (checker *recordingProgramEventPermissionChecker) CheckActorCan(
	_ context.Context,
	actor policyv1.Actor,
	can policyv1.Can,
) (bool, error) {
	checker.calls = append(checker.calls, programEventPermissionCheck{
		actor: actor, resource: can.Resource(), action: can.Action(),
	})
	return true, nil
}

func (checker *recordingProgramEventPermissionChecker) Can(
	_ context.Context,
	decision policyv1.AuthorizationDecision,
) (bool, error) {
	checker.calls = append(checker.calls, programEventPermissionCheck{
		actor: decision.Actor(), resource: decision.Resource(), action: decision.Action(),
	})
	return true, nil
}

func TestProgramEventActionSelectionUsesArchivedCapabilitiesExclusively(t *testing.T) {
	t.Parallel()

	const eventID = "22222222-2222-4222-8222-222222222222"
	archived := managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED.String()
	draft := managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_DRAFT.String()
	viewArchived, err := programEventViewAction(archived)(eventID)
	require.NoError(t, err)
	wantViewArchived, err := policyv1.ProgramEvent.ViewArchived(eventID)
	require.NoError(t, err)
	require.Equal(t, wantViewArchived, viewArchived)
	viewDraft, err := programEventViewAction(draft)(eventID)
	require.NoError(t, err)
	wantViewDraft, err := policyv1.ProgramEvent.View(eventID)
	require.NoError(t, err)
	require.Equal(t, wantViewDraft, viewDraft)

	for _, normal := range []programEventAction{
		policyv1.ProgramEvent.Edit,
		policyv1.ProgramEvent.Delete,
		policyv1.ProgramEvent.Publish,
		policyv1.ProgramEvent.Manage,
	} {
		gotArchived, err := programEventMutationAction(archived, normal)(eventID)
		require.NoError(t, err)
		wantArchived, err := policyv1.ProgramEvent.EditArchived(eventID)
		require.NoError(t, err)
		require.Equal(t, wantArchived, gotArchived)
		gotDraft, err := programEventMutationAction(draft, normal)(eventID)
		require.NoError(t, err)
		wantDraft, err := normal(eventID)
		require.NoError(t, err)
		require.Equal(t, wantDraft, gotDraft)
	}
}

func TestRequireProgramEventPermissionForSubjectChecksExactObjectActionOnce(t *testing.T) {
	t.Parallel()

	const eventID = "22222222-2222-4222-8222-222222222222"
	checker := &recordingProgramEventPermissionChecker{}
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID("11111111-1111-4111-8111-111111111111"))
	require.NoError(t, err)
	require.NoError(t, requireProgramEventPermissionForSubject(
		context.Background(), checker, eventID,
		policyv1.ProgramEvent.EditArchived, subject,
	))
	want, err := policyv1.ProgramEvent.EditArchived(eventID)
	require.NoError(t, err)
	require.Len(t, checker.calls, 1)
	require.Equal(t, subject.ID.String(), checker.calls[0].actor.AccountIdentityID())
	require.Equal(t, eventID, checker.calls[0].resource.ID())
	require.Equal(t, want.Action(), checker.calls[0].action)
}

func TestProgramEventTargetLocaleAuditOperationReflectsResourceLifecycle(t *testing.T) {
	t.Parallel()

	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, programEventTargetLocaleContentOperation(true, false, false))
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, programEventTargetLocaleContentOperation(false, false, false))
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, programEventTargetLocaleContentOperation(false, false, true))
	require.Equal(t, sharedtelemetry.AuditItemOperationDeleted, programEventTargetLocaleContentOperation(false, true, true))
}

var _ CollaborationPermissionChecker = (*recordingProgramEventPermissionChecker)(nil)
