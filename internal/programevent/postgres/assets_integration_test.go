//go:build integration

package programeventpostgres_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil/filetest"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	programeventadapter "github.com/echovisionlab/geul-api/internal/adapters/programevent"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/programevent"
	programeventpostgres "github.com/echovisionlab/geul-api/internal/programevent/postgres"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	// TestPostgresStoreBindsPosterAssetsIntegration covers the public asset
	// bindings of event posters and series posters: which ready asset a File
	// binds, which Files are refused, and that releasing a binding keeps the
	// File and its asset.
)

func TestPostgresStoreBindsPosterAssetsIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	runtime := programeventadapter.NewRuntime("https://cdn.example.com")
	inStore := func(t *testing.T, work func(store programevent.Store) error) error {
		t.Helper()
		return db.Transaction(func(tx *gorm.DB) error {
			return work(programeventpostgres.Bind(tx, programeventpostgres.Ports{Media: runtime}, nil).Store)
		})
	}
	bindings := func(t *testing.T, ownerType, ownerID string) map[string]string {
		t.Helper()
		var rows []model.PublicAssetBinding
		require.NoError(t, db.Where("owner_type = ? AND owner_id = ?", ownerType, ownerID).Find(&rows).Error)
		byKey := map[string]string{}
		for _, row := range rows {
			byKey[row.BindingKey] = row.AssetID
		}
		return byKey
	}
	requireKept := func(t *testing.T, fileID, assetID string) {
		t.Helper()
		var asset model.PublicAsset
		require.NoError(t, db.First(&asset, "id = ?", assetID).Error)
		require.Equal(t, model.PublicAssetStatusReady, asset.Status)
		var files int64
		require.NoError(t, db.Table("file").Where("id = ?", fileID).Count(&files).Error)
		require.EqualValues(t, 1, files)
	}

	t.Run("an event poster binds the ready poster or image asset of its File", func(t *testing.T) {
		t.Parallel()
		eventID := uuid.NewString()
		for _, kind := range []string{"poster", "image"} {
			fileID, assetID := filetest.SeedCanonicalPublicFileFixture(t, db, kind+".webp", "image/webp", kind)
			mediaID := uuid.NewString()
			require.NoError(t, inStore(t, func(store programevent.Store) error {
				return store.BindPosterMedia(t.Context(), eventID, mediaID, fileID)
			}))
			require.Equal(t, assetID, bindings(t, "program_event", eventID)["media_"+mediaID])
		}
		require.Len(t, bindings(t, "program_event", eventID), 2)

		fileID, assetID := filetest.SeedCanonicalPublicFileFixture(t, db, "released.webp", "image/webp", "poster")
		mediaID := uuid.NewString()
		require.NoError(t, inStore(t, func(store programevent.Store) error {
			if err := store.BindPosterMedia(t.Context(), eventID, mediaID, fileID); err != nil {
				return err
			}
			return store.ReleaseMediaBinding(t.Context(), eventID, mediaID)
		}))
		require.NotContains(t, bindings(t, "program_event", eventID), "media_"+mediaID)
		require.Len(t, bindings(t, "program_event", eventID), 2, "releasing one media keeps the others")
		requireKept(t, fileID, assetID)

		require.NoError(t, inStore(t, func(store programevent.Store) error {
			return store.ReleaseEventMedia(t.Context(), eventID)
		}))
		require.Empty(t, bindings(t, "program_event", eventID))
	})

	t.Run("an event poster refuses a File without a ready image asset", func(t *testing.T) {
		t.Parallel()
		eventID := uuid.NewString()
		notReadyFileID, notReadyAssetID := filetest.SeedCanonicalPublicFileFixture(t, db, "not-ready.webp", "image/webp", "image")
		require.NoError(t, db.Model(&model.PublicAsset{}).Where("id = ?", notReadyAssetID).Update("status", model.PublicAssetStatusAllocated).Error)
		nonImageFileID, _ := filetest.SeedCanonicalPublicFileFixture(t, db, "waveform.json", "application/json", "waveform")
		missingFileID, missingAssetID := filetest.SeedCanonicalPublicFileFixture(t, db, "missing.webp", "image/webp", "image")
		require.NoError(t, db.Delete(&model.PublicAsset{}, "id = ?", missingAssetID).Error)
		for name, fileID := range map[string]string{"not ready": notReadyFileID, "not an image": nonImageFileID, "no asset": missingFileID} {
			err := inStore(t, func(store programevent.Store) error {
				return store.BindPosterMedia(t.Context(), eventID, uuid.NewString(), fileID)
			})
			require.Equal(t, connect.CodeNotFound, errs.CodeOf(err), name)
		}
		require.Empty(t, bindings(t, "program_event", eventID))
	})

	t.Run("a series poster binds, rebinds and releases without losing Files", func(t *testing.T) {
		t.Parallel()
		seriesID := uuid.NewString()
		firstFileID, firstAssetID := filetest.SeedCanonicalPublicFileFixture(t, db, "first.webp", "image/webp", "poster")
		secondFileID, secondAssetID := filetest.SeedCanonicalPublicFileFixture(t, db, "second.webp", "image/webp", "image")
		require.NoError(t, inStore(t, func(store programevent.Store) error {
			return store.BindSeriesPoster(t.Context(), seriesID, firstFileID)
		}))
		require.Equal(t, map[string]string{"poster": firstAssetID}, bindings(t, "program_event_series", seriesID))

		require.NoError(t, inStore(t, func(store programevent.Store) error {
			if err := store.ReleaseSeriesPoster(t.Context(), seriesID); err != nil {
				return err
			}
			return store.BindSeriesPoster(t.Context(), seriesID, secondFileID)
		}))
		require.Equal(t, map[string]string{"poster": secondAssetID}, bindings(t, "program_event_series", seriesID))
		requireKept(t, firstFileID, firstAssetID)

		require.NoError(t, inStore(t, func(store programevent.Store) error {
			return store.ReleaseSeriesPoster(t.Context(), seriesID)
		}))
		require.Empty(t, bindings(t, "program_event_series", seriesID))
		requireKept(t, secondFileID, secondAssetID)
	})
}
