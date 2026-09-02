//go:build integration

package series

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	seriesadapter "github.com/echovisionlab/geul-api/internal/adapters/series"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestResolveSeriesOgGenerationUsesLocaleTitlesAndAllowsNoFeaturedImageIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	seriesID := seedSeriesRow(t, db, "English Series", "localized-series-"+integrationTestUUID(), managev1.SeriesStatus_SERIES_STATUS_PUBLISHED.String())
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`
		INSERT INTO series_translation (
			entity_id, locale, title, created_at, updated_at
		) VALUES (?::uuid, 'ko', '한국어 시리즈', ?, ?)
	`, seriesID, now, now).Error)

	resolve := func(selection *managev1.OgTargetSelection) []og.Request {
		t.Helper()
		requests, err := og.NewResolver(seriesadapter.NewRequests()).Resolve(t.Context(), db, &managev1.RegenerateOgImageRequest{
			EntityType: managev1.OgEntityType_OG_ENTITY_TYPE_SERIES,
			EntityId:   &seriesID,
			Selection:  selection,
		})
		require.NoError(t, err)
		return requests
	}

	primary := resolve(&managev1.OgTargetSelection{Target: &managev1.OgTargetSelection_Primary{
		Primary: &managev1.OgPrimaryTarget{},
	}})
	require.Equal(t, []og.Request{{
		Target: og.Target{
			EntityType: "series", EntityID: seriesID, Locale: stringPtr("en"), Kind: "locale",
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
	db := newServiceIntegrationDB(t)
	seriesID := seedSeriesRow(
		t,
		db,
		"Pending Fallback Series",
		"pending-fallback-series-"+integrationTestUUID(),
		managev1.SeriesStatus_SERIES_STATUS_PUBLISHED.String(),
	)
	oldAsset := seedHardCutReadyPublicAsset(t, db, "og", "webp", "image/webp", nil)
	require.NoError(t, db.Table("series_translation").
		Where("entity_id = ? AND locale = ?", seriesID, "en").
		Update("og_asset_id", oldAsset.GetAssetId()).Error)
	require.NoError(t, db.Create(&model.PublicAssetBinding{
		AssetID: oldAsset.GetAssetId(), OwnerType: "series", OwnerID: seriesID, BindingKey: "og:en",
	}).Error)

	var plan *og.Plan
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var err error
		plan, err = newOGRefresherForTest(db, "https://cdn.example.com").RequestCurrentWithDB(
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
		Where("owner_type = ? AND owner_id = ? AND binding_key = ?", "series", seriesID, "og:en").
		Count(&bindingCount).Error)
	require.Zero(t, bindingCount)
	requirePublicAssetDeletePending(t, db, oldAsset.GetAssetId())

	var generation model.OgGeneration
	require.NoError(t, db.First(&generation, "id = ?", plan.GenerationIDs[0]).Error)
	require.Equal(t, model.OgGenerationStatusQueued, generation.Status)
}

func seedSeriesRow(t *testing.T, db *gorm.DB, title, slug, status string) string {
	t.Helper()
	id := integrationTestUUID()
	now := time.Now().UTC()
	contentDocumentID := integrationTestUUID()
	contentDocumentRevision := integrationTestUUID()
	require.NoError(t, db.Exec(`
		INSERT INTO content_document (id, profile, revision, created_at, updated_at)
		VALUES (?::uuid, 'compact', ?::uuid, ?, ?)`, contentDocumentID, contentDocumentRevision, now, now).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO series (id, slug, status, source_locale, content_document_id, created_at, updated_at)
		VALUES (?::uuid, ?, ?, 'en', ?::uuid, ?, ?)`, id, slug, status, contentDocumentID, now, now).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO series_translation (
			entity_id, locale, title, created_at, updated_at
		) VALUES (?::uuid, 'en', ?, ?, ?)`,
		id, title, now, now).Error)
	policy, err := policyv1.PostSeries.TouchPolicy(id)
	require.NoError(t, err)
	_, err = testutil.SetupOryStack(t).SpiceDBClient.ApplyRelationships(t.Context(), policy)
	require.NoError(t, err)
	return id
}

func seedHardCutReadyPublicAsset(t *testing.T, db *gorm.DB, kind, extension, mimeType string, sourceFileID *string) *commonv1.AssetRef {
	t.Helper()
	asset, _, err := mediaasset.NewLifecycle(db, "https://cdn.example.com").AllocatePublicAsset(t.Context(), mediaasset.Allocation{
		SourceFileID: sourceFileID, Kind: kind, Extension: extension, MimeType: mimeType,
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
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

func stringPtr(value string) *string { return &value }
