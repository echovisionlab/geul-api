package label_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/label"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

// participantFixture is a Label with one Owner and a Member who is not yet
// a participant.
type participantFixture struct {
	h      *harness
	label  label.Label
	owner  principal
	member principal
}

func newParticipantFixture(t *testing.T, options ...harnessOption) participantFixture {
	t.Helper()
	h := newHarness(t, options...)
	value := h.seedLabel(t, "Participants")
	owner := h.member(newPrincipal())
	h.store.PutParticipant(value.ID, owner.memberID, entityparticipant.RoleOwner, time.Now().Add(-time.Hour))
	return participantFixture{h: h, label: value, owner: owner, member: h.member(newPrincipal())}
}

func (f participantFixture) set(t *testing.T, role string) (entityparticipant.View, error) {
	t.Helper()
	return f.h.service.SetLabelParticipant(f.owner.context(t), label.SetLabelParticipantCommand{
		LabelID: f.label.ID, MemberID: f.member.memberID, Role: role,
	})
}

func roleMutation(t *testing.T, labelID, identityID string, role string, touch bool) policyv1.RelationshipMutation {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	var mutation policyv1.RelationshipMutation
	switch {
	case role == entityparticipant.RoleOwner && touch:
		mutation, err = policyv1.Label.TouchOwner(labelID, actor)
	case role == entityparticipant.RoleOwner:
		mutation, err = policyv1.Label.DeleteOwner(labelID, actor)
	case touch:
		mutation, err = policyv1.Label.TouchManager(labelID, actor)
	default:
		mutation, err = policyv1.Label.DeleteManager(labelID, actor)
	}
	require.NoError(t, err)
	return mutation
}

func TestSetLabelParticipantAddsAndMovesAMemberWithItsRelationships(t *testing.T) {
	f := newParticipantFixture(t)

	view, err := f.set(t, label.ParticipantRoleManager)

	require.NoError(t, err)
	require.Equal(t, entityparticipant.RoleManager, view.Role)
	require.True(t, view.HasEffectiveAuthority)
	require.Equal(t, entityparticipant.RoleManager, f.h.store.Participants(f.label.ID)[f.member.memberID])
	require.Equal(t, [][]policyv1.RelationshipMutation{{roleMutation(t, f.label.ID, f.member.identityID, entityparticipant.RoleManager, true)}}, f.h.checker.Applied())
	records := f.h.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditRelationshipNone, records[0].PreviousRelationship)
	require.Equal(t, sharedtelemetry.AuditRelationshipManager, records[0].NewRelationship)
	require.Equal(t, f.member.memberID, records[0].SubjectMemberID)
	require.Equal(t, []mutation.Step{
		mutation.StepAuthorizationLock, mutation.StepLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit,
		mutation.StepRelationships, mutation.StepCommit,
	}, f.h.units.Steps())

	_, err = f.set(t, label.ParticipantRoleOwner)
	require.NoError(t, err)
	require.Equal(t, entityparticipant.RoleOwner, f.h.store.Participants(f.label.ID)[f.member.memberID])
	require.Equal(t, []policyv1.RelationshipMutation{
		roleMutation(t, f.label.ID, f.member.identityID, entityparticipant.RoleManager, false),
		roleMutation(t, f.label.ID, f.member.identityID, entityparticipant.RoleOwner, true),
	}, f.h.checker.Applied()[1])
}

