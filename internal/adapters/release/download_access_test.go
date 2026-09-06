package release

import (
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	releasepublic "github.com/echovisionlab/geul-api/internal/release/public"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type noOpSegmentConfigs struct{}

func (noOpSegmentConfigs) LoadSegmentConfigs(context.Context, *gorm.DB, []*model.AudienceSegment) error {
	return nil
}

func TestDownloadAccessKeepsSameFilePolicyPerExactTrackRelation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE release (id TEXT PRIMARY KEY, status TEXT NOT NULL, content_document_id TEXT);
		CREATE TABLE share_link (
			id TEXT PRIMARY KEY,
			token TEXT NOT NULL,
			entity_type TEXT NOT NULL,
			entity_id TEXT NOT NULL,
			label TEXT,
			password_hash TEXT,
			expires_at DATETIME,
			created_at DATETIME NOT NULL
		);
		CREATE TABLE file (id TEXT PRIMARY KEY, extension TEXT NOT NULL, mime_type TEXT NOT NULL, file_size INTEGER NOT NULL, file_name TEXT, delete_requested_at DATETIME);
		CREATE TABLE track (id TEXT PRIMARY KEY, release_id TEXT NOT NULL, audio_original_file_id TEXT, download_audience TEXT NOT NULL DEFAULT 'disabled');
		CREATE TABLE track_download_audience_segment (track_id TEXT NOT NULL, audience_segment_id TEXT NOT NULL, PRIMARY KEY (track_id, audience_segment_id));
		CREATE TABLE audience_segment (id TEXT PRIMARY KEY);
	`).Error)
	releaseID, fileID := uuid.NewString(), uuid.NewString()
	publicTrackID, disabledTrackID := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO release (id, status) VALUES (?, 'RELEASE_STATUS_PUBLISHED')`, releaseID).Error)
	require.NoError(t, db.Exec(`INSERT INTO file (id, extension, mime_type, file_size, file_name) VALUES (?, 'wav', 'audio/wav', 1, 'same.wav')`, fileID).Error)
	require.NoError(t, db.Exec(`INSERT INTO track (id, release_id, audio_original_file_id, download_audience) VALUES (?, ?, ?, 'public'), (?, ?, ?, 'disabled')`, publicTrackID, releaseID, fileID, disabledTrackID, releaseID, fileID).Error)

	resolver := NewDownloadAccess(db, &auth.SpiceDBClient{}, noOpSegmentConfigs{})
	result, err := resolver.Resolve(t.Context(), releaseID, "RELEASE_STATUS_PUBLISHED", mediaasset.ContentDownloadOwnerAuthorization{
		ResourceType: "release", ResourceID: releaseID, Status: "RELEASE_STATUS_PUBLISHED",
		Mode: mediaasset.ContentDownloadOwnerAccessPublic,
	}, []releasepublic.TrackDownloadAccessRequest{
		{TrackID: publicTrackID, FileID: fileID},
		{TrackID: disabledTrackID, FileID: fileID},
	}, func(file releasepublic.MediaFile) (*commonv1.ExpiringMediaRef, error) {
		return &commonv1.ExpiringMediaRef{Url: "signed:" + file.ID}, nil
	})
	require.NoError(t, err)
	require.Equal(t, openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_DOWNLOAD, result[publicTrackID].Access.GetAction())
	require.Equal(t, "signed:"+fileID, result[publicTrackID].Download.GetUrl())
	require.Equal(t, openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE, result[disabledTrackID].Access.GetAction())
	require.Nil(t, result[disabledTrackID].Download)

	require.NoError(t, db.Exec(`UPDATE release SET status = 'RELEASE_STATUS_DRAFT' WHERE id = ?`, releaseID).Error)
	result, err = resolver.Resolve(t.Context(), releaseID, "RELEASE_STATUS_PUBLISHED", mediaasset.ContentDownloadOwnerAuthorization{
		ResourceType: "release", ResourceID: releaseID, Status: "RELEASE_STATUS_PUBLISHED",
		Mode: mediaasset.ContentDownloadOwnerAccessPublic,
	}, []releasepublic.TrackDownloadAccessRequest{{TrackID: publicTrackID, FileID: fileID}}, func(file releasepublic.MediaFile) (*commonv1.ExpiringMediaRef, error) {
		return &commonv1.ExpiringMediaRef{Url: "stale:" + file.ID}, nil
	})
	require.NoError(t, err)
	require.Equal(t, openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE, result[publicTrackID].Access.GetAction())
	require.Nil(t, result[publicTrackID].Download)

	shareID := uuid.NewString()
	expiresAt := time.Now().Add(time.Hour).UTC()
	require.NoError(t, db.Exec(`INSERT INTO share_link (id, token, entity_type, entity_id, expires_at, created_at) VALUES (?, 'opaque', 'SHARE_LINK_ENTITY_TYPE_RELEASE', ?, ?, ?)`, shareID, releaseID, expiresAt, time.Now().UTC()).Error)
	require.NoError(t, db.Exec(`DELETE FROM share_link WHERE id = ?`, shareID).Error)
	result, err = resolver.Resolve(t.Context(), releaseID, "RELEASE_STATUS_DRAFT", mediaasset.ContentDownloadOwnerAuthorization{
		ResourceType: "release", ResourceID: releaseID, Status: "RELEASE_STATUS_DRAFT",
		Mode: mediaasset.ContentDownloadOwnerAccessShare,
		ShareLink: &mediaasset.ContentDownloadShareLinkWitness{
			ID: shareID, EntityType: "SHARE_LINK_ENTITY_TYPE_RELEASE", EntityID: releaseID, ExpiresAt: &expiresAt,
		},
	}, []releasepublic.TrackDownloadAccessRequest{{TrackID: publicTrackID, FileID: fileID}}, func(file releasepublic.MediaFile) (*commonv1.ExpiringMediaRef, error) {
		return &commonv1.ExpiringMediaRef{Url: "stale:" + file.ID}, nil
	})
	require.NoError(t, err)
	require.Equal(t, openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE, result[publicTrackID].Access.GetAction())
	require.Nil(t, result[publicTrackID].Download)
}
