//go:build integration

package referencecatalog_test

import (
	"crypto/sha256"
	"testing"

	"connectrpc.com/connect"
	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestClientThemeLogoSlotsDoNotDeleteSharedFilesIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
	service := referencecatalog.NewClientService(db, referencecatalogadapter.NewAssets("https://cdn.example.com"), spiceDB)

	created, err := service.CreateClient(ctx, connect.NewRequest(&managev1.CreateClientRequest{
		Name: "Client Shared Logo " + testutil.IntegrationUUID(),
	}))
	require.NoError(t, err)

	sharedFileID := seedClientLogoFile(t, db)
	sharedURL := readyClientLogoURL(t, db, sharedFileID)

	lightLogo, err := service.SetClientLogo(ctx, connect.NewRequest(&managev1.SetClientLogoRequest{
		ClientId: created.Msg.Id,
		FileId:   sharedFileID,
		Variant:  managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_LIGHT,
	}))
	require.NoError(t, err)
	require.Equal(t, sharedURL, lightLogo.Msg.GetLogoLightAsset().GetUrl())
	require.Equal(t, sharedFileID, requireClientLogoFileID(t, db, created.Msg.Id, "logo_light_file_id"))

	darkLogo, err := service.SetClientLogo(ctx, connect.NewRequest(&managev1.SetClientLogoRequest{
		ClientId: created.Msg.Id,
		FileId:   sharedFileID,
		Variant:  managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_DARK,
	}))
	require.NoError(t, err)
	require.Equal(t, sharedURL, darkLogo.Msg.GetLogoDarkAsset().GetUrl())
	require.Equal(t, sharedFileID, requireClientLogoFileID(t, db, created.Msg.Id, "logo_dark_file_id"))

	fetched, err := service.GetClient(ctx, connect.NewRequest(&managev1.GetClientRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, sharedURL, fetched.Msg.GetLogoLightAsset().GetUrl())
	require.Equal(t, sharedURL, fetched.Msg.GetLogoDarkAsset().GetUrl())

	deletedDark, err := service.DeleteClientLogo(ctx, connect.NewRequest(&managev1.DeleteClientLogoRequest{
		ClientId: created.Msg.Id,
		Variant:  managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_DARK,
	}))
	require.NoError(t, err)
	require.True(t, deletedDark.Msg.Success)
	requireClientLogoSlotEmpty(t, db, created.Msg.Id, "logo_dark_file_id")
	require.Equal(t, sharedFileID, requireClientLogoFileID(t, db, created.Msg.Id, "logo_light_file_id"))

	deletedLight, err := service.DeleteClientLogo(ctx, connect.NewRequest(&managev1.DeleteClientLogoRequest{
		ClientId: created.Msg.Id,
		Variant:  managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_LIGHT,
	}))
	require.NoError(t, err)
	require.True(t, deletedLight.Msg.Success)
	requireClientLogoSlotEmpty(t, db, created.Msg.Id, "logo_light_file_id")
	var retainedFileCount int64
	require.NoError(t, db.Table("file").Where("id = ?", sharedFileID).Count(&retainedFileCount).Error)
	require.EqualValues(t, 1, retainedFileCount)
}

func seedClientLogoFile(t *testing.T, db *gorm.DB) string {
	t.Helper()
	id := testutil.IntegrationUUID()
	digest := sha256.Sum256([]byte(id))
	require.NoError(t, db.Exec(
		`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256)
		 VALUES (?::uuid, 'shared-logo', 'image/webp', 1024, 'webp', ?)`,
		id, digest[:],
	).Error)
	lifecycle := mediaasset.NewLifecycle(db, "https://cdn.example.com")
	asset, _, err := lifecycle.AllocatePublicAsset(t.Context(), mediaasset.Allocation{
		SourceFileID: &id, Kind: "logo", Extension: "webp", MimeType: "image/webp",
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)
	_, err = lifecycle.CompletePublicAsset(t.Context(), &commonv1.AssetWriteResult{
		AssetId: asset.ID, FileSize: 1024, Sha256: digest[:],
	})
	require.NoError(t, err)
	return id
}

func readyClientLogoURL(t *testing.T, db *gorm.DB, fileID string) string {
	t.Helper()
	asset, err := mediaasset.ReadyPublicAssetRefForSourceFile(
		t.Context(), db, "https://cdn.example.com", fileID, "logo",
	)
	require.NoError(t, err)
	return asset.GetUrl()
}

func requireClientLogoFileID(t *testing.T, db *gorm.DB, clientID, column string) string {
	t.Helper()
	var fileID *string
	require.NoError(t, db.Table("client").Select(column).Where("id = ?", clientID).Scan(&fileID).Error)
	require.NotNil(t, fileID)
	return *fileID
}

func requireClientLogoSlotEmpty(t *testing.T, db *gorm.DB, clientID, column string) {
	t.Helper()
	var fileID *string
	require.NoError(t, db.Table("client").Select(column).Where("id = ?", clientID).Scan(&fileID).Error)
	require.Nil(t, fileID)
}
