//go:build integration

package mediaassetpostgres_test

import (
	"context"
	"testing"
	"time"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	errs "github.com/echovisionlab/geul-api/internal/errors"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
)

func TestMediaAssetLifecycleBindRechecksReadyAfterAssetLockIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	asset := seedMediaAssetLifecycleReadyAssetIntegration(t, db)
	ownerID := uuid.NewString()

	blocker := db.Begin()
	require.NoError(t, blocker.Error)
	t.Cleanup(func() { _ = blocker.Rollback().Error })
	var locked model.PublicAsset
	require.NoError(t, blocker.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id", "status").
		Where("id = ?", asset.ID).
		Take(&locked).Error)

	result := make(chan error, 1)
	go func() {
		result <- mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(db), "").BindPublicAsset(
			context.Background(),
			mediaasset.Binding{
				AssetID: asset.ID, OwnerType: "post", OwnerID: ownerID, BindingKey: "featured_image",
			},
		)
	}()
	testutil.RequireLockWaiters(t, db, 1)
	require.NoError(t, blocker.Model(&model.PublicAsset{}).
		Where("id = ?", asset.ID).
		Updates(structured.Fields{
			"status":              model.PublicAssetStatusDeletePending,
			"delete_requested_at": time.Now().UTC(),
			"updated_at":          time.Now().UTC(),
		}).Error)
	require.NoError(t, blocker.Commit().Error)

	err := requireMediaAssetCallResult(t, result)
	require.Equal(t, connect.CodeFailedPrecondition, errs.Wrap(err).Code())
	var bindingCount int64
	require.NoError(t, db.Model(&model.PublicAssetBinding{}).
		Where("owner_type = ? AND owner_id = ?", "post", ownerID).
		Count(&bindingCount).Error)
	require.Zero(t, bindingCount)
}

func TestMediaAssetLifecycleRebindFailsClosedWhenBindingChangesWhileWaitingIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	oldAsset := seedMediaAssetLifecycleReadyAssetIntegration(t, db)
	newAsset := seedMediaAssetLifecycleReadyAssetIntegration(t, db)
	concurrentAsset := seedMediaAssetLifecycleReadyAssetIntegration(t, db)
	ownerID := uuid.NewString()
	bindingKey := "featured_image"
	service := mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(db), "")
	require.NoError(t, service.BindPublicAsset(t.Context(), mediaasset.Binding{
		AssetID: oldAsset.ID, OwnerType: "page", OwnerID: ownerID, BindingKey: bindingKey,
	}))

	blocker := db.Begin()
	require.NoError(t, blocker.Error)
	t.Cleanup(func() { _ = blocker.Rollback().Error })
	var locked []model.PublicAsset
	require.NoError(t, blocker.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id", "status").
		Where("id IN ?", []string{oldAsset.ID, newAsset.ID}).
		Order("id ASC").
		Find(&locked).Error)
	require.Len(t, locked, 2)

	result := make(chan error, 1)
	go func() {
		result <- service.BindPublicAsset(context.Background(), mediaasset.Binding{
			AssetID: newAsset.ID, OwnerType: "page", OwnerID: ownerID, BindingKey: bindingKey,
		})
	}()
	testutil.RequireLockWaiters(t, db, 1)
	require.NoError(t, blocker.Model(&model.PublicAssetBinding{}).
		Where("owner_type = ? AND owner_id = ? AND binding_key = ?", "page", ownerID, bindingKey).
		Updates(structured.Fields{
			"asset_id":   concurrentAsset.ID,
			"updated_at": time.Now().UTC(),
		}).Error)
	require.NoError(t, blocker.Commit().Error)

	err := requireMediaAssetCallResult(t, result)
	require.Equal(t, connect.CodeFailedPrecondition, errs.Wrap(err).Code())
	var binding model.PublicAssetBinding
	require.NoError(t, db.Where(
		"owner_type = ? AND owner_id = ? AND binding_key = ?", "page", ownerID, bindingKey,
	).Take(&binding).Error)
	require.Equal(t, concurrentAsset.ID, binding.AssetID)
	requireMediaAssetState(t, db, newAsset.ID, model.PublicAssetStatusReady, false)
}

