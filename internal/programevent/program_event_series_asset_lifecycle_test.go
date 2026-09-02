//go:build integration

package programevent

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestProgramEventSeriesPosterBindingLifecyclePreservesFiles(t *testing.T) {
	db := newServiceIntegrationDB(t)
	ctx, spiceDB := integrationAdminCtxWithIdentityAndSpiceDB(t, db)
	readyPoster := func() *model.PublicAsset {
		fileID := seedImageBindingUploadedFileFixtureForKind(t, db, "program-event-series-poster-"+integrationTestUUID()+".webp", "poster")
		var asset model.PublicAsset
		require.NoError(t, db.Where("source_file_id = ? AND kind = ? AND status = ?", fileID, "poster", model.PublicAssetStatusReady).Take(&asset).Error)
		return &asset
	}
	first := readyPoster()
	second := readyPoster()
	third := readyPoster()
	initialSummary := "Opening weekend"
	service := NewProgramEventSeriesService(db, newProgramEventRuntime("https://cdn.example.com"), spiceDB)

	created, err := service.CreateProgramEventSeries(ctx, connect.NewRequest(&managev1.CreateProgramEventSeriesRequest{
		Title:        "Festival",
		Slug:         "festival",
		Summary:      &initialSummary,
		PosterFileId: first.SourceFileID,
	}))
	require.NoError(t, err)
	require.Equal(t, "Festival", created.Msg.Title)
	require.Equal(t, initialSummary, created.Msg.GetSummary())
	seriesID := created.Msg.Id
	requireProgramEventSeriesPosterBinding(t, db, seriesID, first.ID)

	updatedTitle := "Festival 2027"
	updated, err := service.UpdateProgramEventSeries(ctx, connect.NewRequest(&managev1.UpdateProgramEventSeriesRequest{
		Id:           seriesID,
		Title:        &updatedTitle,
		PosterFileId: second.SourceFileID,
	}))
	require.NoError(t, err)
	require.Equal(t, updatedTitle, updated.Msg.Title)
	requireProgramEventSeriesPosterBinding(t, db, seriesID, second.ID)
	requirePublicAssetStatus(t, db, first.ID, model.PublicAssetStatusReady)
	requireProgramEventSeriesSourceFile(t, db, *first.SourceFileID)

	empty := ""
	_, err = service.UpdateProgramEventSeries(ctx, connect.NewRequest(&managev1.UpdateProgramEventSeriesRequest{
		Id:           seriesID,
		PosterFileId: &empty,
	}))
	require.NoError(t, err)
	var bindingCount int64
	require.NoError(t, db.Table("public_asset_binding").
		Where("owner_type = ? AND owner_id = ?", "program_event_series", seriesID).
		Count(&bindingCount).Error)
	require.Zero(t, bindingCount)
	requirePublicAssetStatus(t, db, second.ID, model.PublicAssetStatusReady)
	requireProgramEventSeriesSourceFile(t, db, *second.SourceFileID)

	_, err = service.UpdateProgramEventSeries(ctx, connect.NewRequest(&managev1.UpdateProgramEventSeriesRequest{
		Id:           seriesID,
		PosterFileId: third.SourceFileID,
	}))
	require.NoError(t, err)
	_, err = service.DeleteProgramEventSeries(ctx, connect.NewRequest(&managev1.DeleteProgramEventSeriesRequest{Id: seriesID}))
	require.NoError(t, err)
	requirePublicAssetStatus(t, db, third.ID, model.PublicAssetStatusReady)
	requireProgramEventSeriesSourceFile(t, db, *third.SourceFileID)
}

func requireProgramEventSeriesPosterBinding(t *testing.T, db *gorm.DB, seriesID string, assetID string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Table("public_asset_binding").
		Where("owner_type = ? AND owner_id = ? AND binding_key = ? AND asset_id = ?", "program_event_series", seriesID, "poster", assetID).
		Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func requirePublicAssetStatus(t *testing.T, db *gorm.DB, assetID string, status string) {
	t.Helper()
	var asset model.PublicAsset
	require.NoError(t, db.First(&asset, "id = ?", assetID).Error)
	require.Equal(t, status, asset.Status)
}

func requireProgramEventSeriesSourceFile(t *testing.T, db *gorm.DB, fileID string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Table("file").Where("id = ?", fileID).Count(&count).Error)
	require.Equal(t, int64(1), count)
}
