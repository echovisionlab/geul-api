//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
	"gorm.io/gorm"

	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func newSiteSettingIntegrationService(t *testing.T, db *gorm.DB) (*sitesettings.SiteSettingService, context.Context) {
	t.Helper()
	ctx, spiceDB := integrationAdminCtxWithIdentityAndSpiceDB(t, db)
	return sitesettings.NewSiteSettingService(
		db,
		"https://www.example.test",
		sitesettingsadapter.NewAssets("https://cdn.example.com"),
		sitesettingsadapter.NewReferences(),
		newSiteSettingsOGInvalidatorForTest(db, "https://cdn.example.com"),
		spiceDB,
	), ctx
}

func TestSiteSettingServiceThemeLogoSettingsIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	service, ctx := newSiteSettingIntegrationService(t, db)

	lightFileID, lightURL := seedSiteSettingReadyFile(t, db, "light-logo", "logo", "png", "image/png", 1024)
	darkFileID, darkURL := seedSiteSettingReadyFile(t, db, "dark-logo", "logo", "png", "image/png", 1024)

	lightSet, err := service.SetSetting(ctx, connect.NewRequest(&managev1.SetSettingRequest{
		Key:   "logo_light_file_id",
		Value: structpb.NewStringValue(lightFileID),
	}))
	require.NoError(t, err)
	require.True(t, lightSet.Msg.Success)

	darkSet, err := service.SetSetting(ctx, connect.NewRequest(&managev1.SetSettingRequest{
		Key:   "logo_dark_file_id",
		Value: structpb.NewStringValue(darkFileID),
	}))
	require.NoError(t, err)
	require.True(t, darkSet.Msg.Success)

	lightSetting, err := service.GetSetting(ctx, connect.NewRequest(&managev1.GetSettingRequest{
		Key: "logo_light_file_id",
	}))
	require.NoError(t, err)
	require.Equal(t, lightFileID, lightSetting.Msg.GetSetting().GetValue().GetStringValue())

	darkSetting, err := service.GetSetting(ctx, connect.NewRequest(&managev1.GetSettingRequest{
		Key: "logo_dark_file_id",
	}))
	require.NoError(t, err)
	require.Equal(t, darkFileID, darkSetting.Msg.GetSetting().GetValue().GetStringValue())

	settings, err := service.GetSettings(ctx, connect.NewRequest(&managev1.GetSettingsRequest{}))
	require.NoError(t, err)
	require.Equal(t, lightURL, settings.Msg.Settings.Public.GetLogoLightAsset().GetUrl())
	require.Equal(t, darkURL, settings.Msg.Settings.Public.GetLogoDarkAsset().GetUrl())
	require.Equal(t, "https://www.example.test", settings.Msg.Settings.GetRuntime().GetSiteOrigin())
}

func TestSiteSettingLoaderAssetsUseOrderedRelationOnlyIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	service, ctx := newSiteSettingIntegrationService(t, db)
	fileID := uuid.NewString()
	digest := sha256.Sum256([]byte(fileID))
	require.NoError(t, db.Exec(
		`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256)
		 VALUES (?, ?, 'image/webp', 1024, 'webp', ?)`,
		fileID,
		"loader-"+fileID,
		digest[:],
	).Error)
	seedHardCutReadyPublicAsset(t, db, "loader", "webp", "image/webp", &fileID)

	added, err := service.AddSiteLoaderAsset(ctx, connect.NewRequest(&managev1.AddSiteLoaderAssetRequest{FileId: fileID}))
	require.NoError(t, err)
	require.True(t, added.Msg.Success)

	settings, err := service.GetSettings(ctx, connect.NewRequest(&managev1.GetSettingsRequest{}))
	require.NoError(t, err)
	require.Len(t, settings.Msg.Settings.Public.GetLoaderAssets(), 1)
	require.Equal(t, fileID, settings.Msg.Settings.Public.GetLoaderAssets()[0].GetFileId())

	var relationCount int64
	require.NoError(t, db.Table("site_setting_loader_file").Where("site_setting_id = ? AND file_id = ?", 1, fileID).Count(&relationCount).Error)
	require.Equal(t, int64(1), relationCount)

	removed, err := service.RemoveSiteLoaderAsset(ctx, connect.NewRequest(&managev1.RemoveSiteLoaderAssetRequest{FileId: fileID}))
	require.NoError(t, err)
	require.True(t, removed.Msg.Success)
	require.NoError(t, db.Table("site_setting_loader_file").Where("site_setting_id = ? AND file_id = ?", 1, fileID).Count(&relationCount).Error)
	require.Zero(t, relationCount)

	settings, err = service.GetSettings(ctx, connect.NewRequest(&managev1.GetSettingsRequest{}))
	require.NoError(t, err)
	require.Empty(t, settings.Msg.Settings.Public.GetLoaderAssets())
}

