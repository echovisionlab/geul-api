package programevent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMergeProgramEventArtistsAppliesMembershipDeltaAndPreservesPeerChanges(t *testing.T) {
	current := []Relation{
		{ID: "artist-c", Role: relationRole("peer changed"), SortOrder: 2},
		{ID: "artist-peer", Role: relationRole("unseen add"), SortOrder: 4},
	}
	observed := []Relation{
		{ID: "artist-a", Role: relationRole("removed by user"), SortOrder: 0},
		{ID: "artist-b", Role: relationRole("concurrent delete"), SortOrder: 1},
		{ID: "artist-c", Role: relationRole("before peer change"), SortOrder: 2},
	}
	desired := []Relation{
		{ID: "artist-b", Role: relationRole("concurrent delete"), SortOrder: 0},
		{ID: "artist-c", Role: relationRole("before peer change"), SortOrder: 1},
		{ID: "artist-new", Role: relationRole("new local row"), SortOrder: 2},
	}

	merged, err := mergeProgramEventRelationRows("artists", "artist_id", current, observed, desired)
	require.NoError(t, err)
	require.Equal(t, []Relation{
		{ID: "artist-c", Role: relationRole("peer changed"), SortOrder: 2},
		{ID: "artist-peer", Role: relationRole("unseen add"), SortOrder: 4},
		{ID: "artist-new", Role: relationRole("new local row"), SortOrder: 5},
	}, merged)
}

func TestMergeProgramEventLabelsAppliesOnlyIntendedRoleDifference(t *testing.T) {
	current := []Relation{
		{ID: "label-edited", Role: relationRole("peer value"), SortOrder: 1},
		{ID: "label-untouched", Role: relationRole("peer update"), SortOrder: 5},
	}
	observed := []Relation{
		{ID: "label-edited", Role: relationRole("old"), SortOrder: 0},
		{ID: "label-untouched", Role: relationRole("old untouched"), SortOrder: 1},
	}
	desired := []Relation{
		{ID: "label-edited", Role: relationRole("new intended"), SortOrder: 0},
		{ID: "label-untouched", Role: relationRole("old untouched"), SortOrder: 1},
	}

	merged, err := mergeProgramEventRelationRows("labels", "label_id", current, observed, desired)
	require.NoError(t, err)
	require.Equal(t, []Relation{
		{ID: "label-edited", Role: relationRole("new intended"), SortOrder: 1},
		{ID: "label-untouched", Role: relationRole("peer update"), SortOrder: 5},
	}, merged)
}

func TestMergeProgramEventClientsWithEmptyBaselinePreservesConcurrentRows(t *testing.T) {
	current := []Relation{
		{ID: "client-peer", Role: relationRole("peer addition"), SortOrder: 3},
	}
	desired := []Relation{
		{ID: "client-local", Role: relationRole("local addition"), SortOrder: 0},
	}

	merged, err := mergeProgramEventRelationRows("clients", "client_id", current, nil, desired)
	require.NoError(t, err)
	require.Equal(t, []Relation{
		{ID: "client-peer", Role: relationRole("peer addition"), SortOrder: 3},
		{ID: "client-local", Role: relationRole("local addition"), SortOrder: 4},
	}, merged)
}

func TestMergeProgramEventArtistsAllowsExplicitEmptyDesiredSet(t *testing.T) {
	current := []Relation{
		{ID: "artist-observed", SortOrder: 0},
		{ID: "artist-peer", SortOrder: 3},
	}
	observed := []Relation{{ID: "artist-observed"}}

	merged, err := mergeProgramEventRelationRows("artists", "artist_id", current, observed, nil)
	require.NoError(t, err)
	require.Equal(t, []Relation{{ID: "artist-peer", SortOrder: 3}}, merged)
}

func TestMergeProgramEventRelationRowsRejectsDuplicateObservedIDs(t *testing.T) {
	_, err := mergeProgramEventRelationRows("artists", "artist_id", nil,
		[]Relation{{ID: "artist-1"}, {ID: "artist-1"}},
		nil,
	)
	require.Error(t, err)
}

func relationRole(value string) *string { return &value }
