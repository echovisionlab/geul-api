package programevent

import (
	"testing"
	"time"

	errs "github.com/echovisionlab/geul-api/internal/errors"

	"connectrpc.com/connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestApplyProgramEventTimeAndLocationUpdatesRevalidatesLockedTimeRange(t *testing.T) {
	base := time.Date(2026, time.October, 1, 10, 0, 0, 0, time.UTC)
	initial := Event{
		StartsAt:     base,
		EndsAt:       timestampPtr(timestamppb.New(base.Add(10 * time.Hour))),
		LocationMode: LocationModeOnline,
	}
	newStart := timestamppb.New(base.Add(8 * time.Hour))
	request := &managev1.UpdateProgramEventRequest{StartsAt: newStart}
	keepTimezone := "Asia/Seoul"
	update := programEventUpdate{SettingsUpdate{Timezone: &keepTimezone}}

	require.NoError(t, applyProgramEventTimeAndLocationUpdates(&update, &initial, updateProgramEventCommand(request)))
	require.Equal(t, base.Add(8*time.Hour), *update.StartsAt)

	locked := Event{
		StartsAt:     base,
		EndsAt:       timestampPtr(timestamppb.New(base.Add(6 * time.Hour))),
		LocationMode: initial.LocationMode,
	}
	err := applyProgramEventTimeAndLocationUpdates(&update, &locked, updateProgramEventCommand(request))
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	require.Equal(t, keepTimezone, *update.Timezone)
}

func TestProgramEventRelationWritesRequireObservedBaselines(t *testing.T) {
	cases := []struct {
		name    string
		request *managev1.UpdateProgramEventRequest
	}{
		{name: "artists", request: &managev1.UpdateProgramEventRequest{Artists: []*managev1.ProgramEventArtist{{ArtistId: "artist"}}}},
		{name: "empty artist replacement", request: &managev1.UpdateProgramEventRequest{ReplaceArtists: true}},
		{name: "labels", request: &managev1.UpdateProgramEventRequest{Labels: []*managev1.ProgramEventLabel{{LabelId: "label"}}}},
		{name: "clients", request: &managev1.UpdateProgramEventRequest{Clients: []*managev1.ProgramEventClient{{ClientId: "client"}}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := validateProgramEventRelationBaselines(updateProgramEventCommand(test.request))
			require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
		})
	}
}

func TestProgramEventObservedOnlyRelationBaselinesDoNotStartWrites(t *testing.T) {
	require.NoError(t, validateProgramEventRelationBaselines(updateProgramEventCommand(&managev1.UpdateProgramEventRequest{
		ObservedArtists: &managev1.ProgramEventArtistsSnapshot{},
		ObservedLabels:  &managev1.ProgramEventLabelsSnapshot{},
		ObservedClients: &managev1.ProgramEventClientsSnapshot{},
	})))
}

func TestApplyProgramEventTimeAndLocationUpdatesDoesNotRestoreMapPlaceAfterModeChange(t *testing.T) {
	mapPlaceMode := LocationModeMapPlace
	initialPlace := "place-before"
	requestedPlace := "place-requested"
	for _, lockedMode := range []LocationMode{
		LocationModeOnline,
		LocationModeTBA,
	} {
		t.Run(string(lockedMode), func(t *testing.T) {
			initial := Event{
				StartsAt:     time.Date(2026, time.October, 1, 10, 0, 0, 0, time.UTC),
				LocationMode: mapPlaceMode,
				MapPlaceID:   &initialPlace,
			}
			request := &managev1.UpdateProgramEventRequest{MapPlaceId: &requestedPlace}
			keepTimezone := "Asia/Seoul"
			update := programEventUpdate{SettingsUpdate{Timezone: &keepTimezone}}

			require.NoError(t, applyProgramEventTimeAndLocationUpdates(&update, &initial, updateProgramEventCommand(request)))
			require.Equal(t, "place-requested", *update.MapPlaceID.Value)

			locked := Event{
				StartsAt:     initial.StartsAt,
				LocationMode: lockedMode,
				MapPlaceID:   nil,
			}
			require.NoError(t, applyProgramEventTimeAndLocationUpdates(&update, &locked, updateProgramEventCommand(request)))
			require.True(t, update.MapPlaceID.Set)
			require.Nil(t, update.MapPlaceID.Value)
			require.Nil(t, update.LocationMode)
			require.Equal(t, keepTimezone, *update.Timezone)
		})
	}

	t.Run("explicit TBA transition clears current map place", func(t *testing.T) {
		requestMode := managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_TBA
		initial := Event{
			StartsAt:     time.Date(2026, time.October, 1, 10, 0, 0, 0, time.UTC),
			LocationMode: mapPlaceMode,
			MapPlaceID:   &initialPlace,
		}
		var update programEventUpdate
		require.NoError(t, applyProgramEventTimeAndLocationUpdates(&update, &initial, updateProgramEventCommand(&managev1.UpdateProgramEventRequest{
			LocationMode: &requestMode,
		})))
		require.Equal(t, LocationModeTBA, *update.LocationMode)
		require.True(t, update.MapPlaceID.Set)
		require.Nil(t, update.MapPlaceID.Value)
	})
}
