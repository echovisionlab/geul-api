//go:build integration

package release_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	releasepkg "github.com/echovisionlab/geul-api/internal/release"
	sharelinkdomain "github.com/echovisionlab/geul-api/internal/sharelink"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

type recordingReleaseDeleteFileDeleter struct {
	deletedIDs   []string
	cleanupCalls int
}

func (d *recordingReleaseDeleteFileDeleter) DeleteFileByID(_ context.Context, fileID string) error {
	d.deletedIDs = append(d.deletedIDs, fileID)
	return nil
}

func (d *recordingReleaseDeleteFileDeleter) CleanupTrackUploadSessions(context.Context, string, string) error {
	d.cleanupCalls++
	return nil
}

func (d *recordingReleaseDeleteFileDeleter) RequireNoTrackUploadSessionsWithDB(context.Context, *gorm.DB, string) error {
	return nil
}

func TestDeleteReleaseRejectsOversizedAtomicAuthorizationBatchBeforeMutationIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Release delete batch boundary admin")
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID: auth.IdentityID(adminID), MemberID: auth.MemberID(integrationMemberID(adminID)),
		SessionID: auth.SessionID(integrationTestUUID()), Authenticated: true,
	})
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, adminID, policyv1.Role.Admin())
	files := &recordingReleaseDeleteFileDeleter{}
	service := releasepkg.NewReleaseService(
		db,
		spiceDB,
		testutil.SetupOryStack(t).KratosClient,
		releaseadapter.NewTrackFiles(files),
		releaseadapter.NewAssets("cdn.example.com"),
		releaseadapter.NewOG("cdn.example.com"),
		releaseadapter.NewWaveformJobs(db, releaseIntegrationTranscoderPublisher{}),
		noopReleaseDeleteAsyncPublisher{},
		releasepkg.WithReleaseContentBlockStore(newPageIntegrationContentBlockStore(t, spiceDB)),
	)

	release, err := service.CreateRelease(ctx, connect.NewRequest(&managev1.CreateReleaseRequest{
		Title:    "Oversized authorization batch",
		Type:     managev1.ReleaseType_RELEASE_TYPE_ALBUM,
		Document: creativeContentIntegrationDocument("en", "Oversized authorization batch"),
	}))
	require.NoError(t, err)

	tracks := make([]model.Track, maxReleaseCascadeAuthorizationTracks+1)
	for index := range tracks {
		tracks[index] = model.Track{
			ID:          integrationTestUUID(),
			ReleaseID:   release.Msg.Id,
			TrackNumber: index + 1,
			Title:       "Boundary track",
		}
	}
	require.NoError(t, db.CreateInBatches(&tracks, 100).Error)

	response, err := service.DeleteRelease(ctx, connect.NewRequest(&managev1.DeleteReleaseRequest{Id: release.Msg.Id}))
	require.Nil(t, response)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Contains(t, err.Error(), "at most 999")
	require.Zero(t, files.cleanupCalls, "oversized batch must fail before upload cleanup or product mutation")

	var releaseCount, trackCount int64
	require.NoError(t, db.Model(&model.Release{}).Where("id = ?", release.Msg.Id).Count(&releaseCount).Error)
	require.NoError(t, db.Model(&model.Track{}).Where("release_id = ?", release.Msg.Id).Count(&trackCount).Error)
	require.EqualValues(t, 1, releaseCount)
	require.EqualValues(t, maxReleaseCascadeAuthorizationTracks+1, trackCount)
	requireSynchronousAuthorizedResource(t, spiceDB, policyv1.Release.LookupManage(), release.Msg.Id, adminID, true)
}

type noopReleaseDeleteAsyncPublisher struct{}

func (noopReleaseDeleteAsyncPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}

func (noopReleaseDeleteAsyncPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}

func (noopReleaseDeleteAsyncPublisher) EnqueueProtobufWithExecutor(
	_ context.Context,
	executor eventpkg.DBTX,
	_ string,
	_ string,
	_ proto.Message,
) error {
	if executor == nil {
		return errNilTransactionalExecutor
	}
	return nil
}

