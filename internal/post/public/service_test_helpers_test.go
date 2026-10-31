package public

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewServiceNamesEveryMissingDependency(t *testing.T) {
	_, err := NewService(Deps{CDNDomain: "cdn.example"})
	require.EqualError(t, err, "public Post dependencies are required: ContentBlocks, DB, Files, Localizer, MapPlaces, Members, ShareLinks, SpiceDB")
}
