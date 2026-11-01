//go:build integration

package sitesettingspostgres_test

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	artistadapter "github.com/echovisionlab/geul-api/internal/adapters/artist"
	formogadapter "github.com/echovisionlab/geul-api/internal/adapters/form/og"
	labeladapter "github.com/echovisionlab/geul-api/internal/adapters/label"
	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	seriesadapter "github.com/echovisionlab/geul-api/internal/adapters/series"
	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	sitesettingspostgres "github.com/echovisionlab/geul-api/internal/sitesettings/postgres"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// These tests run the use cases on PostgreSQL with the production File, Page
// and OG adapters behind the Store's ports, authorization and the identity
// fence faked: what the adapters write in the settings transaction, and
// authority rechecked after waiting on the settings lock.

const cdnDomain = "https://cdn.example.test"

func newUnits(t *testing.T, db *gorm.DB) mutation.UnitOfWork[sitesettings.Store] {
	t.Helper()
	planner := og.NewPlanner(
		db, cdnDomain, sitesettingsadapter.NewRenderConfig(),
		legaladapter.NewProjection(), sitesettingsadapter.NewProjection(),
	)
	collector := og.NewCollector(
		postadapter.NewRequests(), pageadapter.NewRequests(), seriesadapter.NewRequests(),
		workadapter.NewRequests(), labeladapter.NewRequests(), artistadapter.NewRequests(),
		formogadapter.NewRequests(), legaladapter.NewRequests(), sitesettingsadapter.NewRequests(),
	)
	units, err := sitesettingspostgres.NewUnits(db, sitesettingspostgres.Ports{
		Assets:     sitesettingsadapter.NewAssets(cdnDomain),
		References: sitesettingsadapter.NewReferences(),
		OG:         sitesettingsadapter.NewInvalidator(planner, collector.Collect, legaladapter.CurrentRequests),
	})
	require.NoError(t, err)
	return units
}

func newService(t *testing.T, db *gorm.DB, checker *authztest.Checker) *sitesettings.Service {
	t.Helper()
	service, err := sitesettings.NewService(sitesettings.Deps{
		Units: newUnits(t, db), SpiceDB: checker, Fence: &identitystatetest.Fence{}, Audit: &audittest.Recorder{},
	})
	require.NoError(t, err)
	return service
}

func adminContext(t *testing.T) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.77")
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), requestContext), &auth.UserInfo{
		IdentityID: auth.IdentityID(uuid.NewString()), MemberID: auth.MemberID(uuid.NewString()),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
}

