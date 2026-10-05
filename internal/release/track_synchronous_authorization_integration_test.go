//go:build integration

package release_test

import (
	"slices"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
	"github.com/echovisionlab/geul-api/internal/auth"
	releasepkg "github.com/echovisionlab/geul-api/internal/release"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestReleaseTrackSynchronousAuthorizationLifecycleIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	spiceDB := integrationSpiceDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Release authorization admin")
	grantIntegrationGlobalRole(t, spiceDB, adminID, policyv1.Role.Admin())
	ctx := withAuditedRequestContext(t, releaseIntegrationAdminCtx(adminID))
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(adminID))
	require.NoError(t, err)
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	require.NoError(t, err)
	releasesBeforeFailure, err := spiceDB.LookupResources(ctx, policyv1.Release.LookupManage(), actor)
	require.NoError(t, err)

	failing := releasepkg.NewAuditedReleaseService(
		db,
		spiceDB,
		testutil.SetupOryStack(t).KratosClient,
		releaseadapter.NewTrackFiles(&recordingArtistFileDeleter{}),
		releaseadapter.NewAssets(""),
		releaseadapter.NewOG(""),
		releaseadapter.NewWaveformJobs(db, releaseIntegrationTranscoderPublisher{}),
		noopAsyncPublisher{},
		releaseFailingAuditAppender{},
		releasepkg.WithReleaseContentBlockStore(newPageIntegrationContentBlockStore(t, spiceDB)),
	)
	_, err = failing.CreateRelease(ctx, connect.NewRequest(&managev1.CreateReleaseRequest{
		Title:    "authorization rollback",
		Type:     managev1.ReleaseType_RELEASE_TYPE_ALBUM,
		Document: creativeContentIntegrationDocument("en", "authorization rollback"),
	}))
	require.Error(t, err)
	releasesAfterFailure, err := spiceDB.LookupResources(ctx, policyv1.Release.LookupManage(), actor)
	require.NoError(t, err)
	require.ElementsMatch(t, releasesBeforeFailure, releasesAfterFailure, "known DB rollback must compensate the policy touch")

	writer := apitelemetry.NewDurableWriter(db)
	releases := releasepkg.NewAuditedReleaseService(
		db,
		spiceDB,
		testutil.SetupOryStack(t).KratosClient,
		releaseadapter.NewTrackFiles(&recordingArtistFileDeleter{}),
		releaseadapter.NewAssets(""),
		releaseadapter.NewOG(""),
		releaseadapter.NewWaveformJobs(db, releaseIntegrationTranscoderPublisher{}),
		noopAsyncPublisher{},
		writer,
		releasepkg.WithReleaseContentBlockStore(newPageIntegrationContentBlockStore(t, spiceDB)),
	)
	tracks := releasepkg.NewAuditedTrackService(
		db,
		releaseadapter.NewTrackTranscodes(releaseIntegrationTranscoderPublisher{}),
		releaseadapter.NewWaveformJobs(db, releaseIntegrationTranscoderPublisher{}),
		spiceDB,
		releaseadapter.NewTrackFiles(&recordingTrackFileDeleter{}),
		releaseadapter.NewMemberSummaries(db, ""),
		releaseadapter.NewArtistSummaries(db),
		writer,
	)

	release, err := releases.CreateRelease(ctx, connect.NewRequest(&managev1.CreateReleaseRequest{
		Title:    "Authorization lifecycle",
		Type:     managev1.ReleaseType_RELEASE_TYPE_ALBUM,
		Document: creativeContentIntegrationDocument("en", "Authorization lifecycle"),
	}))
	require.NoError(t, err)
	requireSynchronousAuthorizedResource(t, spiceDB, policyv1.Release.LookupManage(), release.Msg.Id, adminID, true)
	direct, err := tracks.CreateTrack(ctx, connect.NewRequest(&managev1.CreateTrackRequest{ReleaseId: release.Msg.Id, Title: "Direct delete"}))
	require.NoError(t, err)
	requireSynchronousAuthorizedResource(t, spiceDB, policyv1.Track.LookupManage(), direct.Msg.Id, adminID, true)
	cascadedOne, err := tracks.CreateTrack(ctx, connect.NewRequest(&managev1.CreateTrackRequest{ReleaseId: release.Msg.Id, Title: "Cascade one"}))
	require.NoError(t, err)
	requireSynchronousAuthorizedResource(t, spiceDB, policyv1.Track.LookupManage(), cascadedOne.Msg.Id, adminID, true)
	cascadedTwo, err := tracks.CreateTrack(ctx, connect.NewRequest(&managev1.CreateTrackRequest{ReleaseId: release.Msg.Id, Title: "Cascade two"}))
	require.NoError(t, err)
	requireSynchronousAuthorizedResource(t, spiceDB, policyv1.Track.LookupManage(), cascadedTwo.Msg.Id, adminID, true)

	deletedTrack, err := tracks.DeleteTrack(ctx, connect.NewRequest(&managev1.DeleteTrackRequest{Id: direct.Msg.Id}))
	require.NoError(t, err)
	require.True(t, deletedTrack.Msg.Success)
	requireSynchronousAuthorizedResource(t, spiceDB, policyv1.Track.LookupManage(), direct.Msg.Id, adminID, false)

	deletedRelease, err := releases.DeleteRelease(ctx, connect.NewRequest(&managev1.DeleteReleaseRequest{Id: release.Msg.Id}))
	require.NoError(t, err)
	require.True(t, deletedRelease.Msg.Success)
	for _, ref := range []struct {
		lookup policyv1.ResourceLookup
		id     string
	}{
		{policyv1.Release.LookupManage(), release.Msg.Id},
		{policyv1.Track.LookupManage(), direct.Msg.Id},
		{policyv1.Track.LookupManage(), cascadedOne.Msg.Id},
		{policyv1.Track.LookupManage(), cascadedTwo.Msg.Id},
	} {
		requireSynchronousAuthorizedResource(t, spiceDB, ref.lookup, ref.id, adminID, false)
	}
}

func requireSynchronousAuthorizedResource(t *testing.T, spiceDB *auth.SpiceDBClient, lookup policyv1.ResourceLookup, resourceID, identityID string, expected bool) {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	require.NoError(t, err)
	resources, err := spiceDB.LookupResources(t.Context(), lookup, actor)
	require.NoError(t, err)
	require.Equal(t, expected, slices.Contains(resources, resourceID))
}
