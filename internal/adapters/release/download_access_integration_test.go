//go:build integration

package release

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	releasepublic "github.com/echovisionlab/geul-api/internal/release/public"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type releaseDownloadPhaseOneBlockingLogger struct {
	logger.Interface
	loaded  chan struct{}
	release chan struct{}
	once    sync.Once
}

func (l *releaseDownloadPhaseOneBlockingLogger) Trace(
	ctx context.Context,
	begin time.Time,
	fc func() (string, int64),
	err error,
) {
	sql, rows := fc()
	if strings.Contains(sql, "track.download_audience") {
		l.once.Do(func() {
			close(l.loaded)
			<-l.release
		})
	}
	l.Interface.Trace(ctx, begin, func() (string, int64) { return sql, rows }, err)
}

func TestDownloadAccessRejectsReleaseOrShareRevokedAfterPhaseOneIntegration(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		status     string
		mode       mediaasset.ContentDownloadOwnerAccessMode
		mutate     func(*testing.T, *gorm.DB, string, string)
		shareProof bool
	}{
		{
			name:   "release unpublished",
			status: managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED.String(),
			mode:   mediaasset.ContentDownloadOwnerAccessPublic,
			mutate: func(t *testing.T, db *gorm.DB, releaseID, _ string) {
				t.Helper()
				require.NoError(t, db.Exec(`UPDATE release SET status = ? WHERE id = ?::uuid`, managev1.ReleaseStatus_RELEASE_STATUS_DRAFT.String(), releaseID).Error)
			},
		},
		{
			name:       "share link deleted",
			status:     managev1.ReleaseStatus_RELEASE_STATUS_DRAFT.String(),
			mode:       mediaasset.ContentDownloadOwnerAccessShare,
			shareProof: true,
			mutate: func(t *testing.T, db *gorm.DB, _, shareID string) {
				t.Helper()
				require.NoError(t, db.Exec(`DELETE FROM share_link WHERE id = ?::uuid`, shareID).Error)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			pg := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true})
			db := pg.DB
			releaseID, documentID, trackID, fileID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
			now := time.Now().UTC()
			require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile, revision) VALUES (?::uuid, 'compact', ?::uuid)`, documentID, uuid.NewString()).Error)
			require.NoError(t, db.Exec(`INSERT INTO release (id, type, status, content_document_id) VALUES (?::uuid, ?::release_type, ?, ?::uuid)`, releaseID, managev1.ReleaseType_RELEASE_TYPE_ALBUM.String(), testCase.status, documentID).Error)
			require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, extension, mime_type, file_size, sha256) VALUES (?::uuid, 'release-original', 'wav', 'audio/wav', 4096, ?)`, fileID, make([]byte, 32)).Error)
			require.NoError(t, db.Exec(`INSERT INTO track (id, release_id, track_number, title, audio_original_file_id, download_audience) VALUES (?::uuid, ?::uuid, 1, 'Original', ?::uuid, 'public')`, trackID, releaseID, fileID).Error)

			authorization := mediaasset.ContentDownloadOwnerAuthorization{
				ResourceType: "release", ResourceID: releaseID, Status: testCase.status, DocumentID: documentID,
				Mode: testCase.mode,
			}
			shareID := ""
			if testCase.shareProof {
				shareID = uuid.NewString()
				expiresAt := now.Add(time.Hour)
				require.NoError(t, db.Exec(`INSERT INTO share_link (id, token, entity_type, entity_id, expires_at) VALUES (?::uuid, ?, ?, ?::uuid, ?)`, shareID, uuid.NewString(), managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_RELEASE.String(), releaseID, expiresAt).Error)
				authorization.ShareLink = &mediaasset.ContentDownloadShareLinkWitness{
					ID: shareID, EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_RELEASE.String(), EntityID: releaseID, ExpiresAt: &expiresAt,
				}
			}

			loaded, releasePhase := make(chan struct{}), make(chan struct{})
			serviceDB := db.Session(&gorm.Session{Logger: &releaseDownloadPhaseOneBlockingLogger{
				Interface: db.Logger, loaded: loaded, release: releasePhase,
			}})
			resolver := NewDownloadAccess(serviceDB, &auth.SpiceDBClient{}, noOpSegmentConfigs{})
			type resolveResult struct {
				authorizations map[string]releasepublic.TrackDownloadAuthorization
				err            error
			}
			resolved := make(chan resolveResult, 1)
			go func() {
				authorizations, err := resolver.Resolve(
					t.Context(), releaseID, testCase.status, authorization,
					[]releasepublic.TrackDownloadAccessRequest{{TrackID: trackID, FileID: fileID}},
					func(releasepublic.MediaFile) (*commonv1.ExpiringMediaRef, error) {
						return &commonv1.ExpiringMediaRef{Url: "stale"}, nil
					},
				)
				resolved <- resolveResult{authorizations: authorizations, err: err}
			}()
			select {
			case <-loaded:
			case <-time.After(5 * time.Second):
				t.Fatal("release download did not reach phase one")
			}
			testCase.mutate(t, db, releaseID, shareID)
			close(releasePhase)
			got := <-resolved
			require.NoError(t, got.err)
			require.Equal(t, openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE, got.authorizations[trackID].Access.GetAction())
			require.Nil(t, got.authorizations[trackID].Download)
		})
	}
}

