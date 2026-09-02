package referencecatalog

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestMapPlaceServiceSearchBlankQueryReturnsEmptyWithoutDatabase(t *testing.T) {
	t.Parallel()

	resp, err := (&MapPlaceService{}).SearchMapPlaces(context.Background(), connect.NewRequest(&managev1.SearchMapPlacesRequest{
		Query: "   ",
	}))

	require.NoError(t, err)
	require.Empty(t, resp.Msg.Places)
}
