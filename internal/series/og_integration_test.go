//go:build integration

package series_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	seriesadapter "github.com/echovisionlab/geul-api/internal/adapters/series"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// The Series OG requests and projection are og's SQL over the Series rows;
// these tests pin them with PostgreSQL alone.

type seriesOgSnapshot struct {
	Title string `json:"title"`
}

func TestResolveSeriesOgGenerationUsesLocaleTitlesAndAllowsNoFeaturedImageIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	seriesID := seedSeriesRow(t, db, "English Series", "localized-series-"+testutil.IntegrationUUID(), model.SeriesStatusPublished)
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`
		INSERT INTO series_translation (
			entity_id, locale, title, created_at, updated_at
		) VALUES (?::uuid, 'ko', '한국어 시리즈', ?, ?)
	`, seriesID, now, now).Error)

	resolve := func(selection *managev1.OgTargetSelection) []og.Request {
		t.Helper()
		requests, err := og.ResolveRequests(t.Context(), ogpostgres.Bind(db, ogpostgres.Ports{Requests: ogpostgres.NewResolver(seriesadapter.NewRequests())}), "series", seriesID, og.SelectionFromProto(selection))
		require.NoError(t, err)
		return requests
	}

	primary := resolve(&managev1.OgTargetSelection{Target: &managev1.OgTargetSelection_Primary{
		Primary: &managev1.OgPrimaryTarget{},
	}})
	require.Equal(t, []og.Request{{
		Target: og.Target{
			EntityType: "series", EntityID: seriesID, Locale: text("en"), Kind: "locale",
		},
		Title: "English Series",
	}}, primary)

	allLocales := resolve(&managev1.OgTargetSelection{Target: &managev1.OgTargetSelection_AllLocales{
		AllLocales: &managev1.OgAllLocaleTargets{},
	}})
	require.Len(t, allLocales, 2)
	byLocale := make(map[string]og.Request, len(allLocales))
	for _, request := range allLocales {
		require.NotNil(t, request.Locale)
		require.Nil(t, request.FeaturedImageFileID, "missing Featured Image must produce a title-only request")
		byLocale[*request.Locale] = request
	}
	require.Equal(t, "English Series", byLocale["en"].Title)
	require.Equal(t, "한국어 시리즈", byLocale["ko"].Title)

	global, err := seriesadapter.NewRequests().All(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, allLocales, global)
}

func TestSeriesOgRequestReleasesStaleLocaleProjectionUntilReplacementIsReadyIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	seriesID := seedSeriesRow(
		t,
		db,
		"Pending Fallback Series",
		"pending-fallback-series-"+testutil.IntegrationUUID(),
		model.SeriesStatusPublished,
	)
	oldAsset := seedHardCutReadyPublicAsset(t, db, "og", "webp", "image/webp", nil)
	require.NoError(t, db.Table("series_translation").
		Where("entity_id = ? AND locale = ?", seriesID, "en").
		Update("og_asset_id", oldAsset.GetAssetId()).Error)
	require.NoError(t, db.Create(&model.PublicAssetBinding{
		AssetID: oldAsset.GetAssetId(), OwnerType: "series", OwnerID: seriesID, BindingKey: "og_en",
	}).Error)

	var plan *og.Plan
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var err error
		plan, err = newOGRefresher(db).RequestCurrentWithDB(
			t.Context(),
			tx,
			managev1.OgEntityType_OG_ENTITY_TYPE_SERIES,
			seriesID,
			"en",
			false,
			"series_featured_image_updated",
		)
		return err
	}))
	require.NotNil(t, plan)
	require.Len(t, plan.GenerationIDs, 1)

	var projection struct {
		OgAssetID *string `gorm:"column:og_asset_id"`
	}
	require.NoError(t, db.Table("series_translation").
		Select("og_asset_id").
		Where("entity_id = ? AND locale = ?", seriesID, "en").
		Take(&projection).Error)
	require.Nil(t, projection.OgAssetID)

	var bindingCount int64
	require.NoError(t, db.Model(&model.PublicAssetBinding{}).
		Where("owner_type = ? AND owner_id = ? AND binding_key = ?", "series", seriesID, "og_en").
		Count(&bindingCount).Error)
	require.Zero(t, bindingCount)
	requirePublicAssetDeletePending(t, db, oldAsset.GetAssetId())

	var generation model.OgGeneration
	require.NoError(t, db.First(&generation, "id = ?", plan.GenerationIDs[0]).Error)
	require.Equal(t, model.OgGenerationStatusQueued, generation.Status)
}

