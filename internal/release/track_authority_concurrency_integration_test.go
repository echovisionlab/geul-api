//go:build integration

package release_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	releasepkg "github.com/echovisionlab/geul-api/internal/release"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type releaseTrackAuthorityRaceFixture struct {
	db       *gorm.DB
	spiceDB  *auth.SpiceDBClient
	identity string
	ctx      context.Context
	releases *releasepkg.ReleaseService
	tracks   *releasepkg.TrackService
	release  string
}

func newReleaseTrackAuthorityRaceFixture(t *testing.T) releaseTrackAuthorityRaceFixture {
	t.Helper()

	db := newConcurrentServiceIntegrationDB(t)
	identityID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, identityID, "Release Track authority race")
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	ctx := releaseIntegrationAdminCtx(identityID)
	files := &recordingReleaseDeleteFileDeleter{}
	releases := releasepkg.NewReleaseService(
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
	tracks := releasepkg.NewTrackService(
		db,
		releaseadapter.NewTrackTranscodes(releaseIntegrationTranscoderPublisher{}),
		releaseadapter.NewWaveformJobs(db, releaseIntegrationTranscoderPublisher{}),
		spiceDB,
		releaseadapter.NewTrackFiles(files),
		releaseadapter.NewMemberSummaries(db, "cdn.example.com"),
		releaseadapter.NewArtistSummaries(db),
	)
	created, err := releases.CreateRelease(ctx, connect.NewRequest(&managev1.CreateReleaseRequest{
		Title:    "Authority race release",
		Type:     managev1.ReleaseType_RELEASE_TYPE_ALBUM,
		Document: creativeContentIntegrationDocument("en", "Authority race release"),
	}))
	require.NoError(t, err)

	return releaseTrackAuthorityRaceFixture{
		db:       db,
		spiceDB:  spiceDB,
		identity: identityID,
		ctx:      ctx,
		releases: releases,
		tracks:   tracks,
		release:  created.Msg.Id,
	}
}

func (f releaseTrackAuthorityRaceFixture) createTrack(t *testing.T, title string) string {
	t.Helper()
	created, err := f.tracks.CreateTrack(f.ctx, connect.NewRequest(&managev1.CreateTrackRequest{
		ReleaseId: f.release,
		Title:     title,
	}))
	require.NoError(t, err)
	return created.Msg.Id
}

func (f releaseTrackAuthorityRaceFixture) revokeAdmin(t *testing.T) {
	t.Helper()
	grantIntegrationGlobalRole(t, f.spiceDB, f.identity, policyv1.Role.User())
}

func lockReleaseAuthorityRaceRoot(t *testing.T, db *gorm.DB, releaseID string) *gorm.DB {
	t.Helper()
	lockTx := db.Begin()
	require.NoError(t, lockTx.Error)
	t.Cleanup(func() {
		_ = lockTx.Rollback().Error
	})
	require.NoError(t, lockTx.Exec("SELECT id FROM release WHERE id = ?::uuid FOR UPDATE", releaseID).Error)
	return lockTx
}

func lockTrackAuthorityRaceRoot(t *testing.T, db *gorm.DB, trackID string) *gorm.DB {
	t.Helper()
	lockTx := db.Begin()
	require.NoError(t, lockTx.Error)
	t.Cleanup(func() {
		_ = lockTx.Rollback().Error
	})
	require.NoError(t, lockTx.Exec("SELECT id FROM track WHERE id = ?::uuid FOR UPDATE", trackID).Error)
	return lockTx
}

