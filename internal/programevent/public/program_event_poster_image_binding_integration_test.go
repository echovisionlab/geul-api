//go:build integration

package public

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	programeventadapter "github.com/echovisionlab/geul-api/internal/adapters/programevent"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	eventdomain "github.com/echovisionlab/geul-api/internal/programevent"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestProgramEventPosterImageBindingIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	identityID := uuid.NewString()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: identityID, Name: "Poster admin"})
	memberID := seedPublicAdminMemberIdentityLink(t, db, identityID, "Poster admin")
	ctx := publicProgramEventAdminCtx(memberID, identityID)
	requestContext, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.MemberActor{
		IdentityID: identityID, MemberID: memberID, SessionID: uuid.NewString(),
	})
	require.NoError(t, err)
	ctx = sharedtelemetry.WithRequestContext(ctx, requestContext)
	store, err := contentblock.NewGeneratedStore(publicProgramEventFileReuseAuthorizer{})
	require.NoError(t, err)
	files := publicProgramEventFileGateway{db: db}
	runtime := newManageProgramEventRuntime("https://cdn.example.com")
	service := eventdomain.NewAuditedProgramEventService(
		db, runtime, files, apitelemetry.NewDurableWriter(db), publicIntegrationSpiceDB,
		programeventadapter.NewCreditMemberSummaries(db, "https://cdn.example.com"),
		eventdomain.WithProgramEventContentBlockStore(store),
	)
	typeResponse, err := eventdomain.NewProgramEventTypeService(db, publicIntegrationSpiceDB).CreateProgramEventType(ctx,
		connect.NewRequest(&managev1.CreateProgramEventTypeRequest{Slug: "poster-type-" + uuid.NewString(), Locale: "en", Name: "Poster type"}))
	require.NoError(t, err)
	event, err := service.CreateProgramEvent(ctx, connect.NewRequest(&managev1.CreateProgramEventRequest{
		Title: "Poster event", Slug: "poster-event-" + uuid.NewString(), SourceLocale: "en", TypeId: typeResponse.Msg.Id,
		StartsAt: timestamppb.New(time.Now().UTC().Add(time.Hour)), Timezone: "Asia/Seoul",
		LocationMode: managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_ONLINE,
	}))
	require.NoError(t, err)
	_, err = service.PublishProgramEvent(ctx, connect.NewRequest(&managev1.PublishProgramEventRequest{Id: event.Msg.Id}))
	require.NoError(t, err)
	publicService := NewProgramEventService(db, newPublicProgramEventAssets(db, "https://cdn.example.com"),
		programeventadapter.NewPublicCreditMemberSummaries(db, "https://cdn.example.com"),
		WithProgramEventContentBlockStore(store), WithProgramEventFileService(files))

	var primaryMediaID, primaryFileID string
	for _, kind := range []string{"image", "poster"} {
		if !t.Run("ready "+kind, func(t *testing.T) {
			fileID, assetID := seedCanonicalPublicFileFixture(t, db, kind+".webp", "image/webp", kind)
			added, err := service.AddProgramEventMedia(ctx, connect.NewRequest(&managev1.AddProgramEventMediaRequest{
				EventId: event.Msg.Id, FileId: fileID, Role: "poster", MakePrimary: true,
			}))
			require.NoError(t, err)
			require.True(t, added.Msg.Media.IsPrimary)
			var binding model.PublicAssetBinding
			require.NoError(t, db.Where("owner_type = ? AND owner_id = ? AND binding_key = ?", "program_event", event.Msg.Id, "media:"+added.Msg.Media.Id).Take(&binding).Error)
			require.Equal(t, assetID, binding.AssetID)
			require.Equal(t, fileID, *binding.SourceFileID)
			var assets []model.PublicAsset
			require.NoError(t, db.Where("source_file_id = ?", fileID).Find(&assets).Error)
			require.Len(t, assets, 1)
			require.Equal(t, assetID, assets[0].ID)
			require.Equal(t, kind, assets[0].Kind)
			require.Equal(t, model.PublicAssetStatusReady, assets[0].Status)
			fetched, err := publicService.Get(context.Background(), connect.NewRequest(&openv1.GetProgramEventRequest{Slug: event.Msg.Id}))
			require.NoError(t, err)
			require.Equal(t, assetID, fetched.Msg.Event.GetPosterAsset().GetAssetId())
			require.Equal(t, "https://cdn.example.com/asset/"+assetID+"/"+kind+".webp", fetched.Msg.Event.GetPosterAsset().GetUrl())
			primaryMediaID, primaryFileID = added.Msg.Media.Id, fileID
		}) {
			return
		}
	}

	nonReadyFileID, nonReadyAssetID := seedCanonicalPublicFileFixture(t, db, "not-ready.webp", "image/webp", "image")
	require.NoError(t, db.Model(&model.PublicAsset{}).Where("id = ?", nonReadyAssetID).Update("status", model.PublicAssetStatusAllocated).Error)
	nonImageFileID, _ := seedCanonicalPublicFileFixture(t, db, "waveform.json", "application/json", "waveform")
	missingFileID, missingAssetID := seedCanonicalPublicFileFixture(t, db, "missing.webp", "image/webp", "image")
	require.NoError(t, db.Delete(&model.PublicAsset{}, "id = ?", missingAssetID).Error)
	for _, scenario := range []struct{ name, fileID string }{
		{"non-ready image", nonReadyFileID}, {"non-image", nonImageFileID}, {"missing asset", missingFileID},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var before model.ProgramEvent
			require.NoError(t, db.First(&before, "id = ?", event.Msg.Id).Error)
			_, err := service.AddProgramEventMedia(ctx, connect.NewRequest(&managev1.AddProgramEventMediaRequest{
				EventId: event.Msg.Id, FileId: scenario.fileID, Role: "poster", MakePrimary: true,
			}))
			require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
			var media []model.ProgramEventMedia
			require.NoError(t, db.Where("event_id = ?", event.Msg.Id).Find(&media).Error)
			require.Len(t, media, 2)
			var primary model.ProgramEventMedia
			require.NoError(t, db.Where("event_id = ? AND is_primary = TRUE", event.Msg.Id).Take(&primary).Error)
			require.Equal(t, primaryMediaID, primary.ID)
			require.Equal(t, primaryFileID, primary.FileID)
			var bindingCount int64
			require.NoError(t, db.Model(&model.PublicAssetBinding{}).Where("owner_type = ? AND owner_id = ?", "program_event", event.Msg.Id).Count(&bindingCount).Error)
			require.EqualValues(t, 2, bindingCount)
			var after model.ProgramEvent
			require.NoError(t, db.First(&after, "id = ?", event.Msg.Id).Error)
			require.Equal(t, before.UpdatedAt, after.UpdatedAt)
		})
	}

	t.Run("binding errors preserved", func(t *testing.T) {
		fileID, _ := seedCanonicalPublicFileFixture(t, db, "invalid-binding.webp", "image/webp", "poster")
		_, err := runtime.BindReadyAssetForSourceFile(ctx, db, fileID, "", event.Msg.Id, "invalid", "poster")
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		var count int64
		require.NoError(t, db.Model(&model.PublicAssetBinding{}).Where("source_file_id = ?", fileID).Count(&count).Error)
		require.Zero(t, count)
	})

	t.Run("other expected kinds stay strict", func(t *testing.T) {
		fileID, _ := seedCanonicalPublicFileFixture(t, db, "strict.webp", "image/webp", "image")
		_, err := runtime.BindReadyAssetForSourceFile(ctx, db, fileID, "program_event", event.Msg.Id, "strict", "map_image")
		require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
		var count int64
		require.NoError(t, db.Model(&model.PublicAssetBinding{}).Where("source_file_id = ?", fileID).Count(&count).Error)
		require.Zero(t, count)
	})

	t.Run("series reuses canonical image", func(t *testing.T) {
		fileID, assetID := seedCanonicalPublicFileFixture(t, db, "series-image.webp", "image/webp", "image")
		series, err := eventdomain.NewProgramEventSeriesService(db, runtime, publicIntegrationSpiceDB).CreateProgramEventSeries(ctx,
			connect.NewRequest(&managev1.CreateProgramEventSeriesRequest{Title: "Image series", Slug: "image-series-" + uuid.NewString(), PosterFileId: &fileID}))
		require.NoError(t, err)
		var binding model.PublicAssetBinding
		require.NoError(t, db.Where("owner_type = ? AND owner_id = ? AND binding_key = ?", "program_event_series", series.Msg.Id, "poster").Take(&binding).Error)
		require.Equal(t, assetID, binding.AssetID)
	})
}
