package artist

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

// Every API Artist status maps from the stored value with its own name, so a
// new API value fails here until it is mapped.
func TestArtistStatusToProtoCoversEveryAPIStatus(t *testing.T) {
	for value, name := range managev1.ArtistStatus_name {
		require.Equal(t, managev1.ArtistStatus(value), artistStatusToProto(model.ArtistStatus(name)), name)
	}
	require.Equal(t, managev1.ArtistStatus_ARTIST_STATUS_UNSPECIFIED, artistStatusToProto("ARTIST_STATUS_ARCHIVED"))
}
