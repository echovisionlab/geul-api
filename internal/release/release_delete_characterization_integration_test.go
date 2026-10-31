//go:build integration

package release_test

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	releasepkg "github.com/echovisionlab/geul-api/internal/release"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/releasetest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// These tests pin DeleteRelease before it is split into smaller steps: the
// cascade it performs, the upload-session guard, the bounded retry loop, the
// authorization relationships it removes and the audit it records.

const releaseDeletedUploadReason = "Release deleted"

// releaseDeleteCleaner is a TrackFileManager whose upload-session cleanup runs
// a test-supplied step and records every call.
type releaseDeleteCleaner struct {
	mu      sync.Mutex
	calls   []string
	reasons []string
	cleanup func(trackID string) error
}

func (c *releaseDeleteCleaner) CleanupTrackUploadSessions(_ context.Context, trackID, reason string) error {
	c.mu.Lock()
	c.calls = append(c.calls, trackID)
	c.reasons = append(c.reasons, reason)
	cleanup := c.cleanup
	c.mu.Unlock()
	if cleanup == nil {
		return nil
	}
	return cleanup(trackID)
}

func (c *releaseDeleteCleaner) recorded() ([]string, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls), slices.Clone(c.reasons)
}

func (*releaseDeleteCleaner) RequireNoTrackUploadSessionsWithDB(context.Context, *gorm.DB, string) error {
	return nil
}

func (*releaseDeleteCleaner) DeleteFileByID(context.Context, string) error { return nil }

type recordingReleaseWaveformJobs struct {
	calls   [][]string
	reasons []managev1.TranscodeCancelReason
}

func (w *recordingReleaseWaveformJobs) CancelTracks(_ context.Context, trackIDs []string, reason managev1.TranscodeCancelReason) error {
	w.calls = append(w.calls, slices.Clone(trackIDs))
	w.reasons = append(w.reasons, reason)
	return nil
}

type releaseDeleteFixture struct {
	db         *gorm.DB
	spiceDB    *auth.SpiceDBClient
	ctx        context.Context
	adminID    string
	service    *releasepkg.ConnectHandler
	cleaner    *releaseDeleteCleaner
	waveforms  *recordingReleaseWaveformJobs
	publisher  *recordingReleaseAsyncPublisher
	releaseID  string
	trackIDs   []string
	documentID string
}

