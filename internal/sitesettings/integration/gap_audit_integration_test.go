//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/maptheme"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSiteSettingsLoaderAndDefaultThemeAuditCommitOnlyActualChangesIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID := uuid.NewString()
	memberID := seedExternalKratosIdentityWithTraits(t, db, identityID, "Site Settings gap audit admin")
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	ctx := siteSettingsGapAuditContext(t, identityID, memberID)
	writer := apitelemetry.NewDurableWriter(db)
	settings := sitesettings.NewAuditedSiteSettingService(
		db, "https://www.example.test", sitesettingsadapter.NewAssets("https://cdn.example.test"), sitesettingsadapter.NewReferences(),
		newSiteSettingsOGInvalidatorForTest(db, "https://cdn.example.test"), writer, spiceDB,
	)

	loaderFileID, _ := seedSiteSettingReadyFile(t, db, "audited-loader", "loader", "webp", "image/webp", 1024)
	_, err := settings.AddSiteLoaderAsset(ctx, connect.NewRequest(&managev1.AddSiteLoaderAssetRequest{FileId: loaderFileID}))
	require.NoError(t, err)
	_, err = settings.AddSiteLoaderAsset(ctx, connect.NewRequest(&managev1.AddSiteLoaderAssetRequest{FileId: loaderFileID}))
	require.NoError(t, err)

	var relationCount int64
	require.NoError(t, db.Table("site_setting_loader_file").
		Where("site_setting_id = ? AND file_id = ?", 1, loaderFileID).
		Count(&relationCount).Error)
	require.Equal(t, int64(1), relationCount)

	_, err = settings.RemoveSiteLoaderAsset(ctx, connect.NewRequest(&managev1.RemoveSiteLoaderAssetRequest{FileId: loaderFileID}))
	require.NoError(t, err)
	_, err = settings.RemoveSiteLoaderAsset(ctx, connect.NewRequest(&managev1.RemoveSiteLoaderAssetRequest{FileId: loaderFileID}))
	require.NoError(t, err)

	mapThemes := maptheme.NewMapThemeService(db, spiceDB)
	theme, err := mapThemes.CreateMapTheme(
		ctx,
		connect.NewRequest(serviceMapThemeCreateRequest("Audited default theme")),
	)
	require.NoError(t, err)
	auditedMapThemes := maptheme.NewAuditedMapThemeService(db, writer, spiceDB)
	_, err = auditedMapThemes.SetDefaultMapTheme(ctx, connect.NewRequest(&managev1.SetDefaultMapThemeRequest{ThemeId: theme.Msg.Id}))
	require.NoError(t, err)
	_, err = auditedMapThemes.SetDefaultMapTheme(ctx, connect.NewRequest(&managev1.SetDefaultMapThemeRequest{ThemeId: theme.Msg.Id}))
	require.NoError(t, err)

	var storedDefault string
	require.NoError(t, db.Raw(`SELECT default_map_theme_id::text FROM public.site_settings WHERE id = 1`).Scan(&storedDefault).Error)
	require.Equal(t, theme.Msg.Id, storedDefault)
	require.Equal(t, [][]string{
		{"loader_file_ids"},
		{"loader_file_ids"},
		{"default_map_theme_id"},
	}, siteSettingsGapAuditChangedFields(t, db))
}