func requireReleaseTrackMutationWaiting(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		require.FailNow(t, "mutation returned before its root lock was released", "error: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestReleaseMutationsRecheckCurrentAdminAfterRootLockIntegration(t *testing.T) {
	t.Run("metadata", func(t *testing.T) {
		fixture := newReleaseTrackAuthorityRaceFixture(t)
		lockTx := lockReleaseAuthorityRaceRoot(t, fixture.db, fixture.release)
		catalog := "race-catalog"
		result := make(chan error, 1)
		go func() {
			_, err := fixture.releases.UpdateRelease(fixture.ctx, connect.NewRequest(&managev1.UpdateReleaseRequest{
				Id: fixture.release, CatalogNumber: &catalog,
			}))
			result <- err
		}()
		requireReleaseTrackMutationWaiting(t, result)
		fixture.revokeAdmin(t)
		require.NoError(t, lockTx.Commit().Error)
		require.Equal(t, connect.CodeNotFound, connect.CodeOf(<-result))
		var value *string
		require.NoError(t, fixture.db.Table("release").Select("catalog_number").Where("id = ?", fixture.release).Scan(&value).Error)
		require.Nil(t, value)
	})

	t.Run("lifecycle", func(t *testing.T) {
		fixture := newReleaseTrackAuthorityRaceFixture(t)
		lockTx := lockReleaseAuthorityRaceRoot(t, fixture.db, fixture.release)
		result := make(chan error, 1)
		go func() {
			_, err := fixture.releases.PublishRelease(fixture.ctx, connect.NewRequest(&managev1.PublishReleaseRequest{Id: fixture.release}))
			result <- err
		}()
		requireReleaseTrackMutationWaiting(t, result)
		fixture.revokeAdmin(t)
		require.NoError(t, lockTx.Commit().Error)
		require.Equal(t, connect.CodeNotFound, connect.CodeOf(<-result))
		var status string
		require.NoError(t, fixture.db.Table("release").Select("status").Where("id = ?", fixture.release).Scan(&status).Error)
		require.Equal(t, managev1.ReleaseStatus_RELEASE_STATUS_DRAFT.String(), status)
	})

	t.Run("credits", func(t *testing.T) {
		fixture := newReleaseTrackAuthorityRaceFixture(t)
		lockTx := lockReleaseAuthorityRaceRoot(t, fixture.db, fixture.release)
		result := make(chan error, 1)
		go func() {
			_, err := fixture.releases.SetReleaseCredits(fixture.ctx, connect.NewRequest(&managev1.SetReleaseCreditsRequest{
				ReleaseId: fixture.release,
				Credits:   []*managev1.ReleaseCreditInput{{CreditedName: stringPtr("Must not persist")}},
			}))
			result <- err
		}()
		requireReleaseTrackMutationWaiting(t, result)
		fixture.revokeAdmin(t)
		require.NoError(t, lockTx.Commit().Error)
		require.Equal(t, connect.CodeNotFound, connect.CodeOf(<-result))
		var count int64
		require.NoError(t, fixture.db.Model(&model.ReleaseCredit{}).Where("release_id = ?", fixture.release).Count(&count).Error)
		require.Zero(t, count)
	})

	t.Run("artwork", func(t *testing.T) {
		fixture := newReleaseTrackAuthorityRaceFixture(t)
		fileID := integrationTestUUID()
		seedFileDeleteLifecycleFile(t, fixture.db, fileID, "authority-race", "image/webp", "webp")
		seedHardCutReadyPublicAsset(t, fixture.db, "artwork", "webp", "image/webp", &fileID)
		lockTx := lockReleaseAuthorityRaceRoot(t, fixture.db, fixture.release)
		result := make(chan error, 1)
		go func() {
			_, err := fixture.releases.SetReleaseArtwork(fixture.ctx, connect.NewRequest(&managev1.SetReleaseArtworkRequest{
				ReleaseId: fixture.release, FileId: fileID,
			}))
			result <- err
		}()
		requireReleaseTrackMutationWaiting(t, result)
		fixture.revokeAdmin(t)
		require.NoError(t, lockTx.Commit().Error)
		require.Equal(t, connect.CodeNotFound, connect.CodeOf(<-result))
		var count int64
		require.NoError(t, fixture.db.Model(&model.ReleaseFile{}).Where("release_id = ?", fixture.release).Count(&count).Error)
		require.Zero(t, count)
	})

	t.Run("delete cascade", func(t *testing.T) {
		fixture := newReleaseTrackAuthorityRaceFixture(t)
		trackID := fixture.createTrack(t, "Must survive release deletion")
		lockTx := lockReleaseAuthorityRaceRoot(t, fixture.db, fixture.release)
		result := make(chan error, 1)
		go func() {
			_, err := fixture.releases.DeleteRelease(fixture.ctx, connect.NewRequest(&managev1.DeleteReleaseRequest{Id: fixture.release}))
			result <- err
		}()
		requireReleaseTrackMutationWaiting(t, result)
		fixture.revokeAdmin(t)
		require.NoError(t, lockTx.Commit().Error)
		require.Equal(t, connect.CodeNotFound, connect.CodeOf(<-result))
		var releaseCount, trackCount int64
		require.NoError(t, fixture.db.Model(&model.Release{}).Where("id = ?", fixture.release).Count(&releaseCount).Error)
		require.NoError(t, fixture.db.Model(&model.Track{}).Where("id = ?", trackID).Count(&trackCount).Error)
		require.Equal(t, int64(1), releaseCount)
		require.Equal(t, int64(1), trackCount)
	})
}

func TestReleaseCreateRechecksCurrentAdminBeforeInitialStateIntegration(t *testing.T) {
	fixture := newReleaseTrackAuthorityRaceFixture(t)
	var before int64
	require.NoError(t, fixture.db.Model(&model.Release{}).Count(&before).Error)
	lockTx := fixture.db.Begin()
	require.NoError(t, lockTx.Error)
	require.NoError(t, identitystate.Lock(lockTx, fixture.identity))

	result := make(chan error, 1)
	go func() {
		_, err := fixture.releases.CreateRelease(fixture.ctx, connect.NewRequest(&managev1.CreateReleaseRequest{
			Title: "Must not be created", Type: managev1.ReleaseType_RELEASE_TYPE_ALBUM,
			Document: creativeContentIntegrationDocument("en", "Must not be created"),
		}))
		result <- err
	}()
	requireReleaseTrackMutationWaiting(t, result)
	fixture.revokeAdmin(t)
	require.NoError(t, lockTx.Commit().Error)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(<-result))

	var after int64
	require.NoError(t, fixture.db.Model(&model.Release{}).Count(&after).Error)
	require.Equal(t, before, after)
}

