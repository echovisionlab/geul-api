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
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/model"
	releasepkg "github.com/echovisionlab/geul-api/internal/release"
	releasepostgres "github.com/echovisionlab/geul-api/internal/release/postgres"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/releasetest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type releaseTrackAuthorityRaceFixture struct {
	db       *gorm.DB
	spiceDB  *auth.SpiceDBClient
	identity string
	ctx      context.Context
	releases *releasepkg.ConnectHandler
	tracks   *releasepkg.TrackConnectHandler
	release  string
}

func newReleaseTrackAuthorityRaceFixture(t *testing.T) releaseTrackAuthorityRaceFixture {
	t.Helper()

	db := newConcurrentServiceIntegrationDB(t)
	identityID := testutil.IntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, identityID, "Release Track authority race")
	spiceDB := integrationSpiceDB(t)
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	ctx := releaseIntegrationAdminCtx(identityID)
	service := releasetest.NewService(t, releasetest.Deps{
		DB: db, SpiceDB: spiceDB, Audit: &audittest.Recorder{},
		Ports: releasepostgres.Ports{
			ContentBlocks: newPageIntegrationContentBlockStore(t, spiceDB),
			Assets:        releaseadapter.NewAssets("cdn.example.com"), OG: releaseadapter.NewOG("cdn.example.com"),
		},
		Transcodes:   releaseadapter.NewTrackTranscodes(releaseIntegrationTranscoderPublisher{}),
		WaveformJobs: releaseadapter.NewWaveformJobs(db, releaseIntegrationTranscoderPublisher{}),
		Members:      releaseadapter.NewMemberSummaries(db, "cdn.example.com"),
	})
	releases := releasepkg.NewConnectHandler(service)
	tracks := releasepkg.NewTrackConnectHandler(service)
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
	testutil.GrantIntegrationGlobalRole(t, f.spiceDB, f.identity, policyv1.Role.User())
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

// requireReleaseTrackMutationWaiting waits until a session of this database
// is blocked by the root lock lockTx holds, so the mutation under test has
// passed its first authorization check and waits for the root.
func requireReleaseTrackMutationWaiting(t *testing.T, db, lockTx *gorm.DB, result <-chan error) {
	t.Helper()
	var holder int
	require.NoError(t, lockTx.Raw("SELECT pg_backend_pid()").Scan(&holder).Error)
	var returned bool
	var early error
	require.Eventually(t, func() bool {
		select {
		case early = <-result:
			returned = true
			return true
		default:
		}
		var waiting bool
		return db.Raw(`
			SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity
				WHERE datname = current_database() AND ? = ANY(pg_blocking_pids(pid))
			)`, holder).Scan(&waiting).Error == nil && waiting
	}, 30*time.Second, 10*time.Millisecond)
	require.False(t, returned, "mutation returned before its root lock was released: %v", early)
}

func TestReleaseMutationsRecheckCurrentAdminAfterRootLockIntegration(t *testing.T) {
	t.Parallel()
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
		requireReleaseTrackMutationWaiting(t, fixture.db, lockTx, result)
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
		requireReleaseTrackMutationWaiting(t, fixture.db, lockTx, result)
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
				Observed:  &managev1.ReleaseCreditsSnapshot{},
			}))
			result <- err
		}()
		requireReleaseTrackMutationWaiting(t, fixture.db, lockTx, result)
		fixture.revokeAdmin(t)
		require.NoError(t, lockTx.Commit().Error)
		require.Equal(t, connect.CodeNotFound, connect.CodeOf(<-result))
		var count int64
		require.NoError(t, fixture.db.Model(&model.ReleaseCredit{}).Where("release_id = ?", fixture.release).Count(&count).Error)
		require.Zero(t, count)
	})

	t.Run("artwork", func(t *testing.T) {
		fixture := newReleaseTrackAuthorityRaceFixture(t)
		fileID := testutil.IntegrationUUID()
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
		requireReleaseTrackMutationWaiting(t, fixture.db, lockTx, result)
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
		requireReleaseTrackMutationWaiting(t, fixture.db, lockTx, result)
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