// newReleaseDeleteFixture creates a Release with trackCount Tracks, a credit
// with a source note, a share link and an extra target locale, all through the
// audited Release service.
func newReleaseDeleteFixture(t *testing.T, trackCount int) releaseDeleteFixture {
	t.Helper()
	db := testutil.NewIntegrationDB(t)
	spiceDB := integrationSpiceDB(t)
	adminID := testutil.IntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, adminID, "Release delete characterization admin")
	testutil.GrantIntegrationGlobalRole(t, spiceDB, adminID, policyv1.Role.Admin())
	ctx := withAuditedRequestContext(t, releaseIntegrationAdminCtx(adminID))
	cleaner := &releaseDeleteCleaner{}
	waveforms := &recordingReleaseWaveformJobs{}
	publisher := &recordingReleaseAsyncPublisher{}
	store := newPageIntegrationContentBlockStore(t, spiceDB)
	service := releasepkg.NewConnectHandler(releasetest.NewService(t, releasepkg.Deps{
		DB: db, SpiceDB: spiceDB, Audit: apitelemetry.NewDurableWriter(db), ContentBlocks: store,
		ContentSignals: publisher, TrackFiles: releaseadapter.NewTrackFiles(cleaner), WaveformJobs: waveforms,
	}))
	created, err := service.CreateRelease(ctx, connect.NewRequest(&managev1.CreateReleaseRequest{
		Title:    "Release delete characterization",
		Type:     managev1.ReleaseType_RELEASE_TYPE_ALBUM,
		Document: creativeContentIntegrationDocument("en", "Release delete characterization"),
	}))
	require.NoError(t, err)
	releaseID := created.Msg.Id

	// The Tracks are fixture state: their creation audit stays in memory.
	tracks := releasepkg.NewTrackConnectHandler(releasetest.NewService(t, releasepkg.Deps{
		DB: db, SpiceDB: spiceDB, Audit: &audittest.Recorder{}, ContentBlocks: store,
		TrackFiles: releaseadapter.NewTrackFiles(&releaseDeleteCleaner{}),
	}))
	trackIDs := make([]string, 0, trackCount)
	for index := range trackCount {
		track, err := tracks.CreateTrack(ctx, connect.NewRequest(&managev1.CreateTrackRequest{
			ReleaseId: releaseID, Title: "Cascade track",
		}))
		require.NoError(t, err, "track %d", index)
		trackIDs = append(trackIDs, track.Msg.Id)
	}

	creditID := testutil.IntegrationUUID()
	now := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, db.Exec(`INSERT INTO release_credit (id, release_id, credited_name, created_at) VALUES (?::uuid, ?::uuid, 'Credited', ?)`, creditID, releaseID, now).Error)
	require.NoError(t, db.Exec(`INSERT INTO release_credit_locale (credit_id, locale, note, updated_at) VALUES (?::uuid, 'en', 'Note', ?)`, creditID, now).Error)
	for _, trackID := range trackIDs {
		require.NoError(t, db.Exec(`INSERT INTO track_credit (track_id, credited_name) VALUES (?::uuid, 'Track credit')`, trackID).Error)
	}
	shareLabel := "Release delete characterization share"
	// The link is fixture state; its creation audit is not part of the
	// characterized delete.
	shareExpiresAt := time.Now().Add(time.Hour)
	require.NoError(t, db.Create(&model.ShareLink{
		Token: strings.ReplaceAll(releaseID, "-", ""), EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_RELEASE.String(),
		EntityID: releaseID, Label: &shareLabel, CreatedAt: time.Now(), ExpiresAt: &shareExpiresAt,
	}).Error)
	releasepkg.SeedReleaseTargetLocaleForTest(t, db, store, releaseID, "fr", "Titre", nil)

	var documentID string
	require.NoError(t, db.Raw(`SELECT content_document_id::text FROM release WHERE id = ?`, releaseID).Scan(&documentID).Error)
	return releaseDeleteFixture{
		db: db, spiceDB: spiceDB, ctx: ctx, adminID: adminID, service: service,
		cleaner: cleaner, waveforms: waveforms, publisher: publisher,
		releaseID: releaseID, trackIDs: trackIDs, documentID: documentID,
	}
}

func (f releaseDeleteFixture) delete() (*connect.Response[managev1.DeleteResponse], error) {
	return f.service.DeleteRelease(f.ctx, connect.NewRequest(&managev1.DeleteReleaseRequest{Id: f.releaseID}))
}

// releaseDeleteRows counts every row DeleteRelease removes directly or through
// foreign-key cascades.
type releaseDeleteRows struct {
	Release, Tracks, TrackCredits, Credits, CreditNotes, Translations, ShareLinks, Documents int64
}

func (f releaseDeleteFixture) rows(t *testing.T) releaseDeleteRows {
	t.Helper()
	var rows releaseDeleteRows
	trackIDs := append([]string{testutil.IntegrationUUID()}, f.trackIDs...)
	require.NoError(t, f.db.Raw(`
		SELECT
		  (SELECT COUNT(*) FROM release WHERE id = ?::uuid) AS release,
		  (SELECT COUNT(*) FROM track WHERE release_id = ?::uuid) AS tracks,
		  (SELECT COUNT(*) FROM track_credit WHERE track_id::text IN ?) AS track_credits,
		  (SELECT COUNT(*) FROM release_credit WHERE release_id = ?::uuid) AS credits,
		  (SELECT COUNT(*) FROM release_credit_locale AS l JOIN release_credit AS c ON c.id = l.credit_id WHERE c.release_id = ?::uuid) AS credit_notes,
		  (SELECT COUNT(*) FROM release_translation WHERE entity_id = ?::uuid) AS translations,
		  (SELECT COUNT(*) FROM share_link WHERE entity_type = 'SHARE_LINK_ENTITY_TYPE_RELEASE' AND entity_id::text = ?) AS share_links,
		  (SELECT COUNT(*) FROM content_document WHERE id = ?::uuid) AS documents`,
		f.releaseID, f.releaseID, trackIDs, f.releaseID, f.releaseID, f.releaseID, f.releaseID, f.documentID,
	).Scan(&rows).Error)
	return rows
}

