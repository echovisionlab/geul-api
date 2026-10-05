package release

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestTrackAuthorityReplacementResetsPolicyAndSameFilePreserves(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE track (
			id TEXT PRIMARY KEY, release_id TEXT NOT NULL, track_number INTEGER NOT NULL,
			title TEXT NOT NULL, processing_status TEXT, audio_original_file_id TEXT,
			download_audience TEXT NOT NULL DEFAULT 'disabled'
		);
		CREATE TABLE track_download_audience_segment (
			track_id TEXT NOT NULL, audience_segment_id TEXT NOT NULL, created_at DATETIME,
			PRIMARY KEY (track_id, audience_segment_id)
		)
	`).Error)
	trackID := uuid.NewString()
	releaseID := uuid.NewString()
	firstFileID := uuid.NewString()
	secondFileID := uuid.NewString()
	segmentID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO track (
			id, release_id, track_number, title, audio_original_file_id, download_audience
		) VALUES (?, ?, 1, 'Track', ?, 'restricted')
	`, trackID, releaseID, firstFileID).Error)
	require.NoError(t, db.Create(&model.TrackDownloadAudienceSegment{
		TrackID: trackID, AudienceSegmentID: segmentID,
	}).Error)
	authority := NewTrackAuthority(nil)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		result, attachErr := authority.AttachOriginalWithDB(t.Context(), tx, TrackOriginalAudioInput{
			TrackID: trackID, VerifiedFileID: firstFileID,
		})
		require.True(t, result.AlreadyApplied)
		return attachErr
	}))
	var preserved model.Track
	require.NoError(t, db.First(&preserved, "id = ?", trackID).Error)
	require.Equal(t, "restricted", preserved.DownloadAudience)
	var count int64
	require.NoError(t, db.Model(&model.TrackDownloadAudienceSegment{}).Where("track_id = ?", trackID).Count(&count).Error)
	require.Equal(t, int64(1), count)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, attachErr := authority.AttachOriginalWithDB(t.Context(), tx, TrackOriginalAudioInput{
			TrackID: trackID, VerifiedFileID: secondFileID, ExpectedCurrentFileID: &firstFileID,
		})
		return attachErr
	}))
	var reset model.Track
	require.NoError(t, db.First(&reset, "id = ?", trackID).Error)
	require.Equal(t, secondFileID, *reset.AudioOriginalFileID)
	require.Equal(t, "disabled", reset.DownloadAudience)
	require.NoError(t, db.Model(&model.TrackDownloadAudienceSegment{}).Where("track_id = ?", trackID).Count(&count).Error)
	require.Zero(t, count)
}
