//go:build integration

package ogruntime_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/testutil/filetest"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/config"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func requestCleanupOgGeneration(
	ctx context.Context,
	db *gorm.DB,
	planner *ogpostgres.Planner,
	triggerKind string,
	reason string,
	requests []og.Request,
) (*og.Plan, error) {
	var plan *og.Plan
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var requestErr error
		plan, requestErr = planner.RequestBulkReloadedWithDB(
			ctx,
			tx,
			triggerKind,
			reason,
			requests,
			func(context.Context, *gorm.DB) ([]og.Request, error) {
				return requests, nil
			},
		)
		return requestErr
	})
	if err != nil {
		return nil, err
	}
	return plan, nil
}

func cancelCleanupOgGeneration(
	ctx context.Context,
	db *gorm.DB,
	entityType managev1.OgEntityType,
	entityID string,
) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return ogpostgres.CancelEntity(ctx, tx, entityType, entityID)
	})
}

func TestUnboundOgCleanupUsesProductionPointerSchemaIntegration(t *testing.T) {
	t.Parallel()
	db := newCleanupDB(t)
	now := time.Now().UTC()
	cutoff := now.Add(-unboundOgAssetRetention)
	unbound := seedPostgresReadyOgAsset(t, db, now.Add(-31*24*time.Hour))
	bound := seedPostgresReadyOgAsset(t, db, now.Add(-31*24*time.Hour))
	referenced := seedPostgresReadyOgAsset(t, db, now.Add(-31*24*time.Hour))
	require.NoError(t, db.Create(&model.PublicAssetBinding{
		AssetID: bound.ID, OwnerType: "work", OwnerID: uuid.NewString(), BindingKey: "og",
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	seriesID := uuid.NewString()
	contentDocumentID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO content_document (id, profile, revision)
		VALUES (?, 'compact', ?)`, contentDocumentID, uuid.NewString()).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO series (id, slug, status, source_locale, content_document_id)
		VALUES (?, ?, 'SERIES_STATUS_DRAFT', 'en', ?)`, seriesID, "cleanup-regression-"+seriesID, contentDocumentID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO series_translation (
			entity_id, locale, title, og_asset_id
		) VALUES (?, 'en', 'Cleanup regression', ?)`,
		seriesID, referenced.ID,
	).Error)

	require.NoError(t, ogCleanupForIntegration(&cleanupFixture{db: db}).MarkUnboundReadyAssets(t.Context(), cutoff, now))
	assertCleanupAssetStatus(t, db, unbound.ID, model.PublicAssetStatusDeletePending)
	assertCleanupAssetStatus(t, db, bound.ID, model.PublicAssetStatusReady)
	assertCleanupAssetStatus(t, db, referenced.ID, model.PublicAssetStatusReady)
}

func TestExpiredUnreadyCleanupSkipsOgGenerationAssetsBeforeS3DeleteIntegration(t *testing.T) {
	t.Parallel()
	db := newCleanupDB(t)
	now := time.Now().UTC()
	plan, err := requestCleanupOgGeneration(
		t.Context(), db, newCleanupOGPlanner(db, "https://cdn.example.com"), "automatic", "cleanup_regression", []og.Request{
			cleanupOGRequest(managev1.OgEntityType_OG_ENTITY_TYPE_WORK, uuid.NewString(), "cleanup regression", nil, nil),
		},
	)
	require.NoError(t, err)
	require.Len(t, plan.GenerationIDs, 1)
	assetID := plan.GenerationIDs[0]
	require.NoError(t, db.Model(&model.PublicAsset{}).Where("id = ?", assetID).
		Update("created_at", now.Add(-25*time.Hour)).Error)

	deleteRequests := 0
	s3Client := filetest.NewS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		deleteRequests++
		http.Error(w, "OG generation object must not enter generic unready cleanup", http.StatusInternalServerError)
	}))
	fixture := &cleanupFixture{
		db: db, s3Client: s3Client, config: &config.Config{S3Bucket: "media"},
	}
	require.NoError(t, publicAssetCleanupForIntegration(fixture).DeleteExpiredUnready(t.Context(), now.Add(-unreadyAssetRetention)))
	require.Zero(t, deleteRequests, "cleanup must filter OG generation assets before any S3 delete")

	var count int64
	require.NoError(t, db.Model(&model.PublicAsset{}).Where("id = ?", assetID).Count(&count).Error)
	require.EqualValues(t, 1, count, "OG lifecycle history retains the generation-backed asset")
}

func TestTerminalOgGenerationCleanupRecoversPutAfterCompletionOutageIntegration(t *testing.T) {
	t.Parallel()
	db := newCleanupDB(t)
	ctx := context.Background()
	s3Client, cfg := filetest.NewS3(t)
	now := time.Now().UTC()
	old := now.Add(-25 * time.Hour)

	cloudflare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		var payload cloudflarePurgeRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.Len(t, payload.Prefixes, 3)
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"success":true,"errors":[]}`))
		require.NoError(t, err)
	}))
	t.Cleanup(cloudflare.Close)
	cfg.CDNURL = "https://cdn.example.com"
	cfg.CloudflareAPIURL = cloudflare.URL
	cfg.CloudflareCachePurgeEnabled = true
	cfg.CloudflareZoneID = "test-zone"
	cfg.CloudflareAPIToken = "test-token"

	planner := newCleanupOGPlanner(db, cfg.CDNURL)
	lifecycle := newCleanupOGLifecycle(db, cfg.CDNURL)
	createPlanWithObject := func(reason string) (*og.Plan, string) {
		work := seedCleanupOGWork(t, db)
		plan, err := requestCleanupOgGeneration(ctx, db, planner, "automatic", reason, []og.Request{
			cleanupOGRequest(managev1.OgEntityType_OG_ENTITY_TYPE_WORK, work.ID, reason, nil, nil),
		})
		require.NoError(t, err)
		var asset model.PublicAsset
		require.NoError(t, db.First(&asset, "id = ?", plan.GenerationIDs[0]).Error)
		require.NoError(t, filetest.PutObject(ctx, s3Client, cfg.S3Bucket, asset.ObjectKey, reason))
		require.NoError(t, db.Model(&model.PublicAsset{}).Where("id = ?", asset.ID).Update("created_at", old).Error)
		return plan, work.ID
	}

	// The worker has written each object, but API completion never succeeded.
	// Superseding or cancelling then makes redelivery Claim terminal/SKIP, so
	// cleanup is the durable recovery path for the abandoned object.
	superseded, supersededWorkID := createPlanWithObject("completion outage then supersede")
	_, err := requestCleanupOgGeneration(ctx, db, planner, "automatic", "replacement", []og.Request{
		cleanupOGRequest(managev1.OgEntityType_OG_ENTITY_TYPE_WORK, supersededWorkID, "replacement", nil, nil),
	})
	require.NoError(t, err)

	cancelled, cancelledWorkID := createPlanWithObject("completion outage then cancel")
	require.NoError(t, cancelCleanupOgGeneration(ctx, db, managev1.OgEntityType_OG_ENTITY_TYPE_WORK, cancelledWorkID))

	failed, _ := createPlanWithObject("completion outage then permanent failure")
	claim, err := lifecycle.Claim(ctx, failed.GenerationIDs[0])
	require.NoError(t, err)
	require.Equal(t, og.Claimed, claim.Result)
	require.NoError(t, lifecycle.Fail(ctx, failed.GenerationIDs[0], claim.LeaseToken, og.FailureCodeProcessingFailed))

	terminalIDs := []string{superseded.GenerationIDs[0], cancelled.GenerationIDs[0], failed.GenerationIDs[0]}
	require.NoError(t, ogCleanupForIntegration(&cleanupFixture{db: db}).MarkExpiredTerminalGenerationAssets(ctx, now.Add(-unreadyAssetRetention), now))
	for _, assetID := range terminalIDs {
		assertCleanupAssetStatus(t, db, assetID, model.PublicAssetStatusDeletePending)
	}

	fixture := &cleanupFixture{db: db, config: cfg, s3Client: s3Client, httpClient: cloudflare.Client()}
	require.NoError(t, publicAssetCleanupForIntegration(fixture).DeletePending(ctx, now))
	for _, assetID := range terminalIDs {
		assertCleanupAssetStatus(t, db, assetID, model.PublicAssetStatusDeleted)
		var generationCount int64
		require.NoError(t, db.Model(&model.OgGeneration{}).Where("id = ?", assetID).Count(&generationCount).Error)
		require.EqualValues(t, 1, generationCount, "terminal generation history must be retained")
	}
	remainingKeys := filetest.ListKeys(t, ctx, s3Client, cfg.S3Bucket, "asset/")
	for _, assetID := range terminalIDs {
		var asset model.PublicAsset
		require.NoError(t, db.First(&asset, "id = ?", assetID).Error)
		require.NotContains(t, remainingKeys, asset.ObjectKey)
	}
}