// relationships reads the exact SpiceDB tuples owned by the Release and each
// Track, keyed by resource.
func (f releaseDeleteFixture) relationships(t *testing.T) map[string][]string {
	t.Helper()
	read := func(plan policyv1.RelationshipSnapshotPlan, err error) []string {
		require.NoError(t, err)
		snapshots, _, err := f.spiceDB.SnapshotResourceRelationshipDescriptors(t.Context(), plan)
		require.NoError(t, err)
		tuples := make([]string, 0, len(snapshots))
		for _, snapshot := range snapshots {
			tuples = append(tuples, snapshot.ResourceType()+"#"+snapshot.Relation()+"@"+snapshot.SubjectType()+":"+snapshot.SubjectID())
		}
		return tuples
	}
	relationships := map[string][]string{}
	plan, err := policyv1.Release.Snapshot(f.releaseID)
	relationships["release"] = read(plan, err)
	for _, trackID := range f.trackIDs {
		plan, err := policyv1.Track.Snapshot(trackID)
		relationships["track:"+trackID] = read(plan, err)
	}
	return relationships
}

func (f releaseDeleteFixture) auditActions(t *testing.T) []string {
	t.Helper()
	actions := []string{}
	for _, row := range readReleaseAuditRows(t, f.db, f.releaseID) {
		actions = append(actions, row.Action)
	}
	return actions
}

func (f releaseDeleteFixture) seedUploadSession(t *testing.T, trackID string) {
	t.Helper()
	fileID := testutil.IntegrationUUID()
	seedIntegrationFile(t, f.db, fileID, "upload", "audio/wav", nil)
	require.NoError(t, f.db.Create(&model.UploadSession{
		UploadID: "upload-" + testutil.IntegrationUUID(), FileID: fileID,
		UploadType: managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String(), EntityID: trackID,
		FileName: "upload.wav", FileSize: 1024, RequestedMime: "audio/wav", TotalParts: 1, ChunkSize: 1024,
		Status: model.UploadSessionStatusUploading,
	}).Error)
}

func (f releaseDeleteFixture) deleteUploadSessions(trackID string) error {
	return f.db.Where("upload_type = ? AND entity_id = ?", managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String(), trackID).
		Delete(&model.UploadSession{}).Error
}

func (f releaseDeleteFixture) requireUntouched(t *testing.T, rows releaseDeleteRows, relationships map[string][]string, audits []string) {
	t.Helper()
	require.Equal(t, rows, f.rows(t))
	require.Equal(t, relationships, f.relationships(t))
	require.Equal(t, audits, f.auditActions(t))
	require.Empty(t, f.waveforms.calls)
	require.Zero(t, f.publisher.calls)
}

func TestDeleteReleaseCascadeCharacterizationIntegration(t *testing.T) {
	t.Parallel()
	fixture := newReleaseDeleteFixture(t, 2)
	require.Equal(t, releaseDeleteRows{
		Release: 1, Tracks: 2, TrackCredits: 2, Credits: 1, CreditNotes: 2, Translations: 2, ShareLinks: 1, Documents: 1,
	}, fixture.rows(t))
	policy := []string{"release#policy@platform:global"}
	trackPolicy := []string{"track#policy@platform:global"}
	require.Equal(t, map[string][]string{
		"release":                      policy,
		"track:" + fixture.trackIDs[0]: trackPolicy,
		"track:" + fixture.trackIDs[1]: trackPolicy,
	}, fixture.relationships(t))
	require.Equal(t, []string{string(sharedtelemetry.AuditReleaseCreated)}, fixture.auditActions(t))

	response, err := fixture.delete()
	require.NoError(t, err)
	require.True(t, proto.Equal(&managev1.DeleteResponse{Success: true}, response.Msg))

	require.Equal(t, releaseDeleteRows{}, fixture.rows(t), "the Release, its Tracks and every owned row are gone")
	require.Equal(t, map[string][]string{
		"release":                      {},
		"track:" + fixture.trackIDs[0]: {},
		"track:" + fixture.trackIDs[1]: {},
	}, fixture.relationships(t))
	require.Equal(t, []string{
		string(sharedtelemetry.AuditReleaseCreated), string(sharedtelemetry.AuditReleaseDeleted),
	}, fixture.auditActions(t), "the Track cascade records no per-Track audit")
	calls, _ := fixture.cleaner.recorded()
	require.Empty(t, calls, "no upload session means no cleanup call")
	require.Len(t, fixture.waveforms.calls, 1)
	require.ElementsMatch(t, fixture.trackIDs, fixture.waveforms.calls[0])
	require.Equal(t, []managev1.TranscodeCancelReason{managev1.TranscodeCancelReason_TRANSCODE_CANCEL_REASON_USER_DELETED}, fixture.waveforms.reasons)
	require.Zero(t, fixture.publisher.calls, "DeleteRelease publishes no content event")
}