func TestReleaseServiceDeleteReleasePreservesLibraryFilesIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Release Delete Admin")
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(adminID),
		MemberID:      auth.MemberID(integrationMemberID(adminID)),
		SessionID:     auth.SessionID(integrationTestUUID()),
		Authenticated: true,
	})

	fileDeleter := &recordingReleaseDeleteFileDeleter{}
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, adminID, policyv1.Role.Admin())
	releaseSvc := releasepkg.NewReleaseService(
		db,
		spiceDB,
		testutil.SetupOryStack(t).KratosClient,
		releaseadapter.NewTrackFiles(fileDeleter),
		releaseadapter.NewAssets("cdn.example.com"),
		releaseadapter.NewOG("cdn.example.com"),
		releaseadapter.NewWaveformJobs(db, releaseIntegrationTranscoderPublisher{}),
		noopReleaseDeleteAsyncPublisher{},
		releasepkg.WithReleaseContentBlockStore(newPageIntegrationContentBlockStore(t, spiceDB)),
	)

	releaseResp, err := releaseSvc.CreateRelease(ctx, connect.NewRequest(&managev1.CreateReleaseRequest{
		Title:    "Release Delete Integration",
		Type:     managev1.ReleaseType_RELEASE_TYPE_ALBUM,
		Document: creativeContentIntegrationDocument("en", "Release Delete Integration"),
	}))
	require.NoError(t, err)
	releaseID := releaseResp.Msg.Id

	ogAsset := seedHardCutReadyPublicAsset(t, db, "og", "webp", "image/webp", nil)
	require.NoError(t, db.Model(&model.Release{}).
		Where("id = ?", releaseID).
		Update("og_asset_id", ogAsset.GetAssetId()).Error)
	require.NoError(t, mediaasset.NewLifecycle(db, "cdn.example.com").BindPublicAsset(t.Context(), mediaasset.Binding{
		AssetID: ogAsset.GetAssetId(), OwnerType: "release", OwnerID: releaseID, BindingKey: "og",
	}))

	artworkFileID := integrationTestUUID()
	seedFileDeleteLifecycleFile(t, db, artworkFileID, "artwork", "image/webp", "webp")
	artworkAsset := seedHardCutReadyPublicAsset(t, db, "artwork", "webp", "image/webp", &artworkFileID)
	artworkResp, err := releaseSvc.SetReleaseArtwork(ctx, connect.NewRequest(&managev1.SetReleaseArtworkRequest{
		ReleaseId: releaseID,
		FileId:    artworkFileID,
	}))
	require.NoError(t, err)
	require.Contains(t, artworkResp.Msg.GetArtworkAsset().GetUrl(), "/asset/")
	require.Empty(t, artworkResp.Msg.GetOgGenerationRunId())
	requireHardCutPublicAssetStatus(t, db, ogAsset.GetAssetId(), model.PublicAssetStatusDeletePending)

	managedRelease, err := releaseSvc.GetRelease(ctx, connect.NewRequest(&managev1.GetReleaseRequest{Id: releaseID}))
	require.NoError(t, err)
	require.Equal(t, artworkAsset.GetAssetId(), managedRelease.Msg.GetArtworkAsset().GetAssetId())
	require.Equal(t, artworkAsset.GetAssetId(), managedRelease.Msg.GetOgAsset().GetAssetId())

	trackSvc := releasepkg.NewTrackService(db, releaseadapter.NewTrackTranscodes(releaseIntegrationTranscoderPublisher{}), releaseadapter.NewWaveformJobs(db, releaseIntegrationTranscoderPublisher{}), spiceDB, releaseadapter.NewTrackFiles(fileDeleter), releaseadapter.NewMemberSummaries(db, "cdn.example.com"), releaseadapter.NewArtistSummaries(db))
	trackResp, err := trackSvc.CreateTrack(ctx, connect.NewRequest(&managev1.CreateTrackRequest{
		ReleaseId: releaseID,
		Title:     "Delete Cleanup Track",
	}))
	require.NoError(t, err)

	trackOriginalFileID := integrationTestUUID()
	seedIntegrationFile(t, db, trackOriginalFileID, "original", "audio/wav", nil)
	require.NoError(t, db.Model(&model.Track{}).
		Where("id = ?", trackResp.Msg.Id).
		Updates(structured.Fields{
			"audio_original_file_id": trackOriginalFileID,
			"processing_status":      managev1.TrackProcessingStatus_TRACK_PROCESSING_STATUS_PROCESSING.String(),
		}).Error)

	shareLinkSvc := sharelinkdomain.NewService(db, sharelinkadapter.NewAuthority(db, spiceDB, nil))
	shareLabel := "Release delete integration share"
	shareLink, err := shareLinkSvc.CreateShareLink(ctx, connect.NewRequest(&managev1.CreateShareLinkRequest{
		EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_RELEASE,
		EntityId:   releaseID,
		Label:      &shareLabel,
	}))
	require.NoError(t, err)
	require.NotEmpty(t, shareLink.Msg.ShareLink.Id)

	deleted, err := releaseSvc.DeleteRelease(ctx, connect.NewRequest(&managev1.DeleteReleaseRequest{Id: releaseID}))
	require.NoError(t, err)
	require.True(t, deleted.Msg.Success)
	require.Empty(t, fileDeleter.deletedIDs)
	requireFileRowExists(t, db, artworkFileID)
	requireFileRowExists(t, db, trackOriginalFileID)
	requireHardCutPublicAssetStatus(t, db, ogAsset.GetAssetId(), model.PublicAssetStatusDeletePending)
	requireHardCutPublicAssetStatus(t, db, artworkAsset.GetAssetId(), model.PublicAssetStatusReady)
	requireNoRow(t, db, "release", releaseID)
	requireNoRow(t, db, "track", trackResp.Msg.Id)
	requireNoShareLinks(t, db, managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_RELEASE.String(), releaseID)
	requireNoReleaseTranslationRows(t, db, releaseID)
}