func TestMediaAssetLifecycleConcurrentFirstBindConvergesIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	firstAsset := seedMediaAssetLifecycleReadyAssetIntegration(t, db)
	secondAsset := seedMediaAssetLifecycleReadyAssetIntegration(t, db)
	ownerID := uuid.NewString()
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, assetID := range []string{firstAsset.ID, secondAsset.ID} {
		go func(assetID string) {
			<-start
			results <- mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(db), "").BindPublicAsset(
				context.Background(),
				mediaasset.Binding{
					AssetID: assetID, OwnerType: "post", OwnerID: ownerID, BindingKey: "featured_image",
				},
			)
		}(assetID)
	}
	close(start)

	errors := []error{requireMediaAssetCallResult(t, results), requireMediaAssetCallResult(t, results)}
	successes := 0
	failures := 0
	for _, err := range errors {
		if err == nil {
			successes++
			continue
		}
		failures++
		require.Equal(t, connect.CodeFailedPrecondition, errs.Wrap(err).Code())
	}
	require.GreaterOrEqual(t, successes, 1)
	require.Equal(t, 2, successes+failures)

	var bindings []model.PublicAssetBinding
	require.NoError(t, db.Where(
		"owner_type = ? AND owner_id = ? AND binding_key = ?", "post", ownerID, "featured_image",
	).Find(&bindings).Error)
	require.Len(t, bindings, 1)
	require.Contains(t, []string{firstAsset.ID, secondAsset.ID}, bindings[0].AssetID)
	requireMediaAssetState(t, db, bindings[0].AssetID, model.PublicAssetStatusReady, false)
	losingAssetID := firstAsset.ID
	if bindings[0].AssetID == firstAsset.ID {
		losingAssetID = secondAsset.ID
	}
	if successes == 2 {
		requireMediaAssetState(t, db, losingAssetID, model.PublicAssetStatusDeletePending, true)
	} else {
		requireMediaAssetState(t, db, losingAssetID, model.PublicAssetStatusReady, false)
	}
}

func seedMediaAssetLifecycleReadyAssetIntegration(t *testing.T, db *gorm.DB) model.PublicAsset {
	t.Helper()
	now := time.Now().UTC()
	fileSize := int64(16)
	assetID := uuid.NewString()
	asset := model.PublicAsset{
		ID:          assetID,
		Kind:        "image",
		ObjectKey:   "asset/" + assetID + ".webp",
		Extension:   "webp",
		MimeType:    "image/webp",
		FileSize:    &fileSize,
		SHA256:      make([]byte, 32),
		Disposition: "inline",
		Status:      model.PublicAssetStatusReady,
		ReadyAt:     &now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	require.NoError(t, db.Create(&asset).Error)
	t.Cleanup(func() {
		_ = db.Where("asset_id = ?", asset.ID).Delete(&model.PublicAssetBinding{}).Error
		_ = db.Where("id = ?", asset.ID).Delete(&model.PublicAsset{}).Error
	})
	return asset
}

func requireMediaAssetState(t *testing.T, db *gorm.DB, assetID string, status string, deletionRequested bool) {
	t.Helper()
	var asset model.PublicAsset
	require.NoError(t, db.Where("id = ?", assetID).Take(&asset).Error)
	require.Equal(t, status, asset.Status)
	if deletionRequested {
		require.NotNil(t, asset.DeleteRequestedAt)
		return
	}
	require.Nil(t, asset.DeleteRequestedAt)
}

func requireMediaAssetCallResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		require.FailNow(t, "Media Asset call did not return after its row lock was released")
		return nil
	}
}