func TestDeleteReleaseRejectionsCharacterizationIntegration(t *testing.T) {
	t.Parallel()

	t.Run("caller without delete permission", func(t *testing.T) {
		t.Parallel()
		fixture := newReleaseDeleteFixture(t, 1)
		rows, relationships, audits := fixture.rows(t), fixture.relationships(t), fixture.auditActions(t)
		memberIdentity := testutil.IntegrationUUID()
		testutil.SeedPostIntegrationIdentity(t, fixture.db, memberIdentity, "Release delete outsider")
		ctx := withAuditedRequestContext(t, releaseIntegrationAdminCtx(memberIdentity))
		response, err := fixture.service.DeleteRelease(ctx, connect.NewRequest(&managev1.DeleteReleaseRequest{Id: fixture.releaseID}))
		require.Nil(t, response)
		requireConnectError(t, err, connect.CodeNotFound, "release not found: "+fixture.releaseID)
		fixture.requireUntouched(t, rows, relationships, audits)
	})

	t.Run("finalizing track upload", func(t *testing.T) {
		t.Parallel()
		fixture := newReleaseDeleteFixture(t, 2)
		fixture.seedUploadSession(t, fixture.trackIDs[1])
		fixture.cleaner.cleanup = func(string) error { return mediaasset.ErrUploadSessionNotAbortable }
		rows, relationships, audits := fixture.rows(t), fixture.relationships(t), fixture.auditActions(t)

		response, err := fixture.delete()
		require.Nil(t, response)
		requireConnectError(t, err, connect.CodeFailedPrecondition,
			"track audio upload is finalizing; retry its completion before deleting the release")
		calls, reasons := fixture.cleaner.recorded()
		require.Len(t, calls, 1, "a pending session sends cleanup to every Track, which stops at the first refusal")
		require.Contains(t, fixture.trackIDs, calls[0])
		require.Equal(t, []string{releaseDeletedUploadReason}, reasons)
		fixture.requireUntouched(t, rows, relationships, audits)
	})

	t.Run("upload sessions that survive every cleanup pass", func(t *testing.T) {
		t.Parallel()
		fixture := newReleaseDeleteFixture(t, 1)
		fixture.seedUploadSession(t, fixture.trackIDs[0])
		rows, relationships, audits := fixture.rows(t), fixture.relationships(t), fixture.auditActions(t)

		response, err := fixture.delete()
		require.Nil(t, response)
		requireConnectError(t, err, connect.CodeInternal,
			"cleanup release track audio uploads: track upload sessions changed during deletion")
		calls, _ := fixture.cleaner.recorded()
		require.Equal(t, slices.Repeat(fixture.trackIDs, releasepkg.MaxTrackUploadCleanupPasses), calls)
		fixture.requireUntouched(t, rows, relationships, audits)
	})
}

