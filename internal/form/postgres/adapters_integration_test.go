//go:build integration

package formpostgres_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	formadapter "github.com/echovisionlab/geul-api/internal/adapters/form"
	formogadapter "github.com/echovisionlab/geul-api/internal/adapters/form/og"
	"github.com/echovisionlab/geul-api/internal/form"
	formpostgres "github.com/echovisionlab/geul-api/internal/form/postgres"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// These tests check the production media and OG adapters behind the Store
// Ports on PostgreSQL alone.

const cdnDomain = "https://cdn.example.test"

func adapterStore(t *testing.T, db *gorm.DB) func(tx *gorm.DB) form.Store {
	t.Helper()
	planner := ogpostgres.NewPlanner(cdnDomain, renderConfig{}, formogadapter.NewProjection())
	ports := formpostgres.Ports{
		Assets: formadapter.NewAssets(cdnDomain),
		OG:     formogadapter.NewOG(cdnDomain, ogpostgres.NewRefresher(planner, ogpostgres.NewResolver(formogadapter.NewRequests()))),
	}
	return func(tx *gorm.DB) form.Store { return formpostgres.NewStore(tx, ports) }
}

// seedForm stores a Form with source locale en and returns its ID.
func seedForm(t *testing.T, db *gorm.DB) string {
	t.Helper()
	formID, documentID := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile) VALUES (?, 'compact')`, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO form (id, status, source_locale, content_document_id) VALUES (?, 'FORM_STATUS_DRAFT', 'en', ?)`, formID, documentID).Error)
	return formID
}

func seedLocale(t *testing.T, db *gorm.DB, formID, locale, title string) {
	t.Helper()
	require.NoError(t, db.Exec(`INSERT INTO form_translation (entity_id, locale, title, content_json) VALUES (?, ?, ?, '{"id":"schema","steps":[]}')`, formID, locale, title).Error)
}

func TestFeaturedImageReleaseKeepsTheFileAndItsAssetIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	bind := adapterStore(t, db)
	formID := seedForm(t, db)
	fileID := uuid.NewString()
	digest := sha256.Sum256([]byte(fileID))
	require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256) VALUES (?, ?, 'image/webp', 1024, 'webp', ?)`, fileID, "form-"+fileID, digest[:]).Error)
	assetID := uuid.NewString()
	objectKey, err := mediaauth.AssetObjectKey(assetID, "webp")
	require.NoError(t, err)
	now := time.Now().UTC()
	fileSize := int64(1024)
	require.NoError(t, db.Create(&model.PublicAsset{
		ID: assetID, SourceFileID: &fileID, Kind: "image", ObjectKey: objectKey,
		Extension: "webp", MimeType: "image/webp", FileSize: &fileSize, SHA256: digest[:],
		Disposition: "inline", Status: model.PublicAssetStatusReady, ReadyAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		store := bind(tx)
		if err := store.SetFeaturedImage(t.Context(), formID, &fileID, now); err != nil {
			return err
		}
		asset, err := store.BindFeaturedImage(t.Context(), formID, fileID)
		require.NoError(t, err)
		require.Equal(t, assetID, asset.ID)
		return nil
	}))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		image := bind(tx).FeaturedImage(t.Context(), formID)
		require.NotNil(t, image)
		require.Equal(t, assetID, image.ID)
		return bind(tx).ReleaseFeaturedImage(t.Context(), formID)
	}))

	var bindings int64
	require.NoError(t, db.Model(&model.PublicAssetBinding{}).Where("owner_type = 'form' AND owner_id = ? AND binding_key = 'featured_image'", formID).Count(&bindings).Error)
	require.Zero(t, bindings)
	var asset model.PublicAsset
	require.NoError(t, db.Select("id", "status").Take(&asset, "id = ?", assetID).Error)
	require.Equal(t, model.PublicAssetStatusReady, asset.Status)
	var files int64
	require.NoError(t, db.Table("file").Where("id = ?", fileID).Count(&files).Error)
	require.Equal(t, int64(1), files)
}

func TestOGCancellationFailureFailsTheTransactionIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	bind := adapterStore(t, db)
	formID := seedForm(t, db)
	locale := "en"
	planner := ogpostgres.NewPlanner(cdnDomain, renderConfig{}, formogadapter.NewProjection())
	request := og.Request{Target: og.Target{EntityType: "form", EntityID: formID, Locale: &locale, Kind: "locale"}, Title: "Delete rollback fixture"}
	var plan *og.Plan
	err := db.Transaction(func(tx *gorm.DB) (err error) {
		plan, err = planner.RequestBulkReloadedWithDB(t.Context(), tx, "automatic", "delete rollback fixture", []og.Request{request}, func(context.Context, *gorm.DB) ([]og.Request, error) {
			return []og.Request{request}, nil
		})
		return err
	})
	require.NoError(t, err)
	require.Len(t, plan.GenerationIDs, 1)
	// The first durable cancellation write fails, so the deleting
	// transaction rolls back instead of leaving a live generation.
	require.NoError(t, db.Exec(`