func TestSiteSettingAssetReferencesEnforceSlotFormatIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	service, ctx := newSiteSettingIntegrationService(t, db)

	webpLogoFileID, _ := seedSiteSettingReadyFile(t, db, "webp-logo", "logo", "webp", "image/webp", 1024)
	_, err := service.SetSetting(ctx, connect.NewRequest(&managev1.SetSettingRequest{
		Key:   "logo_light_file_id",
		Value: structpb.NewStringValue(webpLogoFileID),
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = service.SetManySettings(ctx, connect.NewRequest(&managev1.SetManySettingsRequest{
		Settings: []*managev1.SiteSetting{
			{Key: "site_title", Value: structpb.NewStringValue("must roll back")},
			{Key: "logo_light_file_id", Value: structpb.NewStringValue(webpLogoFileID)},
		},
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	var settings model.SiteSettings
	require.NoError(t, db.First(&settings, "id = 1").Error)
	require.NotEqual(t, "must roll back", settings.SiteTitle)

	svgEmailLogoFileID, _ := seedSiteSettingReadyFile(t, db, "svg-email-logo", "email_image", "svg", "image/svg+xml", 1024)
	_, err = service.SetSetting(ctx, connect.NewRequest(&managev1.SetSettingRequest{
		Key:   "logo_email_file_id",
		Value: structpb.NewStringValue(svgEmailLogoFileID),
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	oversizedLoaderFileID, _ := seedSiteSettingReadyFile(t, db, "oversized-loader", "loader", "webp", "image/webp", 100*1024+1)
	_, err = service.AddSiteLoaderAsset(ctx, connect.NewRequest(&managev1.AddSiteLoaderAssetRequest{FileId: oversizedLoaderFileID}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestSiteSettingHomepageRequiresPublishedPageIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	service, ctx := newSiteSettingIntegrationService(t, db)

	draftPageID := uuid.NewString()
	draftDocumentID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile, revision) VALUES (?::uuid, 'page', ?::uuid)`,
		draftDocumentID,
		uuid.NewString(),
	).Error)
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.Page{
		ID:                draftPageID,
		ContentDocumentID: &draftDocumentID,
		DocumentLayout:    model.DefaultDocumentLayout(),
		Status:            model.PageStatus(managev1.PageStatus_PAGE_STATUS_DRAFT.String()),
		CreatedAt:         now,
		UpdatedAt:         now,
	}).Error)
	_, err := service.SetManySettings(ctx, connect.NewRequest(&managev1.SetManySettingsRequest{
		Settings: []*managev1.SiteSetting{
			{Key: "site_title", Value: structpb.NewStringValue("must roll back")},
			{Key: "homepage_page_id", Value: structpb.NewStringValue(draftPageID)},
		},
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	var settings model.SiteSettings
	require.NoError(t, db.First(&settings, "id = 1").Error)
	require.NotEqual(t, "must roll back", settings.SiteTitle)
	require.Nil(t, settings.HomepagePageID)

	require.NoError(t, db.Model(&model.Page{}).
		Where("id = ?", draftPageID).
		Update("status", managev1.PageStatus_PAGE_STATUS_PUBLISHED.String()).Error)
	_, err = service.SetSetting(ctx, connect.NewRequest(&managev1.SetSettingRequest{
		Key:   "homepage_page_id",
		Value: structpb.NewStringValue(draftPageID),
	}))
	require.NoError(t, err)
}

func TestSiteSettingOgInvalidationReturnsOneRunForOverlappingChangesIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	service, ctx := newSiteSettingIntegrationService(t, db)

	noRun, err := service.SetSetting(ctx, connect.NewRequest(&managev1.SetSettingRequest{
		Key: "company_name", Value: structpb.NewStringValue("No OG impact"),
	}))
	require.NoError(t, err)
	require.Nil(t, noRun.Msg.OgGenerationRunId)

	backgroundFileID := seedImageBindingUploadedFileFixture(t, db, "site/og/background.webp")
	response, err := service.SetManySettings(ctx, connect.NewRequest(&managev1.SetManySettingsRequest{
		Settings: []*managev1.SiteSetting{
			{Key: "site_title", Value: structpb.NewStringValue("Updated Site")},
			{Key: "site_og_background_file_id", Value: structpb.NewStringValue(backgroundFileID)},
			{Key: "primary_color", Value: structpb.NewStringValue("#123456")},
		},
	}))
	require.NoError(t, err)
	require.True(t, response.Msg.Success)
	require.NotNil(t, response.Msg.OgGenerationRunId)
	require.NotEmpty(t, response.Msg.GetOgGenerationRunId())

	var runCount int64
	require.NoError(t, db.Model(&model.OgGenerationRun{}).
		Where("id = ?", response.Msg.GetOgGenerationRunId()).
		Count(&runCount).Error)
	require.Equal(t, int64(1), runCount)
	var generationCount int64
	require.NoError(t, db.Model(&model.OgGeneration{}).
		Where("run_id = ?", response.Msg.GetOgGenerationRunId()).
		Count(&generationCount).Error)
	require.Positive(t, generationCount)
}

func TestSiteSettingLegalOgBackgroundPersistsWithoutPublishedLegalContentIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	service, ctx := newSiteSettingIntegrationService(t, db)

	for _, entity := range []struct {
		name  string
		table string
		key   string
	}{
		{name: "privacy", table: "privacy_history", key: "privacy_og_background_file_id"},
		{name: "terms", table: "terms_history", key: "terms_og_background_file_id"},
	} {
		t.Run(entity.name, func(t *testing.T) {
			var legalContentCount int64
			require.NoError(t, db.Table(entity.table).Count(&legalContentCount).Error)
			require.Zero(t, legalContentCount)

			fileID := seedImageBindingUploadedFileFixture(t, db, "site/og/"+entity.name+"-background.webp")
			response, err := service.SetSetting(ctx, connect.NewRequest(&managev1.SetSettingRequest{
				Key:   entity.key,
				Value: structpb.NewStringValue(fileID),
			}))
			require.NoError(t, err)
			require.True(t, response.Msg.Success)
			require.Nil(t, response.Msg.OgGenerationRunId)

			stored, err := service.GetSetting(ctx, connect.NewRequest(&managev1.GetSettingRequest{
				Key: entity.key,
			}))
			require.NoError(t, err)
			require.Equal(t, fileID, stored.Msg.GetSetting().GetValue().GetStringValue())
		})
	}
}

func TestSiteSettingOgPlannerFailureRollsBackSettingAndRunIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	service, ctx := newSiteSettingIntegrationService(t, db)
	missingAssetFileID := uuid.NewString()
	workDocumentID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile, revision) VALUES (?::uuid, 'work', ?::uuid)`,
		workDocumentID,
		uuid.NewString(),
	).Error)
	digest := sha256.Sum256([]byte("missing-ready-public-asset"))
	require.NoError(t, db.Exec(
		`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256)
		 VALUES (?, ?, 'image/webp', 1024, 'webp', ?)`,
		missingAssetFileID,
		"missing-ready-public-asset",
		digest[:],
	).Error)
	work := model.Work{
		ID: uuid.NewString(), ContentDocumentID: &workDocumentID,
		Type: "WORK_TYPE_MUSIC_PROJECT", Year: 2026, Month: 7,
		IsPresent: true, Status: "WORK_STATUS_PUBLISHED", FeaturedImageFileID: &missingAssetFileID,
	}
	require.NoError(t, db.Create(&work).Error)

	var before model.SiteSettings
	require.NoError(t, db.First(&before, "id = 1").Error)
	var beforeRuns int64
	require.NoError(t, db.Model(&model.OgGenerationRun{}).Count(&beforeRuns).Error)
	_, err := service.SetSetting(ctx, connect.NewRequest(&managev1.SetSettingRequest{
		Key: "site_title", Value: structpb.NewStringValue("Must Roll Back"),
	}))
	require.Error(t, err)

	var after model.SiteSettings
	require.NoError(t, db.First(&after, "id = 1").Error)
	require.Equal(t, before.SiteTitle, after.SiteTitle)
	var afterRuns int64
	require.NoError(t, db.Model(&model.OgGenerationRun{}).Count(&afterRuns).Error)
	require.Equal(t, beforeRuns, afterRuns)
}

func seedSiteSettingReadyFile(
	t *testing.T,
	db *gorm.DB,
	key string,
	kind string,
	extension string,
	mimeType string,
	fileSize int64,
) (string, string) {
	t.Helper()

	fileID := uuid.NewString()
	digest := sha256.Sum256([]byte(key))
	require.NoError(t, db.Exec(
		`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		fileID,
		key,
		mimeType,
		fileSize,
		extension,
		digest[:],
	).Error)
	asset := seedHardCutReadyPublicAsset(t, db, kind, extension, mimeType, &fileID)
	return fileID, asset.GetUrl()
}
