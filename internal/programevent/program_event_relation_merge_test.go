package programevent

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestMergeProgramEventArtistsAppliesMembershipDeltaAndPreservesPeerChanges(t *testing.T) {
	current := []model.ProgramEventArtist{
		{ArtistID: "artist-c", Role: relationRole("peer changed"), SortOrder: 2},
		{ArtistID: "artist-peer", Role: relationRole("unseen add"), SortOrder: 4},
	}
	observed := []*managev1.ProgramEventArtist{
		{ArtistId: "artist-a", Role: relationRole("removed by user"), SortOrder: 0},
		{ArtistId: "artist-b", Role: relationRole("concurrent delete"), SortOrder: 1},
		{ArtistId: "artist-c", Role: relationRole("before peer change"), SortOrder: 2},
	}
	desired := []*managev1.ProgramEventArtist{
		{ArtistId: "artist-b", Role: relationRole("concurrent delete"), SortOrder: 0},
		{ArtistId: "artist-c", Role: relationRole("before peer change"), SortOrder: 1},
		{ArtistId: "artist-new", Role: relationRole("new local row"), SortOrder: 2},
	}

	merged, err := mergeProgramEventArtists(current, observed, desired)
	require.NoError(t, err)
	require.Equal(t, []model.ProgramEventArtist{
		{ArtistID: "artist-c", Role: relationRole("peer changed"), SortOrder: 2},
		{ArtistID: "artist-peer", Role: relationRole("unseen add"), SortOrder: 4},
		{ArtistID: "artist-new", Role: relationRole("new local row"), SortOrder: 5},
	}, merged)
}

func TestMergeProgramEventLabelsAppliesOnlyIntendedRoleDifference(t *testing.T) {
	current := []model.ProgramEventLabel{
		{LabelID: "label-edited", Role: relationRole("peer value"), SortOrder: 1},
		{LabelID: "label-untouched", Role: relationRole("peer update"), SortOrder: 5},
	}
	observed := []*managev1.ProgramEventLabel{
		{LabelId: "label-edited", Role: relationRole("old"), SortOrder: 0},
		{LabelId: "label-untouched", Role: relationRole("old untouched"), SortOrder: 1},
	}
	desired := []*managev1.ProgramEventLabel{
		{LabelId: "label-edited", Role: relationRole("new intended"), SortOrder: 0},
		{LabelId: "label-untouched", Role: relationRole("old untouched"), SortOrder: 1},
	}

	merged, err := mergeProgramEventLabels(current, observed, desired)
	require.NoError(t, err)
	require.Equal(t, []model.ProgramEventLabel{
		{LabelID: "label-edited", Role: relationRole("new intended"), SortOrder: 1},
		{LabelID: "label-untouched", Role: relationRole("peer update"), SortOrder: 5},
	}, merged)
}

func TestMergeProgramEventClientsWithEmptyBaselinePreservesConcurrentRows(t *testing.T) {
	current := []model.ProgramEventClient{
		{ClientID: "client-peer", Role: relationRole("peer addition"), SortOrder: 3},
	}
	desired := []*managev1.ProgramEventClient{
		{ClientId: "client-local", Role: relationRole("local addition"), SortOrder: 0},
	}

	merged, err := mergeProgramEventClients(current, nil, desired)
	require.NoError(t, err)
	require.Equal(t, []model.ProgramEventClient{
		{ClientID: "client-peer", Role: relationRole("peer addition"), SortOrder: 3},
		{ClientID: "client-local", Role: relationRole("local addition"), SortOrder: 4},
	}, merged)
}

func TestMergeProgramEventArtistsAllowsExplicitEmptyDesiredSet(t *testing.T) {
	current := []model.ProgramEventArtist{
		{ArtistID: "artist-observed", SortOrder: 0},
		{ArtistID: "artist-peer", SortOrder: 3},
	}
	observed := []*managev1.ProgramEventArtist{{ArtistId: "artist-observed"}}

	merged, err := mergeProgramEventArtists(current, observed, nil)
	require.NoError(t, err)
	require.Equal(t, []model.ProgramEventArtist{{ArtistID: "artist-peer", SortOrder: 3}}, merged)
}

func TestMergeProgramEventRelationRowsRejectsDuplicateObservedIDs(t *testing.T) {
	_, err := mergeProgramEventArtists(nil,
		[]*managev1.ProgramEventArtist{{ArtistId: "artist-1"}, {ArtistId: "artist-1"}},
		nil,
	)
	require.Error(t, err)
}

func relationRole(value string) *string { return &value }
