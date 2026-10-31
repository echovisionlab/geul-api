package programevent

import (
	"testing"
	"time"

	errs "github.com/echovisionlab/geul-api/internal/errors"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestApplyProgramEventTimeAndLocationUpdatesRevalidatesLockedTimeRange(t *testing.T) {
	base := time.Date(2026, time.October, 1, 10, 0, 0, 0, time.UTC)
	initial := model.ProgramEvent{
		StartsAt:     base,
		EndsAt:       timestampPtr(timestamppb.New(base.Add(10 * time.Hour))),
		LocationMode: model.ProgramEventLocationModeOnline,
	}
	newStart := timestamppb.New(base.Add(8 * time.Hour))
	request := &managev1.UpdateProgramEventRequest{StartsAt: newStart}
	keepTimezone := "Asia/Seoul"
	update := programEventUpdate{timezone: &keepTimezone}

	require.NoError(t, applyProgramEventTimeAndLocationUpdates(&update, &initial, updateProgramEventCommand(request)))
	require.Equal(t, base.Add(8*time.Hour), *update.startsAt)

	locked := model.ProgramEvent{
		StartsAt:     base,
		EndsAt:       timestampPtr(timestamppb.New(base.Add(6 * time.Hour))),
		LocationMode: initial.LocationMode,
	}
	err := applyProgramEventTimeAndLocationUpdates(&update, &locked, updateProgramEventCommand(request))
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	require.Equal(t, keepTimezone, *update.timezone)
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
	mapPlaceMode := model.ProgramEventLocationModeMapPlace
	initialPlace := "place-before"
	requestedPlace := "place-requested"
	for _, lockedMode := range []model.ProgramEventLocationMode{
		model.ProgramEventLocationModeOnline,
		model.ProgramEventLocationModeTBA,
	} {
		t.Run(string(lockedMode), func(t *testing.T) {
			initial := model.ProgramEvent{
				StartsAt:     time.Date(2026, time.October, 1, 10, 0, 0, 0, time.UTC),
				LocationMode: mapPlaceMode,
				MapPlaceID:   &initialPlace,
			}
			request := &managev1.UpdateProgramEventRequest{MapPlaceId: &requestedPlace}
			keepTimezone := "Asia/Seoul"
			update := programEventUpdate{timezone: &keepTimezone}

			require.NoError(t, applyProgramEventTimeAndLocationUpdates(&update, &initial, updateProgramEventCommand(request)))
			require.Equal(t, "place-requested", *update.mapPlaceID.value)

			locked := model.ProgramEvent{
				StartsAt:     initial.StartsAt,
				LocationMode: lockedMode,
				MapPlaceID:   nil,
			}
			require.NoError(t, applyProgramEventTimeAndLocationUpdates(&update, &locked, updateProgramEventCommand(request)))
			require.True(t, update.mapPlaceID.set)
			require.Nil(t, update.mapPlaceID.value)
			require.Nil(t, update.locationMode)
			require.Equal(t, keepTimezone, *update.timezone)
		})
	}

	t.Run("explicit TBA transition clears current map place", func(t *testing.T) {
		requestMode := managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_TBA
		initial := model.ProgramEvent{
			StartsAt:     time.Date(2026, time.October, 1, 10, 0, 0, 0, time.UTC),
			LocationMode: mapPlaceMode,
			MapPlaceID:   &initialPlace,
		}
		var update programEventUpdate
		require.NoError(t, applyProgramEventTimeAndLocationUpdates(&update, &initial, updateProgramEventCommand(&managev1.UpdateProgramEventRequest{
			LocationMode: &requestMode,
		})))
		require.Equal(t, model.ProgramEventLocationModeTBA, *update.locationMode)
		require.True(t, update.mapPlaceID.set)
		require.Nil(t, update.mapPlaceID.value)
	})
}
