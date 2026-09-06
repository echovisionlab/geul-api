package public

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestArtistPublicStatusFilterAllowsDraftAndPublishedOnly(t *testing.T) {
	require.ElementsMatch(t, []string{
		managev1.ArtistStatus_ARTIST_STATUS_DRAFT.String(),
		managev1.ArtistStatus_ARTIST_STATUS_PUBLISHED.String(),
	}, ArtistFilterConfig.Fields["status"].EnumValues)
}