func TestDownloadAccessHoldsReleaseShareAndTrackFencesThroughSigningIntegration(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		status     string
		mode       mediaasset.ContentDownloadOwnerAccessMode
		shareProof bool
		mutate     func(*gorm.DB, string, string, string, string) error
	}{
		{
			name:   "release status",
			status: managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED.String(),
			mode:   mediaasset.ContentDownloadOwnerAccessPublic,
			mutate: func(db *gorm.DB, releaseID, _, _, _ string) error {
				return db.Exec(`UPDATE release SET status = ? WHERE id = ?::uuid`, managev1.ReleaseStatus_RELEASE_STATUS_DRAFT.String(), releaseID).Error
			},
		},
		{
			name:       "share link",
			status:     managev1.ReleaseStatus_RELEASE_STATUS_DRAFT.String(),
			mode:       mediaasset.ContentDownloadOwnerAccessShare,
			shareProof: true,
			mutate: func(db *gorm.DB, _, shareID, _, _ string) error {
				return db.Exec(`DELETE FROM share_link WHERE id = ?::uuid`, shareID).Error
			},
		},
		{
			name:   "track original replacement",
			status: managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED.String(),
			mode:   mediaasset.ContentDownloadOwnerAccessPublic,
			mutate: func(db *gorm.DB, _, _, trackID, replacementFileID string) error {
				return db.Exec(`UPDATE track SET audio_original_file_id = ?::uuid, download_audience = 'disabled' WHERE id = ?::uuid`, replacementFileID, trackID).Error
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			pg := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true})
			db := pg.DB
			releaseID, documentID, trackID := uuid.NewString(), uuid.NewString(), uuid.NewString()
			fileID, replacementFileID := uuid.NewString(), uuid.NewString()
			require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile, revision) VALUES (?::uuid, 'compact', ?::uuid)`, documentID, uuid.NewString()).Error)
			require.NoError(t, db.Exec(`INSERT INTO release (id, type, status, content_document_id) VALUES (?::uuid, ?::release_type, ?, ?::uuid)`, releaseID, managev1.ReleaseType_RELEASE_TYPE_ALBUM.String(), testCase.status, documentID).Error)
			require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, extension, mime_type, file_size, sha256) VALUES (?::uuid, 'original', 'wav', 'audio/wav', 4096, ?), (?::uuid, 'replacement', 'wav', 'audio/wav', 4096, ?)`, fileID, make([]byte, 32), replacementFileID, make([]byte, 32)).Error)
			require.NoError(t, db.Exec(`INSERT INTO track (id, release_id, track_number, title, audio_original_file_id, download_audience) VALUES (?::uuid, ?::uuid, 1, 'Original', ?::uuid, 'public')`, trackID, releaseID, fileID).Error)

			authorization := mediaasset.ContentDownloadOwnerAuthorization{
				ResourceType: "release", ResourceID: releaseID, Status: testCase.status, DocumentID: documentID,
				Mode: testCase.mode,
			}
			shareID := ""
			if testCase.shareProof {
				shareID = uuid.NewString()
				expiresAt := time.Now().UTC().Add(time.Hour)
				require.NoError(t, db.Exec(`INSERT INTO share_link (id, token, entity_type, entity_id, expires_at) VALUES (?::uuid, ?, ?, ?::uuid, ?)`, shareID, uuid.NewString(), managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_RELEASE.String(), releaseID, expiresAt).Error)
				authorization.ShareLink = &mediaasset.ContentDownloadShareLinkWitness{
					ID: shareID, EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_RELEASE.String(), EntityID: releaseID, ExpiresAt: &expiresAt,
				}
			}

			resolver := NewDownloadAccess(db, &auth.SpiceDBClient{}, noOpSegmentConfigs{})
			signing, releaseSigning := make(chan struct{}), make(chan struct{})
			type resolveResult struct {
				authorizations map[string]releasepublic.TrackDownloadAuthorization
				err            error
			}
			resolved := make(chan resolveResult, 1)
			go func() {
				authorizations, err := resolver.Resolve(
					t.Context(), releaseID, testCase.status, authorization,
					[]releasepublic.TrackDownloadAccessRequest{{TrackID: trackID, FileID: fileID}},
					func(file releasepublic.MediaFile) (*commonv1.ExpiringMediaRef, error) {
						close(signing)
						<-releaseSigning
						return &commonv1.ExpiringMediaRef{Url: "signed:" + file.ID}, nil
					},
				)
				resolved <- resolveResult{authorizations: authorizations, err: err}
			}()
			select {
			case <-signing:
			case <-time.After(5 * time.Second):
				t.Fatal("release download did not reach signing under the phase-two fences")
			}

			mutationDone := make(chan error, 1)
			mutationStarted := make(chan struct{})
			go func() {
				close(mutationStarted)
				mutationDone <- testCase.mutate(db, releaseID, shareID, trackID, replacementFileID)
			}()
			<-mutationStarted
			select {
			case err := <-mutationDone:
				require.NoError(t, err)
				t.Fatal("revocation committed while signing still held the release/share/track fence")
			case <-time.After(150 * time.Millisecond):
			}

			close(releaseSigning)
			got := <-resolved
			require.NoError(t, got.err)
			require.Equal(t, openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_DOWNLOAD, got.authorizations[trackID].Access.GetAction())
			require.Equal(t, "signed:"+fileID, got.authorizations[trackID].Download.GetUrl())
			select {
			case err := <-mutationDone:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("revocation did not commit after signing released the phase-two fences")
			}
		})
	}
}