func TestSetLabelParticipantRejections(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, participantFixture) (string, string)
		code    connect.Code
		message string
	}{
		{
			name:    "unspecified role",
			prepare: func(*testing.T, participantFixture) (string, string) { return "", "" },
			code:    connect.CodeInvalidArgument, message: "role: must be Owner or Manager",
		},
		{
			name: "same role",
			prepare: func(_ *testing.T, f participantFixture) (string, string) {
				f.h.store.PutParticipant(f.label.ID, f.member.memberID, entityparticipant.RoleManager, time.Now())
				return label.ParticipantRoleManager, ""
			},
			code: connect.CodeFailedPrecondition, message: "Label participant already has this role",
		},
		{
			name: "missing label",
			prepare: func(*testing.T, participantFixture) (string, string) {
				return label.ParticipantRoleManager, "00000000-0000-4000-8000-000000000003"
			},
			code: connect.CodeNotFound, message: "label not found: 00000000-0000-4000-8000-000000000003",
		},
		{
			name: "no permission answers not found",
			prepare: func(t *testing.T, f participantFixture) (string, string) {
				manage, err := policyv1.Label.ManageParticipants(f.label.ID)
				require.NoError(t, err)
				f.h.checker.Checker = authztest.AllowAll().Deny(manage, nil)
				return label.ParticipantRoleManager, ""
			},
			code: connect.CodeNotFound,
		},
		{
			name: "demoting an Owner needs RemoveOwner",
			prepare: func(t *testing.T, f participantFixture) (string, string) {
				f.h.store.PutParticipant(f.label.ID, f.member.memberID, entityparticipant.RoleOwner, time.Now())
				remove, err := policyv1.Label.RemoveOwner(f.label.ID)
				require.NoError(t, err)
				f.h.checker.Checker = authztest.AllowAll().Deny(remove, nil)
				return label.ParticipantRoleManager, ""
			},
			code: connect.CodeNotFound,
		},
		{
			name: "demoting the last Owner",
			prepare: func(t *testing.T, f participantFixture) (string, string) {
				f.h.store.PutParticipant(f.label.ID, f.member.memberID, entityparticipant.RoleOwner, time.Now())
				_, err := f.h.store.DeleteParticipant(t.Context(), f.label.ID, f.owner.memberID, entityparticipant.RoleOwner)
				require.NoError(t, err)
				return label.ParticipantRoleManager, ""
			},
			code: connect.CodeFailedPrecondition, message: "a Label must retain at least one durable Owner",
		},
		{
			name: "unknown member",
			prepare: func(_ *testing.T, f participantFixture) (string, string) {
				return label.ParticipantRoleManager, "unknown-member"
			},
			code: connect.CodeNotFound,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newParticipantFixture(t)
			role, override := test.prepare(t, f)
			command := label.SetLabelParticipantCommand{LabelID: f.label.ID, MemberID: f.member.memberID, Role: role}
			if override == "unknown-member" {
				command.MemberID = "00000000-0000-4000-8000-0000000000aa"
			} else if override != "" {
				command.LabelID = override
			}
			before := f.h.store.Participants(f.label.ID)

			_, err := f.h.service.SetLabelParticipant(f.owner.context(t), command)

			requireCode(t, err, test.code, test.message)
			require.Equal(t, before, f.h.store.Participants(f.label.ID))
			require.Empty(t, f.h.checker.Applied())
			require.Empty(t, f.h.audit.Records())
		})
	}
}

func TestRemoveLabelParticipant(t *testing.T) {
	t.Run("removes a Manager", func(t *testing.T) {
		f := newParticipantFixture(t)
		f.h.store.PutParticipant(f.label.ID, f.member.memberID, entityparticipant.RoleManager, time.Now())

		require.NoError(t, f.h.service.RemoveLabelParticipant(f.owner.context(t), label.RemoveLabelParticipantCommand{LabelID: f.label.ID, MemberID: f.member.memberID}))

		require.NotContains(t, f.h.store.Participants(f.label.ID), f.member.memberID)
		require.Equal(t, [][]policyv1.RelationshipMutation{{roleMutation(t, f.label.ID, f.member.identityID, entityparticipant.RoleManager, false)}}, f.h.checker.Applied())
		require.Equal(t, sharedtelemetry.AuditRelationshipNone, f.h.audit.Records()[0].NewRelationship)
	})
	t.Run("a missing Manager answers not found", func(t *testing.T) {
		f := newParticipantFixture(t)

		err := f.h.service.RemoveLabelParticipant(f.owner.context(t), label.RemoveLabelParticipantCommand{LabelID: f.label.ID, MemberID: f.member.memberID})

		requireCode(t, err, connect.CodeNotFound, "Label participant not found")
		require.Empty(t, f.h.checker.Applied())
	})
	t.Run("the last Owner stays", func(t *testing.T) {
		f := newParticipantFixture(t)

		err := f.h.service.RemoveLabelParticipant(f.owner.context(t), label.RemoveLabelParticipantCommand{LabelID: f.label.ID, MemberID: f.owner.memberID})

		requireCode(t, err, connect.CodeFailedPrecondition, "a Label must retain at least one durable Owner")
		require.Contains(t, f.h.store.Participants(f.label.ID), f.owner.memberID)
	})
}