// seedImageFile stores a WebP File with a ready image asset, as an upload to
// the File library leaves it.
func seedImageFile(t *testing.T, db *gorm.DB) string {
	t.Helper()
	fileID := uuid.NewString()
	digest := sha256.Sum256([]byte(fileID))
	require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256)
		VALUES (?, 'background', 'image/webp', 1024, 'webp', ?)`, fileID, digest[:]).Error)
	lifecycle := mediaasset.NewLifecycle(db, cdnDomain)
	asset, _, err := lifecycle.AllocatePublicAsset(t.Context(), mediaasset.Allocation{
		SourceFileID: &fileID, Kind: "image", Extension: "webp", MimeType: "image/webp",
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)
	_, err = lifecycle.CompletePublicAsset(t.Context(), &commonv1.AssetWriteResult{AssetId: asset.ID, FileSize: 1024, Sha256: digest[:]})
	require.NoError(t, err)
	return fileID
}

func count(t *testing.T, db *gorm.DB, table, where string, args ...any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Table(table).Where(where, args...).Count(&n).Error)
	return n
}

func storedTitle(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var title string
	require.NoError(t, db.Raw(`SELECT site_title FROM site_settings WHERE id = 1`).Scan(&title).Error)
	return title
}

func TestSettingsMutationRechecksAuthorityAfterWaitingOnTheSettingsLockIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	checker := authztest.AllowAll()
	service := newService(t, db, checker)
	before := storedTitle(t, db)

	holder := db.Begin()
	require.NoError(t, holder.Error)
	t.Cleanup(func() { holder.Rollback() })
	require.NoError(t, holder.Exec(`SELECT id FROM site_settings WHERE id = 1 FOR UPDATE`).Error)
	result := make(chan error, 1)
	go func() {
		_, err := service.SetSetting(adminContext(t), sitesettings.SetSettingCommand{Key: "site_title", Value: "Must not persist"})
		result <- err
	}()
	testutil.RequireLockWaiters(t, db, 1)
	checker.Deny(policyv1.SiteSetting.Edit("1"))
	require.NoError(t, holder.Commit().Error)

	err := <-result
	require.ErrorContains(t, err, "admin access required", "authority is decided after the lock, not before it")
	require.Equal(t, before, storedTitle(t, db))
}

func TestSettingsChangesPlanOneOGRunForOverlappingChangesIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	service := newService(t, db, authztest.AllowAll())
	ctx := adminContext(t)

	noImpact, err := service.SetSetting(ctx, sitesettings.SetSettingCommand{Key: "company_name", Value: "No OG impact"})
	require.NoError(t, err)
	require.Nil(t, noImpact.OGGenerationRunID)

	background := seedImageFile(t, db)
	update, err := service.SetManySettings(ctx, sitesettings.SetManySettingsCommand{Settings: []sitesettings.Setting{
		{Key: "site_title", Value: "Updated Site"},
		{Key: "site_og_background_file_id", Value: background},
		{Key: "primary_color", Value: "#123456"},
	}})
	require.NoError(t, err)
	require.NotNil(t, update.OGGenerationRunID)
	require.EqualValues(t, 1, count(t, db, "og_generation_run", "id = ?", *update.OGGenerationRunID))
	require.Positive(t, count(t, db, "og_generation", "run_id = ?", *update.OGGenerationRunID))
	require.EqualValues(t, 1, count(t, db, "public_asset_binding", "owner_type = 'site_settings' AND binding_key = 'og_background_site' AND source_file_id = ?", background))
}

func TestLegalOGBackgroundsPersistWithoutPublishedLegalContentIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	service := newService(t, db, authztest.AllowAll())
	ctx := adminContext(t)
	require.Zero(t, count(t, db, "privacy_history", "true"))
	require.Zero(t, count(t, db, "terms_history", "true"))

	for _, key := range []string{"privacy_og_background_file_id", "terms_og_background_file_id"} {
		fileID := seedImageFile(t, db)
		update, err := service.SetSetting(ctx, sitesettings.SetSettingCommand{Key: key, Value: fileID})
		require.NoError(t, err, key)
		require.Nil(t, update.OGGenerationRunID, "no legal route has an image to regenerate")
		stored, err := service.GetSetting(ctx, sitesettings.GetSettingQuery{Key: key})
		require.NoError(t, err)
		require.Equal(t, fileID, stored.Value)
	}
}

func TestAnOGPlanFailureRollsBackTheSettingAndItsRunIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	service := newService(t, db, authztest.AllowAll())
	// A published Work whose featured image has no ready asset fails the plan.
	fileID, documentID := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile, revision) VALUES (?::uuid, 'work', ?::uuid)`, documentID, uuid.NewString()).Error)
	require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, mime_type, file_size, extension)
		VALUES (?, 'unready', 'image/webp', 1024, 'webp')`, fileID).Error)
	require.NoError(t, db.Create(&model.Work{
		ID: uuid.NewString(), ContentDocumentID: &documentID, Type: "WORK_TYPE_MUSIC_PROJECT", Year: 2026, Month: 7,
		IsPresent: true, Status: "WORK_STATUS_PUBLISHED", FeaturedImageFileID: &fileID,
	}).Error)
	before := storedTitle(t, db)
	runs := count(t, db, "og_generation_run", "true")

	_, err := service.SetSetting(adminContext(t), sitesettings.SetSettingCommand{Key: "site_title", Value: structured.Value("Must Roll Back")})

	require.Error(t, err)
	require.Equal(t, before, storedTitle(t, db))
	require.Equal(t, runs, count(t, db, "og_generation_run", "true"))
}
