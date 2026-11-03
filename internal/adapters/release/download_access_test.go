package release

import (
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	optional "github.com/echovisionlab/geul-api/internal/optional"

	releasepublic "github.com/echovisionlab/geul-api/internal/release/public"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type noOpCriteriaConfigs struct{}

func (noOpCriteriaConfigs) LoadCriteriaConfigs(context.Context, *gorm.DB, []*model.MemberCriteria) error {
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
		CREATE TABLE file ( client_media_bundle_id TEXT,id TEXT PRIMARY KEY, extension TEXT NOT NULL, mime_type TEXT NOT NULL, file_size INTEGER NOT NULL, file_name TEXT, delete_requested_at DATETIME);
		CREATE TABLE track (id TEXT PRIMARY KEY, release_id TEXT NOT NULL, audio_original_file_id TEXT, download_access TEXT NOT NULL DEFAULT 'disabled');
		CREATE TABLE track_download_member_criteria (track_id TEXT NOT NULL, member_criteria_id TEXT NOT NULL, PRIMARY KEY (track_id, member_criteria_id));
		CREATE TABLE member_criteria (id TEXT PRIMARY KEY);
	`).Error)
	releaseID, fileID := uuid.NewString(), uuid.NewString()
	publicTrackID, disabledTrackID := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO release (id, status) VALUES (?, 'RELEASE_STATUS_PUBLISHED')`, releaseID).Error)
	require.NoError(t, db.Exec(`INSERT INTO file (id, extension, mime_type, file_size, file_name) VALUES (?, 'wav', 'audio/wav', 1, 'same.wav')`, fileID).Error)
	require.NoError(t, db.Exec(`INSERT INTO track (id, release_id, audio_original_file_id, download_access) VALUES (?, ?, ?, 'public'), (?, ?, ?, 'disabled')`, publicTrackID, releaseID, fileID, disabledTrackID, releaseID, fileID).Error)

	resolver := NewDownloadAccess(db, &auth.SpiceDBClient{}, noOpCriteriaConfigs{})
	result, err := resolver.Resolve(t.Context(), releaseID, "RELEASE_STATUS_PUBLISHED", mediaasset.ContentDownloadOwnerAuthorization{
		ResourceType: "release", ResourceID: releaseID, Status: "RELEASE_STATUS_PUBLISHED",
		Mode: mediaasset.ContentDownloadOwnerAccessPublic,
	}, []releasepublic.TrackDownloadAccessRequest{
		{TrackID: publicTrackID, FileID: fileID},
		{TrackID: disabledTrackID, FileID: fileID},
	}, func(file releasepublic.MediaFile) (*mediaauth.Reference, error) {
		return &mediaauth.Reference{URL: "signed:" + file.ID}, nil
	})
	require.NoError(t, err)
	require.Equal(t, releasepublic.DownloadActionDownload, result[publicTrackID].Access.Action)
	require.Equal(t, "signed:"+fileID, optional.Value(result[publicTrackID].Download).URL)
	require.Equal(t, releasepublic.DownloadActionNone, result[disabledTrackID].Access.Action)
	require.Nil(t, result[disabledTrackID].Download)

	require.NoError(t, db.Exec(`UPDATE release SET status = 'RELEASE_STATUS_DRAFT' WHERE id = ?`, releaseID).Error)
	result, err = resolver.Resolve(t.Context(), releaseID, "RELEASE_STATUS_PUBLISHED", mediaasset.ContentDownloadOwnerAuthorization{
		ResourceType: "release", ResourceID: releaseID, Status: "RELEASE_STATUS_PUBLISHED",
		Mode: mediaasset.ContentDownloadOwnerAccessPublic,
	}, []releasepublic.TrackDownloadAccessRequest{{TrackID: publicTrackID, FileID: fileID}}, func(file releasepublic.MediaFile) (*mediaauth.Reference, error) {
		return &mediaauth.Reference{URL: "stale:" + file.ID}, nil
	})
	require.NoError(t, err)
	require.Equal(t, releasepublic.DownloadActionNone, result[publicTrackID].Access.Action)
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
	}, []releasepublic.TrackDownloadAccessRequest{{TrackID: publicTrackID, FileID: fileID}}, func(file releasepublic.MediaFile) (*mediaauth.Reference, error) {
		return &mediaauth.Reference{URL: "stale:" + file.ID}, nil
	})
	require.NoError(t, err)
	require.Equal(t, releasepublic.DownloadActionNone, result[publicTrackID].Access.Action)
	require.Nil(t, result[publicTrackID].Download)
}
