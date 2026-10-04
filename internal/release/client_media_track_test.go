package release

import (
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestTrackAuthorityClientMediaCompletedDurationPreservesCAS(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE track (id TEXT PRIMARY KEY,release_id TEXT,track_number INTEGER,title TEXT,audio_original_file_id TEXT,processing_status TEXT,duration_seconds INTEGER,download_audience TEXT);CREATE TABLE track_download_audience_segment (track_id TEXT,audience_segment_id TEXT)`).Error)
	trackID, releaseID, oldID, newID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	duration := 77
	require.NoError(t, db.Exec(`INSERT INTO track (id,release_id,audio_original_file_id,download_audience) VALUES (?,?,?,'public')`, trackID, releaseID, oldID).Error)
	authority := NewTrackAuthority(nil)
	wrongID := uuid.NewString()
	_, err = authority.AttachOriginalWithDB(t.Context(), db, TrackOriginalAudioInput{TrackID: trackID, VerifiedFileID: newID, ExpectedCurrentFileID: &wrongID, ClientMediaReady: true, DurationSeconds: &duration})
	require.Error(t, err)
	_, err = authority.AttachOriginalWithDB(t.Context(), db, TrackOriginalAudioInput{TrackID: trackID, VerifiedFileID: newID, ExpectedCurrentFileID: &oldID, ClientMediaReady: true, DurationSeconds: &duration})
	require.NoError(t, err)
	var track model.Track
	require.NoError(t, db.Where("id = ?", trackID).Take(&track).Error)
	require.Equal(t, managev1.TrackProcessingStatus_TRACK_PROCESSING_STATUS_COMPLETED.String(), *track.ProcessingStatus)
	require.Equal(t, duration, *track.DurationSeconds)
	require.Equal(t, "disabled", track.DownloadAudience)
	require.Equal(t, newID, *track.AudioOriginalFileID)
}