func TestListLabelParticipantsReportsEffectiveAuthority(t *testing.T) {
	f := newParticipantFixture(t)
	f.h.store.PutParticipant(f.label.ID, f.member.memberID, entityparticipant.RoleManager, time.Now())
	manage, err := policyv1.Label.Manage(f.label.ID)
	require.NoError(t, err)
	f.h.checker.Checker = authztest.AllowAll()
	f.h.checker.For(f.member.identityID).Deny(manage, nil)

	views, err := f.h.service.ListLabelParticipants(f.owner.context(t), label.ListLabelParticipantsQuery{LabelID: f.label.ID})

	require.NoError(t, err)
	require.Len(t, views, 2)
	require.Equal(t, f.owner.memberID, views[0].Member.ID)
	require.True(t, views[0].HasEffectiveAuthority)
	require.Equal(t, f.member.memberID, views[1].Member.ID)
	require.False(t, views[1].HasEffectiveAuthority)
}

func TestLabelParticipantChangesBetweenOwners(t *testing.T) {
	t.Run("one of two Owners is demoted with RemoveOwner", func(t *testing.T) {
		f := newParticipantFixture(t)
		f.h.store.PutParticipant(f.label.ID, f.member.memberID, entityparticipant.RoleOwner, time.Now())

		_, err := f.set(t, label.ParticipantRoleManager)

		require.NoError(t, err)
		require.Equal(t, entityparticipant.RoleManager, f.h.store.Participants(f.label.ID)[f.member.memberID])
		remove, err := policyv1.Label.RemoveOwner(f.label.ID)
		require.NoError(t, err)
		require.Contains(t, f.h.checker.Checks(), authztest.Of(f.owner.identityID, remove, nil))
	})
	t.Run("the current Owner role is rejected", func(t *testing.T) {
		f := newParticipantFixture(t)
		f.h.store.PutParticipant(f.label.ID, f.member.memberID, entityparticipant.RoleOwner, time.Now())

		_, err := f.set(t, label.ParticipantRoleOwner)

		requireCode(t, err, connect.CodeFailedPrecondition, "Label participant already has this role")
	})
	t.Run("one of two Owners is removed", func(t *testing.T) {
		f := newParticipantFixture(t)
		f.h.store.PutParticipant(f.label.ID, f.member.memberID, entityparticipant.RoleOwner, time.Now())

		require.NoError(t, f.h.service.RemoveLabelParticipant(f.owner.context(t), label.RemoveLabelParticipantCommand{LabelID: f.label.ID, MemberID: f.member.memberID}))

		require.Equal(t, map[string]string{f.owner.memberID: entityparticipant.RoleOwner}, f.h.store.Participants(f.label.ID))
		require.Equal(t, [][]policyv1.RelationshipMutation{{roleMutation(t, f.label.ID, f.member.identityID, entityparticipant.RoleOwner, false)}}, f.h.checker.Applied())
	})
	t.Run("an anonymous caller and a missing Label are not found", func(t *testing.T) {
		f := newParticipantFixture(t)
		_, err := f.h.service.SetLabelParticipant(t.Context(), label.SetLabelParticipantCommand{LabelID: f.label.ID, MemberID: f.member.memberID, Role: label.ParticipantRoleManager})
		requireCode(t, err, connect.CodeNotFound, "label not found: "+f.label.ID)
		err = f.h.service.RemoveLabelParticipant(f.owner.context(t), label.RemoveLabelParticipantCommand{LabelID: "00000000-0000-4000-8000-000000000007", MemberID: f.member.memberID})
		requireCode(t, err, connect.CodeNotFound, "label not found: 00000000-0000-4000-8000-000000000007")
		require.Empty(t, f.h.checker.Applied())
	})
}