func TestReleaseServiceArtworkReplacementAndUnlinkPreserveFilesIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Release Artwork Admin")
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(adminID),
		MemberID:      auth.MemberID(integrationMemberID(adminID)),
		SessionID:     auth.SessionID(integrationTestUUID()),
		Authenticated: true,
	})

	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, adminID, policyv1.Role.Admin())
	releaseSvc := releasepkg.NewReleaseService(
		db,
		spiceDB,
		testutil.SetupOryStack(t).KratosClient,
		releaseadapter.NewTrackFiles(&recordingReleaseDeleteFileDeleter{}),
		releaseadapter.NewAssets("cdn.example.com"),
		releaseadapter.NewOG("cdn.example.com"),
		releaseadapter.NewWaveformJobs(db, releaseIntegrationTranscoderPublisher{}),
		noopReleaseDeleteAsyncPublisher{},
		releasepkg.WithReleaseContentBlockStore(newPageIntegrationContentBlockStore(t, spiceDB)),
	)
	releaseResp, err := releaseSvc.CreateRelease(ctx, connect.NewRequest(&managev1.CreateReleaseRequest{
		Title:    "Release Artwork Lifecycle",
		Type:     managev1.ReleaseType_RELEASE_TYPE_ALBUM,
		Document: creativeContentIntegrationDocument("en", "Release Artwork Lifecycle"),
	}))
	require.NoError(t, err)

	firstFileID := integrationTestUUID()
	seedFileDeleteLifecycleFile(t, db, firstFileID, "first", "image/webp", "webp")
	firstAsset := seedHardCutReadyPublicAsset(t, db, "artwork", "webp", "image/webp", &firstFileID)
	firstSet, err := releaseSvc.SetReleaseArtwork(ctx, connect.NewRequest(&managev1.SetReleaseArtworkRequest{
		ReleaseId: releaseResp.Msg.Id,
		FileId:    firstFileID,
	}))
	require.NoError(t, err)
	require.Empty(t, firstSet.Msg.GetOgGenerationRunId())

	secondFileID := integrationTestUUID()
	seedFileDeleteLifecycleFile(t, db, secondFileID, "second", "image/webp", "webp")
	secondAsset := seedHardCutReadyPublicAsset(t, db, "artwork", "webp", "image/webp", &secondFileID)
	secondSet, err := releaseSvc.SetReleaseArtwork(ctx, connect.NewRequest(&managev1.SetReleaseArtworkRequest{
		ReleaseId: releaseResp.Msg.Id,
		FileId:    secondFileID,
	}))
	require.NoError(t, err)
	require.Empty(t, secondSet.Msg.GetOgGenerationRunId())
	requireHardCutPublicAssetStatus(t, db, firstAsset.GetAssetId(), model.PublicAssetStatusReady)
	requireHardCutPublicAssetStatus(t, db, secondAsset.GetAssetId(), model.PublicAssetStatusReady)
	requireFileRowExists(t, db, firstFileID)
	requireFileRowExists(t, db, secondFileID)

	managedRelease, err := releaseSvc.GetRelease(ctx, connect.NewRequest(&managev1.GetReleaseRequest{Id: releaseResp.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, secondAsset.GetAssetId(), managedRelease.Msg.GetArtworkAsset().GetAssetId())
	require.Equal(t, secondAsset.GetAssetId(), managedRelease.Msg.GetOgAsset().GetAssetId())

	deleted, err := releaseSvc.DeleteReleaseArtwork(ctx, connect.NewRequest(&managev1.DeleteReleaseArtworkRequest{
		ReleaseId: releaseResp.Msg.Id,
	}))
	require.NoError(t, err)
	require.True(t, deleted.Msg.Success)
	require.Empty(t, deleted.Msg.GetOgGenerationRunId())
	requireHardCutPublicAssetStatus(t, db, secondAsset.GetAssetId(), model.PublicAssetStatusReady)
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
