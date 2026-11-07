package artist_test

import (
	"slices"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestSetArtistParentMovesTheRowAndTheRelationship(t *testing.T) {
	h := newHarness(t)
	caller := h.member(newPrincipal())
	value, parent := h.seedArtist(t, "Moved"), h.seedArtist(t, "Parent")

	view, err := h.service.SetArtistParent(caller.context(t), artist.SetArtistParentCommand{ArtistID: value.ID, ParentArtistID: &parent.ID})

	require.NoError(t, err)
	require.Equal(t, parent.ID, *view.Artist.ParentArtistID)
	touch, err := policyv1.Artist.TouchParent(value.ID, parent.ID)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{touch}}, h.checker.Applied())
	require.Equal(t, 2, h.store.ParentGraphLocks(), "the move takes the hierarchy lock first and validates under it")
}

func TestSetArtistParentRequiresParticipantAuthorityOverTheNewParent(t *testing.T) {
	h := newHarness(t)
	caller := h.member(newPrincipal())
	value, parent := h.seedArtist(t, "Moved"), h.seedArtist(t, "Parent")
	can, err := policyv1.Artist.ManageParticipants(parent.ID)
	require.NoError(t, err)
	h.checker.Checker = authztest.AllowAll().Deny(can, nil)

	_, err = h.service.SetArtistParent(caller.context(t), artist.SetArtistParentCommand{ArtistID: value.ID, ParentArtistID: &parent.ID})

	requireCode(t, err, connect.CodeNotFound, "artist not found: "+parent.ID)
	require.Empty(t, h.checker.Applied())
}

func TestSetArtistParentRejectsAHierarchyCycle(t *testing.T) {
	h := newHarness(t)
	caller := h.member(newPrincipal())
	value := h.seedArtist(t, "Moved")
	child := h.seedArtist(t, "Child", func(child *artist.Artist) { child.ParentArtistID = &value.ID })
	grandchild := h.seedArtist(t, "Grandchild", func(next *artist.Artist) { next.ParentArtistID = &child.ID })

	_, err := h.service.SetArtistParent(caller.context(t), artist.SetArtistParentCommand{ArtistID: value.ID, ParentArtistID: &grandchild.ID})
	requireCode(t, err, connect.CodeInvalidArgument, "parent_artist_id: would create an artist hierarchy cycle")
	_, err = h.service.SetArtistParent(caller.context(t), artist.SetArtistParentCommand{ArtistID: value.ID, ParentArtistID: &value.ID})
	requireCode(t, err, connect.CodeInvalidArgument, "parent_artist_id: cannot reference the same artist")
	stored, _ := h.store.Artist(value.ID)
	require.Nil(t, stored.ParentArtistID)
	require.Empty(t, h.checker.Applied())
}

func TestSetArtistLabelsMovesTheLinksAndTheirRelationships(t *testing.T) {
	h := newHarness(t)
	caller := h.member(newPrincipal())
	value := h.seedArtist(t, "Signed")
	ids := []string{uuid.NewString(), uuid.NewString()}
	slices.Sort(ids)
	first, second := ids[0], ids[1]
	h.store.PutLabel(first, "First")
	h.store.PutLabel(second, "Second")

	saved, err := h.service.SetArtistLabels(caller.context(t), artist.SetArtistLabelsCommand{ArtistID: value.ID, LabelIDs: []string{second, first}})
	require.NoError(t, err)
	require.Equal(t, []string{first, second}, saved, "Labels are stored in ID order")
	require.Equal(t, []string{first, second}, h.store.LinkedLabels(value.ID))
	touchFirst, err := policyv1.Artist.TouchLabel(value.ID, first)
	require.NoError(t, err)
	touchSecond, err := policyv1.Artist.TouchLabel(value.ID, second)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{touchFirst, touchSecond}}, h.checker.Applied())

	_, err = h.service.SetArtistLabels(caller.context(t), artist.SetArtistLabelsCommand{ArtistID: value.ID, LabelIDs: []string{first, second}})
	require.NoError(t, err)
	require.Len(t, h.checker.Applied(), 1, "the current Labels are a no-op")

	_, err = h.service.SetArtistLabels(caller.context(t), artist.SetArtistLabelsCommand{ArtistID: value.ID})
	require.NoError(t, err)
	require.Empty(t, h.store.LinkedLabels(value.ID), "an empty list clears the Labels")
	removeFirst, err := policyv1.Artist.DeleteLabel(value.ID, first)
	require.NoError(t, err)
	removeSecond, err := policyv1.Artist.DeleteLabel(value.ID, second)
	require.NoError(t, err)
	require.Equal(t, []policyv1.RelationshipMutation{removeFirst, removeSecond}, h.checker.Applied()[1])
}

func TestSetArtistLabelsRequiresParticipantAuthorityOverEverySide(t *testing.T) {
	for _, denied := range []string{"artist", "joined label", "left label"} {
		t.Run(denied, func(t *testing.T) {
			h := newHarness(t)
			caller := h.member(newPrincipal())
			value := h.seedArtist(t, "Signed")
			joined, left := uuid.NewString(), uuid.NewString()
			h.store.PutLabel(joined, "Joined")
			h.store.PutLabel(left, "Left")
			h.store.LinkLabels(value.ID, left)
			var can policyv1.Can
			var err error
			want := "label not found: "
			switch denied {
			case "artist":
				can, err = policyv1.Artist.ManageParticipants(value.ID)
				want = "artist not found: " + value.ID
			case "joined label":
				can, err = policyv1.Label.ManageParticipants(joined)
				want += joined
			default:
				can, err = policyv1.Label.ManageParticipants(left)
				want += left
			}
			require.NoError(t, err)
			h.checker.Checker = authztest.AllowAll().Deny(can, nil)

			_, err = h.service.SetArtistLabels(caller.context(t), artist.SetArtistLabelsCommand{ArtistID: value.ID, LabelIDs: []string{joined}})

			requireCode(t, err, connect.CodeNotFound, want)
			require.Empty(t, h.checker.Applied())
			require.Equal(t, []string{left}, h.store.LinkedLabels(value.ID))
		})
	}
}

func TestSetArtistLabelsRejections(t *testing.T) {
	duplicate := uuid.NewString()
	for _, test := range []struct {
		name     string
		labelIDs []string
		message  string
	}{
		{name: "an unknown Label", labelIDs: []string{uuid.NewString()}, message: "label_ids: contains a Label that does not exist"},
		{name: "a duplicate Label", labelIDs: []string{duplicate, " " + duplicate}, message: "label_ids: must contain unique Label IDs"},
		{name: "an invalid Label ID", labelIDs: []string{"label"}, message: "label_ids: must contain valid Label IDs"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			caller := h.member(newPrincipal())
			value := h.seedArtist(t, "Signed")

			_, err := h.service.SetArtistLabels(caller.context(t), artist.SetArtistLabelsCommand{ArtistID: value.ID, LabelIDs: test.labelIDs})

			requireCode(t, err, connect.CodeInvalidArgument, test.message)
			require.Empty(t, h.checker.Applied())
			require.Empty(t, h.store.LinkedLabels(value.ID))
		})
	}
}
