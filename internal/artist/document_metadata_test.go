package artist

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeArtistLabelIDsSortsByIDAndRejectsADuplicate(t *testing.T) {
	first := "22222222-2222-4222-8222-222222222222"
	second := "11111111-1111-4111-8111-111111111111"
	labelIDs, err := normalizeArtistLabelIDs([]string{first, second})
	require.NoError(t, err)
	require.Equal(t, []string{second, first}, labelIDs)
	_, err = normalizeArtistLabelIDs([]string{first, first})
	require.Error(t, err)
}