func TestTerminalOgGenerationCleanupProtectsActiveBoundAndPointedAssetsIntegration(t *testing.T) {
	t.Parallel()
	db := newCleanupDB(t)
	now := time.Now().UTC()
	old := now.Add(-25 * time.Hour)
	planner := newCleanupOGPlanner(db, "https://cdn.example.com")

	create := func(name string) (*og.Plan, model.Work) {
		work := seedCleanupOGWork(t, db)
		plan, err := requestCleanupOgGeneration(t.Context(), db, planner, "automatic", name, []og.Request{
			cleanupOGRequest(managev1.OgEntityType_OG_ENTITY_TYPE_WORK, work.ID, name, nil, nil),
		})
		require.NoError(t, err)
		require.NoError(t, db.Model(&model.PublicAsset{}).Where("id = ?", plan.GenerationIDs[0]).Update("created_at", old).Error)
		return plan, work
	}

	for _, status := range []string{
		model.OgGenerationStatusQueued,
		model.OgGenerationStatusProcessing,
	} {
		plan, _ := create("active " + status)
		updates := map[string]interface{}{"status": status}
		switch status {
		case model.OgGenerationStatusProcessing:
			updates["processing_at"] = now
			updates["lease_token"] = uuid.NewString()
			updates["lease_expires_at"] = now.Add(10 * time.Minute)
		}
		require.NoError(t, db.Model(&model.OgGeneration{}).Where("id = ?", plan.GenerationIDs[0]).Updates(updates).Error)
	}

	bound, boundWork := create("terminal bound")
	_, err := requestCleanupOgGeneration(t.Context(), db, planner, "automatic", "terminal bound replacement", []og.Request{
		cleanupOGRequest(managev1.OgEntityType_OG_ENTITY_TYPE_WORK, boundWork.ID, "replacement", nil, nil),
	})
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.PublicAssetBinding{
		AssetID: bound.GenerationIDs[0], OwnerType: "work", OwnerID: boundWork.ID, BindingKey: "og",
		CreatedAt: now, UpdatedAt: now,
	}).Error)

	pointed, pointedWork := create("terminal pointed")
	_, err = requestCleanupOgGeneration(t.Context(), db, planner, "automatic", "terminal pointed replacement", []og.Request{
		cleanupOGRequest(managev1.OgEntityType_OG_ENTITY_TYPE_WORK, pointedWork.ID, "replacement", nil, nil),
	})
	require.NoError(t, err)
	// Work OG pointers live on the translation rows; the base column is legacy.
	require.NoError(t, db.Exec(
		`INSERT INTO work_translation (entity_id, locale, og_asset_id) VALUES (?, 'en', ?)`,
		pointedWork.ID, pointed.GenerationIDs[0],
	).Error)

	require.NoError(t, ogCleanupForIntegration(&cleanupFixture{db: db}).MarkExpiredTerminalGenerationAssets(t.Context(), now.Add(-unreadyAssetRetention), now))
	for _, assetID := range []string{bound.GenerationIDs[0], pointed.GenerationIDs[0]} {
		assertCleanupAssetStatus(t, db, assetID, model.PublicAssetStatusAllocated)
	}
	var activeAssets []model.PublicAsset
	require.NoError(t, db.Joins("JOIN og_generation ON og_generation.id = public_asset.id").
		Where("og_generation.status IN ?", []string{
			model.OgGenerationStatusQueued,
			model.OgGenerationStatusProcessing,
		}).Find(&activeAssets).Error)
	for _, asset := range activeAssets {
		if asset.CreatedAt.Equal(old) {
			require.Equal(t, model.PublicAssetStatusAllocated, asset.Status)
		}
	}
}