func TestSiteSettingsLoaderAndDefaultThemeAuditFailureRollsBackIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID := uuid.NewString()
	memberID := seedExternalKratosIdentityWithTraits(t, db, identityID, "Site Settings gap rollback admin")
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	ctx := siteSettingsGapAuditContext(t, identityID, memberID)
	loaderFileID, _ := seedSiteSettingReadyFile(t, db, "rollback-loader", "loader", "webp", "image/webp", 1024)
	settings := sitesettings.NewAuditedSiteSettingService(
		db, "https://www.example.test", sitesettingsadapter.NewAssets("https://cdn.example.test"), sitesettingsadapter.NewReferences(),
		newSiteSettingsOGInvalidatorForTest(db, "https://cdn.example.test"), failingDomainAuditAppender{}, spiceDB,
	)

	_, err := settings.AddSiteLoaderAsset(ctx, connect.NewRequest(&managev1.AddSiteLoaderAssetRequest{FileId: loaderFileID}))
	require.Error(t, err)
	var relationCount int64
	require.NoError(t, db.Table("site_setting_loader_file").
		Where("site_setting_id = ? AND file_id = ?", 1, loaderFileID).
		Count(&relationCount).Error)
	require.Zero(t, relationCount)

	var beforeDefault string
	require.NoError(t, db.Raw(`SELECT default_map_theme_id::text FROM public.site_settings WHERE id = 1`).Scan(&beforeDefault).Error)
	theme, err := maptheme.NewMapThemeService(db, spiceDB).CreateMapTheme(
		ctx,
		connect.NewRequest(serviceMapThemeCreateRequest("Rollback default theme")),
	)
	require.NoError(t, err)
	_, err = maptheme.NewAuditedMapThemeService(db, failingDomainAuditAppender{}, spiceDB).
		SetDefaultMapTheme(ctx, connect.NewRequest(&managev1.SetDefaultMapThemeRequest{ThemeId: theme.Msg.Id}))
	require.Error(t, err)
	var afterDefault string
	require.NoError(t, db.Raw(`SELECT default_map_theme_id::text FROM public.site_settings WHERE id = 1`).Scan(&afterDefault).Error)
	require.Equal(t, beforeDefault, afterDefault)
}

func serviceMapThemeCreateRequest(name string) *managev1.CreateMapThemeRequest {
	variant := func(background string) *managev1.MapThemeVariantInput {
		return &managev1.MapThemeVariantInput{
			BackgroundColor: background, WaterColor: "#112233", LandColor: "#223344",
			RoadColor: "#334455", BuildingFillColor: "#445566", BuildingStrokeEnabled: true,
			BuildingStrokeColor: "#556677", CalloutLineColor: "#667788",
			CalloutTextColor: "#778899", CalloutBackgroundColor: "rgba(10,20,30,0.5)",
			CalloutDescriptionColor: "#8899aa", AttributionColor: "transparent",
			LabelTextColor: "#99aabb", ClusterColor: "#aabbcc", ClusterHoverColor: "#bbccdd",
			ClusterTextColor: "#ccddee", ClusterTextHoverColor: "#ddeeff",
			CalloutHoverLineColor: "#123", CalloutHoverTextColor: "#1234",
			CalloutHoverDescriptionColor: "#123456", CalloutHoverBackgroundColor: "#12345678",
		}
	}
	return &managev1.CreateMapThemeRequest{
		Name: name,
		Settings: &managev1.MapThemeSettings{
			CalloutScale: 1.25, CalloutOffsetX: 2, CalloutOffsetY: 3,
			CalloutFields: []string{"name", "address"}, AttributionFontSize: 12,
			ShowAreaLabels: true,
		},
		LightVariant: variant("#ffffff"),
		DarkVariant:  variant("#000000"),
	}
}

func siteSettingsGapAuditContext(t *testing.T, identityID, memberID string) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.118")
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), requestContext), &auth.UserInfo{
		SessionID: auth.SessionID(uuid.NewString()), IdentityID: auth.IdentityID(identityID),
		MemberID: auth.MemberID(memberID), Authenticated: true, Onboarded: true,
	})
}

func siteSettingsGapAuditChangedFields(t *testing.T, db *gorm.DB) [][]string {
	t.Helper()
	var rows []struct {
		Attributes []byte `gorm:"column:attributes"`
	}
	require.NoError(t, db.Raw(`
		SELECT attributes
		FROM public.domain_audit
		WHERE action = ? AND target_type = 'site_settings' AND target_id = '1'
		ORDER BY occurred_at ASC, audit_id ASC
	`, sharedtelemetry.AuditSiteSettingsUpdated).Scan(&rows).Error)
	fields := make([][]string, 0, len(rows))
	for _, row := range rows {
		var attributes struct {
			ChangedFields []string `json:"changed_fields"`
		}
		require.NoError(t, json.Unmarshal(row.Attributes, &attributes))
		fields = append(fields, attributes.ChangedFields)
	}
	return fields
}