CREATE FUNCTION fail_test_og_cancel() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'forced OG cancellation failure';
END;
$$`).Error)
	require.NoError(t, db.Exec(fmt.Sprintf(`
CREATE TRIGGER fail_test_og_cancel
BEFORE UPDATE ON og_generation
FOR EACH ROW
WHEN (OLD.id = '%s')
EXECUTE FUNCTION fail_test_og_cancel()`, plan.GenerationIDs[0])).Error)

	err = db.Transaction(func(tx *gorm.DB) error {
		store := bind(tx)
		if err := store.DeleteForm(t.Context(), formID); err != nil {
			return err
		}
		return store.CancelOG(t.Context(), formID)
	})
	require.ErrorContains(t, err, "forced OG cancellation failure")

	var forms int64
	require.NoError(t, db.Table("form").Where("id = ?", formID).Count(&forms).Error)
	require.Equal(t, int64(1), forms)
	var generation model.OgGeneration
	require.NoError(t, db.First(&generation, "id = ?", plan.GenerationIDs[0]).Error)
	require.Equal(t, model.OgGenerationStatusQueued, generation.Status)
}

func TestOGRequestsTitleEachLocaleExactlyIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	formID := seedForm(t, db)
	seedLocale(t, db, formID, "ko", "원본 신청서 제목")
	seedLocale(t, db, formID, "ja", "日本語フォーム題名")
	seedLocale(t, db, formID, "fr", "Titre français exact")
	require.NoError(t, db.Table("form").Where("id = ?", formID).Update("source_locale", "ko").Error)
	resolve := func(t *testing.T, selection *managev1.OgTargetSelection) []og.Request {
		t.Helper()
		requests, err := og.ResolveRequests(t.Context(), ogpostgres.Bind(db, ogpostgres.Ports{Requests: ogpostgres.NewResolver(formogadapter.NewRequests())}),
			"form", formID, og.SelectionFromProto(selection))
		require.NoError(t, err)
		return requests
	}

	primary := resolve(t, &managev1.OgTargetSelection{Target: &managev1.OgTargetSelection_Primary{Primary: &managev1.OgPrimaryTarget{}}})
	require.Equal(t, []og.Request{{
		Target: og.Target{EntityType: "form", EntityID: formID, Locale: new("ko"), Kind: "locale"}, Title: "원본 신청서 제목",
	}}, primary)
	localized := resolve(t, &managev1.OgTargetSelection{Target: &managev1.OgTargetSelection_Locale{Locale: "ja"}})
	require.Equal(t, []og.Request{{
		Target: og.Target{EntityType: "form", EntityID: formID, Locale: new("ja"), Kind: "locale"}, Title: "日本語フォーム題名",
	}}, localized)
	allLocales := resolve(t, &managev1.OgTargetSelection{Target: &managev1.OgTargetSelection_AllLocales{AllLocales: &managev1.OgAllLocaleTargets{}}})
	byLocale := make(map[string]string, len(allLocales))
	for _, request := range allLocales {
		require.NotNil(t, request.Locale)
		byLocale[*request.Locale] = request.Title
	}
	require.Equal(t, map[string]string{"ko": "원본 신청서 제목", "ja": "日本語フォーム題名", "fr": "Titre français exact"}, byLocale)
	globalRequests, err := formogadapter.NewRequests().All(t.Context(), db)
	require.NoError(t, err)
	globalByLocale := make(map[string]string, len(globalRequests))
	for _, request := range globalRequests {
		require.NotNil(t, request.Locale)
		globalByLocale[*request.Locale] = request.Title
	}
	require.Equal(t, byLocale, globalByLocale, "global regeneration must use the same source row and locale policy")
}

type renderConfig struct{}

func (renderConfig) Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error) {
	payload := []byte(`{"site_title":"","primary_color":"#b02d23"}`)
	return payload, fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}
