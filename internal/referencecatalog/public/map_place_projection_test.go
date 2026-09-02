package public

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/model"
)

func TestToProtoMapPlaceBasic(t *testing.T) {
	require.Nil(t, ToProtoMapPlaceBasic(nil))

	googlePlaceID := "google-place-1"
	result := ToProtoMapPlaceBasic(&model.MapPlace{
		ID:            "place-1",
		Name:          "Polarfront Lab",
		Address:       "Seoul",
		Lat:           37.539639,
		Lng:           126.9904063,
		GooglePlaceID: &googlePlaceID,
	})

	require.Equal(t, "place-1", result.GetId())
	require.Equal(t, "Polarfront Lab", result.GetName())
	require.Equal(t, "Seoul", result.GetAddress())
	require.Equal(t, 37.539639, result.GetLat())
	require.Equal(t, 126.9904063, result.GetLng())
	require.Equal(t, googlePlaceID, result.GetGooglePlaceId())
}
