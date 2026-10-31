package public

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/stretchr/testify/require"
)

// Every public Artist status maps from the stored value with its own name,
// so a new API value fails here until it is mapped.
func TestPublicArtistStatusCoversEveryAPIStatus(t *testing.T) {
	for value, name := range openv1.ArtistStatus_name {
		require.Equal(t, openv1.ArtistStatus(value), publicArtistStatus(model.ArtistStatus(name)), name)
	}
	require.Equal(t, openv1.ArtistStatus_ARTIST_STATUS_UNSPECIFIED, publicArtistStatus("ARTIST_STATUS_ARCHIVED"))
}
