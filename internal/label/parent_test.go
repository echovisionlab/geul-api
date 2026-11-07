package label_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/label"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestSetLabelParentMovesTheRowAndTheRelationship(t *testing.T) {
	h := newHarness(t)
	caller := h.member(newPrincipal())
	value, first, second := h.seedLabel(t, "Moved"), h.seedLabel(t, "First"), h.seedLabel(t, "Second")

	view, err := h.service.SetLabelParent(caller.context(t), label.SetLabelParentCommand{LabelID: value.ID, ParentLabelID: &first.ID})
	require.NoError(t, err)
	require.Equal(t, first.ID, *view.Label.ParentLabelID)
	touchFirst, err := policyv1.Label.TouchParent(value.ID, first.ID)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{touchFirst}}, h.checker.Applied())
	require.Equal(t, 2, h.store.ParentGraphLocks(), "the move takes the hierarchy lock first and validates under it")
	require.Equal(t, mutation.StepRelationships, h.units.Steps()[len(h.units.Steps())-2])

	_, err = h.service.SetLabelParent(caller.context(t), label.SetLabelParentCommand{LabelID: value.ID, ParentLabelID: &first.ID})
	require.NoError(t, err)
	require.Len(t, h.checker.Applied(), 1, "the current parent is a no-op")

	_, err = h.service.SetLabelParent(caller.context(t), label.SetLabelParentCommand{LabelID: value.ID, ParentLabelID: &second.ID})
	require.NoError(t, err)
	removeFirst, err := policyv1.Label.DeleteParent(value.ID, first.ID)
	require.NoError(t, err)
	touchSecond, err := policyv1.Label.TouchParent(value.ID, second.ID)
	require.NoError(t, err)
	require.Equal(t, []policyv1.RelationshipMutation{removeFirst, touchSecond}, h.checker.Applied()[1])

	_, err = h.service.SetLabelParent(caller.context(t), label.SetLabelParentCommand{LabelID: value.ID})
	require.NoError(t, err)
	stored, _ := h.store.Label(value.ID)
	require.Nil(t, stored.ParentLabelID)
	removeSecond, err := policyv1.Label.DeleteParent(value.ID, second.ID)
	require.NoError(t, err)
	require.Equal(t, []policyv1.RelationshipMutation{removeSecond}, h.checker.Applied()[2])
}

func TestSetLabelParentRequiresParticipantAuthorityOverEveryLabelItTouches(t *testing.T) {
	for _, denied := range []string{"label", "current parent", "new parent"} {
		t.Run(denied, func(t *testing.T) {
			h := newHarness(t)
			caller := h.member(newPrincipal())
			current := h.seedLabel(t, "Current")
			value := h.seedLabel(t, "Moved", func(value *label.Label) { value.ParentLabelID = &current.ID })
			next := h.seedLabel(t, "Next")
			target := map[string]string{"label": value.ID, "current parent": current.ID, "new parent": next.ID}[denied]
			can, err := policyv1.Label.ManageParticipants(target)
			require.NoError(t, err)
			h.checker.Checker = authztest.AllowAll().Deny(can, nil)

			_, err = h.service.SetLabelParent(caller.context(t), label.SetLabelParentCommand{LabelID: value.ID, ParentLabelID: &next.ID})

			requireCode(t, err, connect.CodeNotFound, "label not found: "+target)
			require.Empty(t, h.checker.Applied())
			stored, _ := h.store.Label(value.ID)
			require.Equal(t, current.ID, *stored.ParentLabelID)
		})
	}
}

func TestSetLabelParentRejections(t *testing.T) {
	for _, test := range []struct {
		name    string
		parent  func(*testing.T, *harness, label.Label) string
		message string
	}{
		{name: "invalid ID", parent: func(*testing.T, *harness, label.Label) string { return "not-a-uuid" },
			message: "parent_label_id: must be a valid Label ID"},
		{name: "itself", parent: func(_ *testing.T, _ *harness, value label.Label) string { return value.ID },
			message: "parent_label_id: cannot reference the same label"},
		{name: "missing", parent: func(*testing.T, *harness, label.Label) string { return uuid.NewString() },
			message: "parent_label_id: label does not exist"},
		{name: "descendant", parent: func(t *testing.T, h *harness, value label.Label) string {
			return h.seedLabel(t, "Child", func(child *label.Label) { child.ParentLabelID = &value.ID }).ID
		}, message: "parent_label_id: would create a label hierarchy cycle"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			caller := h.member(newPrincipal())
			value := h.seedLabel(t, "Moved")
			parent := test.parent(t, h, value)

			_, err := h.service.SetLabelParent(caller.context(t), label.SetLabelParentCommand{LabelID: value.ID, ParentLabelID: &parent})

			requireCode(t, err, connect.CodeInvalidArgument, test.message)
			require.Empty(t, h.checker.Applied())
			stored, _ := h.store.Label(value.ID)
			require.Nil(t, stored.ParentLabelID)
		})
	}
}
