//go:build integration

package release_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	releasepkg "github.com/echovisionlab/geul-api/internal/release"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type trackUploadCleanupFileManager struct {
	cleanupErr   error
	cleanupCalls int
	deletedFiles []string
}

func (m *trackUploadCleanupFileManager) CleanupTrackUploadSessions(context.Context, string, string) error {
	m.cleanupCalls++
	return m.cleanupErr
}

func (m *trackUploadCleanupFileManager) RequireNoTrackUploadSessionsWithDB(context.Context, *gorm.DB, string) error {
	return nil
}

func (m *trackUploadCleanupFileManager) DeleteFileByID(_ context.Context, fileID string) error {
	m.deletedFiles = append(m.deletedFiles, fileID)
	return nil
}

func TestDeleteTrackStopsBeforeMutationWhenAudioUploadIsFinalizing(t *testing.T) {
	t.Parallel()

	db := newServiceIntegrationDB(t)
	trackID := uuid.NewString()
	releaseID := testutil.CreateReleaseFixture(t, db)
	originalFileID := uuid.NewString()
	seedIntegrationFile(t, db, originalFileID, "original", "audio/wav", nil)
	require.NoError(t, db.Exec(
		"INSERT INTO track (id, release_id, track_number, title, audio_original_file_id) VALUES (?, ?, 1, 'Finalizing', ?)",
		trackID,
		releaseID,
		originalFileID,
	).Error)

	files := &trackUploadCleanupFileManager{cleanupErr: releasepkg.ErrTrackUploadSessionNotAbortable}
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	policy, err := policyv1.Track.TouchPolicy(trackID)
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.ApplyRelationships(t.Context(), policy)
	require.NoError(t, err)
	service := releasepkg.NewTrackService(db, releaseadapter.NewTrackTranscodes(nil), releaseadapter.NewWaveformJobs(db, nil), stack.SpiceDBClient, releaseadapter.NewTrackFiles(files), releaseadapter.NewMemberSummaries(db, "cdn.example.com"), releaseadapter.NewArtistSummaries(db))
	ctx := auth.WithUser(context.Background(), admin.AuthUserInfo())

	response, err := service.DeleteTrack(ctx, connect.NewRequest(&managev1.DeleteTrackRequest{Id: trackID}))
	require.Error(t, err)
	require.Nil(t, response)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Equal(t, 1, files.cleanupCalls)
	require.Empty(t, files.deletedFiles)

	var count int64
	require.NoError(t, db.Model(&model.Track{}).Where("id = ?", trackID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}