func seedHardCutReadyPublicAsset(t *testing.T, db *gorm.DB, kind, extension, mimeType string, sourceFileID *string) *commonv1.AssetRef {
	t.Helper()
	asset, _, err := mediaasset.AllocationResultProto(mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(db), "https://cdn.example.com").AllocatePublicAsset(t.Context(), mediaasset.Allocation{
		SourceFileID: sourceFileID, Kind: kind, Extension: extension, MimeType: mimeType,
		Disposition: mediaasset.DispositionInline,
	}))
	require.NoError(t, err)
	now := time.Now().UTC()
	fileSize := int64(128)
	digest := make([]byte, 32)
	digest[0] = 1
	require.NoError(t, db.Model(&model.PublicAsset{}).Where("id = ?", asset.ID).Updates(map[string]any{
		"status": model.PublicAssetStatusReady, "file_size": fileSize, "sha256": digest,
		"ready_at": now, "updated_at": now,
	}).Error)
	return &commonv1.AssetRef{AssetId: asset.ID, Extension: extension, MimeType: mimeType}
}

func requirePublicAssetDeletePending(t *testing.T, db *gorm.DB, assetID string) {
	t.Helper()
	var asset model.PublicAsset
	require.NoError(t, db.Select("id", "status").Take(&asset, "id = ?", assetID).Error)
	require.Equal(t, model.PublicAssetStatusDeletePending, asset.Status)
}

func TestAutomaticOgRequestReloadsCanonicalSeriesSnapshotAfterTargetLockIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	seriesID := uuid.NewString()
	now := time.Now().UTC()
	contentDocumentID := uuid.NewString()
	contentDocumentRevision := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile, revision, created_at, updated_at) VALUES (?, 'compact', ?, ?, ?)`,
		contentDocumentID, contentDocumentRevision, now, now,
	).Error)
	require.NoError(t, db.Create(&model.Series{
		ID: seriesID, Slug: "series-" + seriesID, SourceLocale: "en",
		ContentDocumentID: contentDocumentID,
		Status:            model.SeriesStatusPublished, CreatedAt: now, UpdatedAt: &now,
	}).Error)
	require.NoError(t, db.Table("series_translation").Create(&model.SeriesTranslation{
		EntityID: seriesID, Locale: "en",
		Title: new("Title A"), CreatedAt: now, UpdatedAt: now,
	}).Error)
	locale := "en"
	target := model.OgGenerationTarget{
		ID: uuid.NewString(), EntityType: "series", EntityID: seriesID,
		TargetKind: "locale", Locale: &locale, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&target).Error)

	blocker := db.WithContext(ctx).Begin()
	require.NoError(t, blocker.Error)
	t.Cleanup(func() { _ = blocker.Rollback().Error })
	require.NoError(t, blocker.Exec("SELECT 1 FROM og_generation_target WHERE id = ? FOR UPDATE", target.ID).Error)

	refresher := newOGRefresher(db)
	result := make(chan struct {
		plan *og.Plan
		err  error
	}, 1)
	go func() {
		var plan *og.Plan
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var requestErr error
			plan, requestErr = refresher.RequestCurrentWithDB(
				ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_SERIES,
				seriesID, "en", false, "series_concurrent_update",
			)
			return requestErr
		})
		result <- struct {
			plan *og.Plan
			err  error
		}{plan: plan, err: err}
	}()
	testutil.RequireLockWaiters(t, db, 1)
	require.NoError(t, db.WithContext(ctx).Table("series_translation").
		Where("entity_id = ? AND locale = ?", seriesID, "en").
		Updates(structured.Fields{"title": "Title B", "updated_at": time.Now().UTC()}).Error)
	require.NoError(t, blocker.Rollback().Error)

	requested := <-result
	require.NoError(t, requested.err)
	require.NotNil(t, requested.plan)
	require.Len(t, requested.plan.GenerationIDs, 1)
	var generation model.OgGeneration
	require.NoError(t, db.First(&generation, "id = ?", requested.plan.GenerationIDs[0]).Error)
	var snapshot seriesOgSnapshot
	require.NoError(t, json.Unmarshal(generation.EntitySnapshot, &snapshot))
	require.Equal(t, "Title B", snapshot.Title)
}