func seedCleanupOGWork(t *testing.T, db *gorm.DB) model.Work {
	t.Helper()
	documentID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile, revision) VALUES (?, 'work', ?)`,
		documentID,
		uuid.NewString(),
	).Error)
	work := model.Work{
		ID: uuid.NewString(), ContentDocumentID: &documentID, Type: "WORK_TYPE_MUSIC_PROJECT",
		Year: 2026, Month: 7, IsPresent: true, Status: "WORK_STATUS_PUBLISHED",
	}
	require.NoError(t, db.Create(&work).Error)
	return work
}

func seedPostgresReadyOgAsset(t *testing.T, db *gorm.DB, readyAt time.Time) model.PublicAsset {
	t.Helper()
	assetID := uuid.NewString()
	objectKey, err := mediaauth.AssetObjectKey(assetID, "webp")
	require.NoError(t, err)
	payload := []byte(assetID)
	digest := sha256.Sum256(payload)
	size := int64(len(payload))
	asset := model.PublicAsset{
		ID: assetID, Kind: "og", ObjectKey: objectKey, Extension: "webp", MimeType: "image/webp",
		FileSize: &size, SHA256: digest[:], Disposition: "inline", Status: model.PublicAssetStatusReady,
		ReadyAt: &readyAt, CreatedAt: readyAt, UpdatedAt: readyAt,
	}
	require.NoError(t, db.Create(&asset).Error)
	return asset
}

func assertCleanupAssetStatus(t *testing.T, db *gorm.DB, assetID, status string) {
	t.Helper()
	var asset model.PublicAsset
	require.NoError(t, db.First(&asset, "id = ?", assetID).Error)
	require.Equal(t, status, asset.Status)
}
