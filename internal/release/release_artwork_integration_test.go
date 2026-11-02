//go:build integration

package release_test

import (
	"testing"

	"connectrpc.com/connect"
	optional "github.com/echovisionlab/geul-api/internal/optional"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/model"

	releasepkg "github.com/echovisionlab/geul-api/internal/release"

	releasepostgres "github.com/echovisionlab/geul-api/internal/release/postgres"
	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/echovisionlab/geul-api/internal/testutil/releasetest"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// The artwork lifecycle is the one Release path that crosses into the File
// and public asset lifecycles: the asset binding, the OG reuse of the
// artwork and the unlink run through the production media adapter against
// the File and public asset rows, which no single Store contract owns.

func TestReleaseServiceArtworkReplacementAndUnlinkPreserveFilesIntegration(t *testing.T) {
	t.Parallel()
	db := orytest.NewIntegrationDB(t)
	adminID := orytest.IntegrationUUID()
	orytest.SeedPostIntegrationIdentity(t, db, adminID, "Release Artwork Admin")
	ctx := releaseIntegrationAdminCtx(adminID)

	spiceDB := integrationSpiceDB(t)
	orytest.GrantIntegrationGlobalRole(t, spiceDB, adminID, policyv1.Role.Admin())
	releaseSvc := releasepkg.NewConnectHandler(newReleaseArtworkUseCases(t, db, spiceDB))
	releaseResp, err := releaseSvc.CreateRelease(ctx, connect.NewRequest(&managev1.CreateReleaseRequest{
		Title:    "Release Artwork Lifecycle",
		Type:     managev1.ReleaseType_RELEASE_TYPE_ALBUM,
		Document: creativeContentIntegrationDocument("en", "Release Artwork Lifecycle"),
	}))
	require.NoError(t, err)

	firstFileID := orytest.IntegrationUUID()
	seedFileDeleteLifecycleFile(t, db, firstFileID, "first", "image/webp", "webp")
	firstAsset := seedHardCutReadyPublicAsset(t, db, "artwork", "webp", "image/webp", &firstFileID)
	firstSet, err := releaseSvc.SetReleaseArtwork(ctx, connect.NewRequest(&managev1.SetReleaseArtworkRequest{
		ReleaseId: releaseResp.Msg.Id,
		FileId:    firstFileID,
	}))
	require.NoError(t, err)
	require.Empty(t, firstSet.Msg.GetOgGenerationRunId())

	secondFileID := orytest.IntegrationUUID()
	seedFileDeleteLifecycleFile(t, db, secondFileID, "second", "image/webp", "webp")
	secondAsset := seedHardCutReadyPublicAsset(t, db, "artwork", "webp", "image/webp", &secondFileID)
	secondSet, err := releaseSvc.SetReleaseArtwork(ctx, connect.NewRequest(&managev1.SetReleaseArtworkRequest{
		ReleaseId: releaseResp.Msg.Id,
		FileId:    secondFileID,
	}))
	require.NoError(t, err)
	require.Empty(t, secondSet.Msg.GetOgGenerationRunId())
	requireHardCutPublicAssetStatus(t, db, optional.Value(firstAsset).AssetID, model.PublicAssetStatusReady)
	requireHardCutPublicAssetStatus(t, db, optional.Value(secondAsset).AssetID, model.PublicAssetStatusReady)
	requireFileRowExists(t, db, firstFileID)
	requireFileRowExists(t, db, secondFileID)

	managedRelease, err := releaseSvc.GetRelease(ctx, connect.NewRequest(&managev1.GetReleaseRequest{Id: releaseResp.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, optional.Value(secondAsset).AssetID, managedRelease.Msg.GetArtworkAsset().GetAssetId())
	require.Equal(t, optional.Value(secondAsset).AssetID, managedRelease.Msg.GetOgAsset().GetAssetId())

	deleted, err := releaseSvc.DeleteReleaseArtwork(ctx, connect.NewRequest(&managev1.DeleteReleaseArtworkRequest{
		ReleaseId: releaseResp.Msg.Id,
	}))
	require.NoError(t, err)
	require.True(t, deleted.Msg.Success)
	require.Empty(t, deleted.Msg.GetOgGenerationRunId())
	requireHardCutPublicAssetStatus(t, db, optional.Value(secondAsset).AssetID, model.PublicAssetStatusReady)
	requireFileRowExists(t, db, firstFileID)
	requireFileRowExists(t, db, secondFileID)
	var artworkBindingCount int64
	require.NoError(t, db.Model(&model.PublicAssetBinding{}).
		Where("owner_type = ? AND owner_id = ? AND binding_key = ?", "release", releaseResp.Msg.Id, "artwork").
		Count(&artworkBindingCount).Error)
	require.Zero(t, artworkBindingCount)

	managedRelease, err = releaseSvc.GetRelease(ctx, connect.NewRequest(&managev1.GetReleaseRequest{Id: releaseResp.Msg.Id}))
	require.NoError(t, err)
	require.Nil(t, managedRelease.Msg.ArtworkAsset)
	require.Nil(t, managedRelease.Msg.OgAsset)
}

// newReleaseArtworkUseCases returns the Release use cases with the CDN the
// artwork tests read assets from, and in-memory audit.
func newReleaseArtworkUseCases(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient) *releasepkg.Service {
	t.Helper()
	return releasetest.NewService(t, releasetest.Deps{
		DB: db, SpiceDB: spiceDB, Audit: &audittest.Recorder{},
		Ports: releasepostgres.Ports{
			ContentBlocks: newPageIntegrationContentBlockStore(t, spiceDB),
			Assets:        releaseadapter.NewAssets("cdn.example.com"), OG: releaseadapter.NewOG("cdn.example.com"),
		},
		Members: releaseadapter.NewMemberSummaries(db, "cdn.example.com"),
	})
}
