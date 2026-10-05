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

	t.Run("relation snapshot applies membership delta to locked current rows", func(t *testing.T) {
		createArtist := func(name string) string {
			id := integrationTestUUID()
			documentID := seedServiceIntegrationContentDocument(t, db, creativeContentProfile)
			slug := name + "-" + integrationTestUUID()
			require.NoError(t, db.Create(&model.Artist{
				ID: id, ContentDocumentID: &documentID, Slug: &slug,
				Status: "ARTIST_STATUS_DRAFT", CreatedAt: time.Now().UTC(),
			}).Error)
			return id
		}
		artistRemoved := createArtist("event-delta-removed")
		artistConcurrentDelete := createArtist("event-delta-concurrent-delete")
		artistRoleChanged := createArtist("event-delta-role-changed")
		artistPeerAdd := createArtist("event-delta-peer-add")
		artistLocalAdd := createArtist("event-delta-local-add")
		originalRole := "original role"
		peerRole := "peer role"
		require.NoError(t, db.Create(&[]model.ProgramEventArtist{
			{EventID: event.Msg.Id, ArtistID: artistRemoved, Role: &originalRole, SortOrder: 0},
			{EventID: event.Msg.Id, ArtistID: artistConcurrentDelete, Role: &originalRole, SortOrder: 1},
			{EventID: event.Msg.Id, ArtistID: artistRoleChanged, Role: &originalRole, SortOrder: 2},
		}).Error)

		lockTx := lockAdminMutationRoot(t, db, "program_event", "id = '"+event.Msg.Id+"'::uuid")
		result := make(chan error, 1)
		go func() {
			_, err := eventService.UpdateProgramEvent(ctx, connect.NewRequest(&managev1.UpdateProgramEventRequest{
				Id: event.Msg.Id,
				ObservedArtists: &managev1.ProgramEventArtistsSnapshot{Artists: []*managev1.ProgramEventArtist{
					{ArtistId: artistRemoved, Role: &originalRole, SortOrder: 0},
					{ArtistId: artistConcurrentDelete, Role: &originalRole, SortOrder: 1},
					{ArtistId: artistRoleChanged, Role: &originalRole, SortOrder: 2},
				}},
				Artists: []*managev1.ProgramEventArtist{
					{ArtistId: artistConcurrentDelete, Role: &originalRole, SortOrder: 0},
					{ArtistId: artistRoleChanged, Role: &originalRole, SortOrder: 1},
					{ArtistId: artistLocalAdd, Role: &originalRole, SortOrder: 2},
				},
			}))
			result <- err
		}()
		requireAdminMutationWaiting(t, result)

		require.NoError(t, lockTx.Create(&model.ProgramEventArtist{
			EventID: event.Msg.Id, ArtistID: artistPeerAdd, Role: &peerRole, SortOrder: 4,
		}).Error)
		require.NoError(t, lockTx.Where("event_id = ? AND artist_id = ?", event.Msg.Id, artistConcurrentDelete).
			Delete(&model.ProgramEventArtist{}).Error)
		require.NoError(t, lockTx.Model(&model.ProgramEventArtist{}).
			Where("event_id = ? AND artist_id = ?", event.Msg.Id, artistRoleChanged).
			Updates(map[string]any{"role": peerRole}).Error)
		require.NoError(t, lockTx.Commit().Error)
		require.NoError(t, <-result)

		var after []model.ProgramEventArtist
		require.NoError(t, db.Where("event_id = ?", event.Msg.Id).Order("sort_order ASC, artist_id ASC").Find(&after).Error)
		require.Equal(t, []string{artistRoleChanged, artistPeerAdd, artistLocalAdd}, []string{after[0].ArtistID, after[1].ArtistID, after[2].ArtistID})
		require.Equal(t, peerRole, *after[0].Role, "a peer role update to an unchanged row survives")
		require.Equal(t, peerRole, *after[1].Role, "an unseen concurrent addition survives")
		require.Equal(t, originalRole, *after[2].Role, "the intended local addition is applied")
	})
}
