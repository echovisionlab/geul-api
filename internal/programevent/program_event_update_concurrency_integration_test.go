//go:build integration

package programevent

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestProgramEventUpdateRevalidatesTimeAndLocationAfterRootLockIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	ctx, spiceDB := integrationAdminCtxWithIdentityAndSpiceDB(t, db)
	contentBlocks := newProgramEventIntegrationContentBlockStore(t, spiceDB)

	typeResponse, err := NewProgramEventTypeService(db, spiceDB).CreateProgramEventType(
		ctx,
		connect.NewRequest(&managev1.CreateProgramEventTypeRequest{
			Slug:              "program-event-update-race-type-" + integrationTestUUID(),
			Locale:            "en",
			Name:              "Program Event update race type",
			RequiresPlace:     ptrBool(false),
			RequiresStreamUrl: ptrBool(false),
		}),
	)
	require.NoError(t, err)

	places := referencecatalog.NewMapPlaceService(
		db,
		referencecatalogadapter.NewAssets(""),
		referencecatalogadapter.NewMemberSummaries(""),
		spiceDB,
	)
	createPlace := func(name string) string {
		place, err := places.CreateMapPlace(ctx, connect.NewRequest(&managev1.CreateMapPlaceRequest{
			Name:          name,
			Address:       "1 Concurrent Way, Seoul",
			Lat:           37.57,
			Lng:           126.98,
			GooglePlaceId: ptrString("google-place-" + integrationTestUUID()),
		}))
		require.NoError(t, err)
		return place.Msg.Id
	}
	placeBefore := createPlace("Program Event update race before")
	placeRequested := createPlace("Program Event update race requested")

	eventService := NewProgramEventService(
		db,
		newProgramEventRuntime(""),
		spiceDB,
		newProgramEventCreditMemberSummaries(db, ""),
	)
	eventService.contentBlocks = contentBlocks
	startsAt := time.Now().UTC().Add(3 * time.Hour).Truncate(time.Microsecond)
	endsAt := startsAt.Add(10 * time.Hour)
	event, err := eventService.CreateProgramEvent(ctx, connect.NewRequest(&managev1.CreateProgramEventRequest{
		Title:        "Program Event update race",
		Slug:         "program-event-update-race-" + integrationTestUUID(),
		SourceLocale: "en",
		TypeId:       typeResponse.Msg.Id,
		StartsAt:     timestamppb.New(startsAt),
		EndsAt:       timestamppb.New(endsAt),
		Timezone:     "UTC",
		LocationMode: managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_MAP_PLACE,
		MapPlaceId:   ptrString(placeBefore),
	}))
	require.NoError(t, err)

	t.Run("concurrent end change returns invalid argument before database check", func(t *testing.T) {
		lockTx := lockAdminMutationRoot(t, db, "program_event", "id = '"+event.Msg.Id+"'::uuid")
		result := make(chan error, 1)
		requestedStart := startsAt.Add(7 * time.Hour)
		go func() {
			_, err := eventService.UpdateProgramEvent(ctx, connect.NewRequest(&managev1.UpdateProgramEventRequest{
				Id: event.Msg.Id, StartsAt: timestamppb.New(requestedStart),
			}))
			result <- err
		}()
		requireAdminMutationWaiting(t, result)

		concurrentEnd := startsAt.Add(5 * time.Hour)
		require.NoError(t, lockTx.Model(&model.ProgramEvent{}).
			Where("id = ?", event.Msg.Id).
			Update("ends_at", concurrentEnd).Error)
		require.NoError(t, lockTx.Commit().Error)
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(<-result))

		var after model.ProgramEvent
		require.NoError(t, db.First(&after, "id = ?", event.Msg.Id).Error)
		require.True(t, after.StartsAt.Equal(startsAt))
		require.NotNil(t, after.EndsAt)
		require.True(t, after.EndsAt.Equal(concurrentEnd))
	})

	t.Run("concurrent move to online does not restore map place", func(t *testing.T) {
		lockTx := lockAdminMutationRoot(t, db, "program_event", "id = '"+event.Msg.Id+"'::uuid")
		result := make(chan error, 1)
		go func() {
			_, err := eventService.UpdateProgramEvent(ctx, connect.NewRequest(&managev1.UpdateProgramEventRequest{
				Id: event.Msg.Id, MapPlaceId: ptrString(placeRequested),
			}))
			result <- err
		}()
		requireAdminMutationWaiting(t, result)

		require.NoError(t, lockTx.Model(&model.ProgramEvent{}).
			Where("id = ?", event.Msg.Id).
			Updates(map[string]any{
				"location_mode": managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_ONLINE.String(),
				"map_place_id":  nil,
			}).Error)
		require.NoError(t, lockTx.Commit().Error)
		require.NoError(t, <-result)

		var after model.ProgramEvent
		require.NoError(t, db.First(&after, "id = ?", event.Msg.Id).Error)
		require.Equal(t, managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_ONLINE.String(), after.LocationMode)
		require.Nil(t, after.MapPlaceID)
	})
}
