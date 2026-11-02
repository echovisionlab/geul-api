//go:build integration

package mediaassetruntime_test

import (
	"context"
	"crypto/sha256"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/testutil/filetest"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/config"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func TestExpiredUnreadyCleanupSerializesWithAssetCompletionIntegration(t *testing.T) {
	t.Parallel()
	pg := newCleanupPostgres(t)
	now := time.Now().UTC()
	cutoff := now.Add(-unreadyAssetRetention)
	digest := sha256.Sum256([]byte("completed"))
	s3Client := filetest.NewS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNoContent)
	}))
	fixture := &cleanupFixture{db: pg.DB, s3Client: s3Client, config: &config.Config{S3Bucket: "media"}}

	allocateExpired := func(t *testing.T) model.PublicAsset {
		t.Helper()
		asset, _, allocateErr := mediaasset.AllocationResultProto(mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(pg.DB), "").AllocatePublicAsset(
			t.Context(),
			mediaasset.Allocation{
				Kind:        "image",
				Extension:   "webp",
				MimeType:    "image/webp",
				Disposition: mediaasset.DispositionInline,
			},
		))
		require.NoError(t, allocateErr)
		require.NoError(t, pg.DB.Model(&model.PublicAsset{}).Where("id = ?", asset.ID).
			Update("created_at", now.Add(-25*time.Hour)).Error)
		t.Cleanup(func() {
			_ = pg.DB.Where("id = ?", asset.ID).Delete(&model.PublicAsset{}).Error
		})
		return mediaassetpostgres.AssetRow(*asset)
	}

	t.Run("cleanup lock wins", func(t *testing.T) {
		asset := allocateExpired(t)
		locked := make(chan struct{})
		release := make(chan struct{})
		var releaseOnce sync.Once
		releaseCleanup := func() { releaseOnce.Do(func() { close(release) }) }
		t.Cleanup(releaseCleanup)
		blockingS3Client := filetest.NewS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, http.MethodDelete, r.Method)
			close(locked)
			<-release
			w.WriteHeader(http.StatusNoContent)
		}))
		blockingFixture := &cleanupFixture{
			db: pg.DB, s3Client: blockingS3Client, config: &config.Config{S3Bucket: "media"},
		}
		cleanupDone := make(chan error, 1)
		go func() {
			cleanupDone <- publicAssetCleanupForIntegration(blockingFixture).DeleteExpiredUnready(context.Background(), cutoff)
		}()
		<-locked

		completeDone := make(chan error, 1)
		go func() {
			_, completeErr := mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(pg.DB), "").CompletePublicAsset(
				context.Background(),
				mediaasset.WrittenAssetFromProto(&commonv1.AssetWriteResult{AssetId: asset.ID, FileSize: 9, Sha256: digest[:]}),
			)
			completeDone <- completeErr
		}()
		pgtest.RequireLockWaiters(t, pg.DB, 1)
		releaseCleanup()
		require.NoError(t, <-cleanupDone)
		require.Error(t, <-completeDone)
		var count int64
		require.NoError(t, pg.DB.Model(&model.PublicAsset{}).Where("id = ?", asset.ID).Count(&count).Error)
		require.Zero(t, count)
	})

	t.Run("completion lock wins", func(t *testing.T) {
		asset := allocateExpired(t)
		tx := pg.DB.Begin()
		t.Cleanup(func() { _ = tx.Rollback().Error })
		require.NoError(t, tx.Error)
		_, err := mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(tx), "").CompletePublicAsset(
			t.Context(),
			mediaasset.WrittenAssetFromProto(&commonv1.AssetWriteResult{AssetId: asset.ID, FileSize: 9, Sha256: digest[:]}),
		)
		require.NoError(t, err)

		cleanupDone := make(chan error, 1)
		go func() {
			cleanupDone <- publicAssetCleanupForIntegration(fixture).DeleteExpiredUnready(context.Background(), cutoff)
		}()
		pgtest.RequireLockWaiters(t, pg.DB, 1)
		require.NoError(t, tx.Commit().Error)
		require.NoError(t, <-cleanupDone)
		assertCleanupAssetStatus(t, pg.DB, asset.ID, model.PublicAssetStatusReady)
	})
}
func assertCleanupAssetStatus(t *testing.T, db *gorm.DB, assetID, status string) {
	t.Helper()
	var asset model.PublicAsset
	require.NoError(t, db.First(&asset, "id = ?", assetID).Error)
	require.Equal(t, status, asset.Status)
}