func TestTrackMutationsRecheckCurrentAdminAfterRootLockIntegration(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		fixture := newReleaseTrackAuthorityRaceFixture(t)
		lockTx := lockReleaseAuthorityRaceRoot(t, fixture.db, fixture.release)
		result := make(chan error, 1)
		go func() {
			_, err := fixture.tracks.CreateTrack(fixture.ctx, connect.NewRequest(&managev1.CreateTrackRequest{
				ReleaseId: fixture.release, Title: "Must not exist",
			}))
			result <- err
		}()
		requireReleaseTrackMutationWaiting(t, result)
		fixture.revokeAdmin(t)
		require.NoError(t, lockTx.Commit().Error)
		require.Equal(t, connect.CodeNotFound, connect.CodeOf(<-result))
		var count int64
		require.NoError(t, fixture.db.Model(&model.Track{}).Where("release_id = ?", fixture.release).Count(&count).Error)
		require.Zero(t, count)
	})

	t.Run("update", func(t *testing.T) {
		fixture := newReleaseTrackAuthorityRaceFixture(t)
		trackID := fixture.createTrack(t, "Original")
		lockTx := lockTrackAuthorityRaceRoot(t, fixture.db, trackID)
		title := "Must not persist"
		result := make(chan error, 1)
		go func() {
			_, err := fixture.tracks.UpdateTrack(fixture.ctx, connect.NewRequest(&managev1.UpdateTrackRequest{Id: trackID, Title: &title}))
			result <- err
		}()
		requireReleaseTrackMutationWaiting(t, result)
		fixture.revokeAdmin(t)
		require.NoError(t, lockTx.Commit().Error)
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(<-result))
		var persisted string
		require.NoError(t, fixture.db.Table("track").Select("title").Where("id = ?", trackID).Scan(&persisted).Error)
		require.Equal(t, "Original", persisted)
	})

	t.Run("credits", func(t *testing.T) {
		fixture := newReleaseTrackAuthorityRaceFixture(t)
		trackID := fixture.createTrack(t, "Credit target")
		lockTx := lockTrackAuthorityRaceRoot(t, fixture.db, trackID)
		result := make(chan error, 1)
		go func() {
			_, err := fixture.tracks.SetTrackCredits(fixture.ctx, connect.NewRequest(&managev1.SetTrackCreditsRequest{
				TrackId: trackID,
				Credits: []*managev1.TrackCreditInput{{
					CreditedName: stringPtr("Must not persist"),
				}},
			}))
			result <- err
		}()
		requireReleaseTrackMutationWaiting(t, result)
		fixture.revokeAdmin(t)
		require.NoError(t, lockTx.Commit().Error)
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(<-result))
		var count int64
		require.NoError(t, fixture.db.Model(&model.TrackCredit{}).Where("track_id = ?", trackID).Count(&count).Error)
		require.Zero(t, count)
	})

	t.Run("reorder", func(t *testing.T) {
		fixture := newReleaseTrackAuthorityRaceFixture(t)
		first := fixture.createTrack(t, "First")
		second := fixture.createTrack(t, "Second")
		lockTx := lockReleaseAuthorityRaceRoot(t, fixture.db, fixture.release)
		result := make(chan error, 1)
		go func() {
			_, err := fixture.tracks.ReorderTracks(fixture.ctx, connect.NewRequest(&managev1.ReorderTracksRequest{
				TrackIds: []string{second, first},
			}))
			result <- err
		}()
		requireReleaseTrackMutationWaiting(t, result)
		fixture.revokeAdmin(t)
		require.NoError(t, lockTx.Commit().Error)
		require.Equal(t, connect.CodeNotFound, connect.CodeOf(<-result))
		var ordered []string
		require.NoError(t, fixture.db.Model(&model.Track{}).Where("release_id = ?", fixture.release).Order("track_number ASC").Pluck("id", &ordered).Error)
		require.Equal(t, []string{first, second}, ordered)
	})

	t.Run("delete", func(t *testing.T) {
		fixture := newReleaseTrackAuthorityRaceFixture(t)
		trackID := fixture.createTrack(t, "Must survive")
		lockTx := lockTrackAuthorityRaceRoot(t, fixture.db, trackID)
		result := make(chan error, 1)
		go func() {
			_, err := fixture.tracks.DeleteTrack(fixture.ctx, connect.NewRequest(&managev1.DeleteTrackRequest{Id: trackID}))
			result <- err
		}()
		requireReleaseTrackMutationWaiting(t, result)
		fixture.revokeAdmin(t)
		require.NoError(t, lockTx.Commit().Error)
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(<-result))
		var count int64
		require.NoError(t, fixture.db.Model(&model.Track{}).Where("id = ?", trackID).Count(&count).Error)
		require.Equal(t, int64(1), count)
	})
}
