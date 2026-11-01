package work

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// nicknameStore answers the nicknames of the Members it holds, recording
// each read's IDs.
func nicknameStore(members map[string]string, reads *[][]string) stubStore {
	return stubStore{nicknames: func(memberIDs []string) (map[string]string, error) {
		*reads = append(*reads, slices.Clone(memberIDs))
		found := map[string]string{}
		for _, id := range memberIDs {
			if nickname, ok := members[id]; ok {
				found[id] = nickname
			}
		}
		return found, nil
	}}
}

func TestResolveVersionContributorNamesReadsEachMemberOnce(t *testing.T) {
	activeID := uuid.NewString()
	tombstoneID := uuid.NewString()
	var reads [][]string
	store := nicknameStore(map[string]string{activeID: "Active", tombstoneID: " Preserved "}, &reads)

	names, err := resolveVersionContributorNames(t.Context(), store, []string{tombstoneID, activeID, activeID})
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		activeID:    "Active",
		tombstoneID: "Preserved",
	}, names)
	require.Equal(t, [][]string{normalizeContributorMemberIDs([]string{activeID, tombstoneID})}, reads)
}

func TestResolveVersionContributorNamesRejectsMissingAndBlankMembers(t *testing.T) {
	blankID := uuid.NewString()
	var reads [][]string
	store := nicknameStore(map[string]string{blankID: " "}, &reads)

	_, err := resolveVersionContributorNames(t.Context(), store, []string{uuid.NewString()})
	require.ErrorContains(t, err, "Member set is incomplete")
	_, err = resolveVersionContributorNames(t.Context(), store, []string{blankID})
	require.ErrorContains(t, err, "has blank nickname")
	names, err := resolveVersionContributorNames(t.Context(), store)
	require.NoError(t, err)
	require.Empty(t, names)
	require.Len(t, reads, 2, "no contributors need no read")
}

func TestResolveVersionContributorNamesDoesNotHideStoreFailure(t *testing.T) {
	failure := errors.New("member table unavailable")
	store := stubStore{nicknames: func([]string) (map[string]string, error) { return nil, failure }}

	_, err := resolveVersionContributorNames(t.Context(), store, []string{uuid.NewString()})
	require.ErrorIs(t, err, failure)
	require.ErrorContains(t, err, "resolve work Version contributor Members")
}
