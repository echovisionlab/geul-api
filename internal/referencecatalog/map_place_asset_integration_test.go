//go:build integration

package referencecatalog_test

import (
	"context"
	"crypto/sha256"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestMapPlaceImageAssetLifecycleIntegration(t *testing.T) {
	stack := testutil.PrepareOryIntegrationTest(t)
	db := stack.DB
	user := stack.CreateUser(t, policyv1.Role.Author().ID())
	spiceDB := stack.SpiceDBClient
	ctx := auth.WithUser(context.Background(), user.AuthUserInfo())
	service := mapPlaceServiceForTest(t, db, "https://cdn.example.com", spiceDB)

	firstFileID := testutil.IntegrationUUID()
	secondFileID := testutil.IntegrationUUID()
	thirdFileID := testutil.IntegrationUUID()
	firstAsset := seedMapImageSourceFixture(t, db, firstFileID)
	secondAsset := seedMapImageSourceFixture(t, db, secondFileID)
	thirdAsset := seedMapImageSourceFixture(t, db, thirdFileID)

	created, err := service.CreateMapPlace(ctx, connect.NewRequest(&managev1.CreateMapPlaceRequest{
		Name: "Asset map place", Address: "1 Asset Road", Lat: 37.5, Lng: 127.0, ImageFileId: &firstFileID,
	}))
	require.NoError(t, err)
	require.Equal(t, firstAsset.GetUrl(), created.Msg.GetImageAsset().GetUrl())
	requireMapImageBinding(t, db, created.Msg.Id, firstAsset.GetAssetId())

	updated, err := service.UpdateMapPlace(ctx, connect.NewRequest(&managev1.UpdateMapPlaceRequest{
		Id: created.Msg.Id, ImageFileId: &secondFileID,
	}))
	require.NoError(t, err)
	require.Equal(t, secondAsset.GetUrl(), updated.Msg.GetImageAsset().GetUrl())
	requireMapImageBinding(t, db, created.Msg.Id, secondAsset.GetAssetId())
	requireMapPlaceAssetStatus(t, db, firstAsset.GetAssetId(), model.PublicAssetStatusReady)

	cleared, err := service.UpdateMapPlace(ctx, connect.NewRequest(&managev1.UpdateMapPlaceRequest{
		Id: created.Msg.Id, ClearImage: true,
	}))
	require.NoError(t, err)
	require.Empty(t, cleared.Msg.GetImageAsset().GetUrl())
	requireNoMapImageBinding(t, db, created.Msg.Id)
	requireMapPlaceAssetStatus(t, db, secondAsset.GetAssetId(), model.PublicAssetStatusReady)

	_, err = service.UpdateMapPlace(ctx, connect.NewRequest(&managev1.UpdateMapPlaceRequest{
		Id: created.Msg.Id, ImageFileId: &thirdFileID,
	}))
	require.NoError(t, err)
	requireMapImageBinding(t, db, created.Msg.Id, thirdAsset.GetAssetId())
	testutil.GrantIntegrationGlobalRole(t, spiceDB, user.IdentityID, policyv1.Role.Admin())
	deleted, err := service.DeleteMapPlace(ctx, connect.NewRequest(&managev1.DeleteMapPlaceRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.True(t, deleted.Msg.GetSuccess())
	requireNoMapImageBinding(t, db, created.Msg.Id)
	requireMapPlaceAssetStatus(t, db, thirdAsset.GetAssetId(), model.PublicAssetStatusReady)
}

func TestReferenceCatalogMembersExcludeUnresolvedAndInactiveIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	memberID := testutil.IntegrationUUID()
	members := referencecatalogadapter.NewMemberSummaries("https://cdn.example.com")

	resolved, err := members.Resolve(t.Context(), db, []string{memberID})
	require.NoError(t, err)
	require.NotContains(t, resolved, memberID)

	require.NoError(t, db.Create(&model.Member{
		ID: memberID, Nickname: memberID, Onboarded: false, SocialLinks: map[string]string{},
	}).Error)
	resolved, err = members.Resolve(t.Context(), db, []string{memberID})
	require.NoError(t, err)
	require.NotContains(t, resolved, memberID)
}

func seedMapImageSourceFixture(t *testing.T, db *gorm.DB, fileID string) *commonv1.AssetRef {
	t.Helper()
	digest := sha256.Sum256([]byte(fileID))
	require.NoError(t, db.Create(&model.File{
		ID: fileID, FileName: fileID, MimeType: "image/webp", FileSize: 1024,
		Extension: "webp", SHA256: digest[:],
	}).Error)
	return seedMapPlaceReadyAsset(t, db, "map_image", "webp", "image/webp", &fileID)
}

func requireMapImageBinding(t *testing.T, db *gorm.DB, placeID, assetID string) {
	t.Helper()
	var binding model.PublicAssetBinding
	require.NoError(t, db.First(&binding,
		"owner_type = ? AND owner_id = ? AND binding_key = ?", "map_place", placeID, "image").Error)
	require.Equal(t, assetID, binding.AssetID)
}

func requireNoMapImageBinding(t *testing.T, db *gorm.DB, placeID string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&model.PublicAssetBinding{}).
		Where("owner_type = ? AND owner_id = ? AND binding_key = ?", "map_place", placeID, "image").
		Count(&count).Error)
	require.Zero(t, count)
}