func TestDeleteReleaseUploadSessionRetryCharacterizationIntegration(t *testing.T) {
	t.Parallel()

	t.Run("cleanup removes the session before the delete transaction", func(t *testing.T) {
		t.Parallel()
		fixture := newReleaseDeleteFixture(t, 1)
		fixture.seedUploadSession(t, fixture.trackIDs[0])
		fixture.cleaner.cleanup = fixture.deleteUploadSessions

		_, err := fixture.delete()
		require.NoError(t, err)
		calls, reasons := fixture.cleaner.recorded()
		require.Equal(t, fixture.trackIDs, calls)
		require.Equal(t, []string{releaseDeletedUploadReason}, reasons)
		require.Equal(t, releaseDeleteRows{}, fixture.rows(t))
	})

	// The delete transaction rechecks upload sessions after taking the global
	// authorization lock. Holding that lock lets the test add a session after
	// cleanup finished and before the recheck, which forces another pass.
	t.Run("session added inside the delete transaction is cleaned on the next pass", func(t *testing.T) {
		t.Parallel()
		fixture := newReleaseDeleteFixture(t, 1)
		authority := holdAuthorizationLock(t, fixture.db)
		fixture.cleaner.cleanup = fixture.deleteUploadSessions

		result := make(chan error, 1)
		go func() {
			_, err := fixture.delete()
			result <- err
		}()
		waitForAuthorizationLockWaiter(t, fixture.db)
		fixture.seedUploadSession(t, fixture.trackIDs[0])
		authority.release(t)

		require.NoError(t, <-result)
		calls, _ := fixture.cleaner.recorded()
		require.Equal(t, fixture.trackIDs, calls, "the second pass cleans the late session")
		require.Equal(t, releaseDeleteRows{}, fixture.rows(t))
		require.Equal(t, []string{
			string(sharedtelemetry.AuditReleaseCreated), string(sharedtelemetry.AuditReleaseDeleted),
		}, fixture.auditActions(t))
	})

	t.Run("sessions added on every pass exhaust the retry budget", func(t *testing.T) {
		t.Parallel()
		fixture := newReleaseDeleteFixture(t, 1)
		rows, relationships, audits := fixture.rows(t), fixture.relationships(t), fixture.auditActions(t)
		authority := holdAuthorizationLock(t, fixture.db)
		fixture.cleaner.cleanup = func(trackID string) error {
			if err := fixture.deleteUploadSessions(trackID); err != nil {
				return err
			}
			return authority.acquire()
		}

		result := make(chan error, 1)
		go func() {
			_, err := fixture.delete()
			result <- err
		}()
		for range releasepkg.MaxTrackUploadCleanupPasses {
			waitForAuthorizationLockWaiter(t, fixture.db)
			fixture.seedUploadSession(t, fixture.trackIDs[0])
			authority.release(t)
		}

		requireConnectError(t, <-result, connect.CodeFailedPrecondition,
			"track audio uploads changed during deletion; retry the request")
		calls, _ := fixture.cleaner.recorded()
		require.Equal(t, slices.Repeat(fixture.trackIDs, releasepkg.MaxTrackUploadCleanupPasses-1), calls)
		fixture.requireUntouched(t, rows, relationships, audits)
	})
}

// authorizationLock holds authzmutation's global advisory lock on a dedicated
// session, so a test can pause any authorization-mutating transaction.
type authorizationLock struct{ conn *sql.Conn }

func holdAuthorizationLock(t *testing.T, db *gorm.DB) *authorizationLock {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	conn, err := sqlDB.Conn(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	lock := &authorizationLock{conn: conn}
	require.NoError(t, lock.acquire())
	return lock
}

// acquire may run on the goroutine under test, so it reports instead of failing.
func (l *authorizationLock) acquire() error {
	_, err := l.conn.ExecContext(context.Background(), "SELECT pg_advisory_lock($1)", authzmutation.GlobalLockKey)
	return err
}

func (l *authorizationLock) release(t *testing.T) {
	t.Helper()
	var released bool
	require.NoError(t, l.conn.QueryRowContext(t.Context(), "SELECT pg_advisory_unlock($1)", authzmutation.GlobalLockKey).Scan(&released))
	require.True(t, released)
}

func waitForAuthorizationLockWaiter(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.Eventually(t, func() bool {
		var waiting bool
		return db.Raw(`
			SELECT EXISTS (
				SELECT 1 FROM pg_locks AS lock
				JOIN pg_stat_activity AS activity ON activity.pid = lock.pid
				WHERE activity.datname = current_database()
				  AND lock.locktype = 'advisory' AND NOT lock.granted AND lock.objsubid = 1
				  AND lock.classid::bigint = (?::bigint >> 32) AND lock.objid::bigint = (?::bigint & 4294967295)
			)`, authzmutation.GlobalLockKey, authzmutation.GlobalLockKey).Scan(&waiting).Error == nil && waiting
	}, 30*time.Second, 20*time.Millisecond)
}
