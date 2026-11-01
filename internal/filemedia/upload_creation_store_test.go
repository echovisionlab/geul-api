package filemedia_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTrackUploadCreationRequiresExistingTrack(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "existing"}[exists], func(t *testing.T) {
			t.Parallel()
			s := filemediatest.NewLibraryStore()
			track := uuid.NewString()
			s.UploadTracks = map[string]bool{track: exists}
			row := domain.UploadSession{UploadID: uuid.NewString(), FileID: uuid.NewString(), UploadType: "UPLOAD_TYPE_TRACK_AUDIO", EntityID: track, EntityType: new("TRANSCODE_ENTITY_TYPE_TRACK"), Status: domain.UploadSessionStatusInitiated}
			service := filemedia.ExportSessionService(mutationtest.New[filemedia.Store](s))
			_, err := filemedia.ExportCreateUploadSession(service, t.Context(), row)
			if exists {
				require.NoError(t, err)
				require.Contains(t, s.UploadSessions, row.UploadID)
			} else {
				require.Error(t, err)
				require.Empty(t, s.UploadSessions)
			}
		})
	}
}

func TestTrackCleanupClaimsAllSessionsOnlyWhenCompletionIsAbsent(t *testing.T) {
	for _, state := range []string{"abortable", "finalizing", "missing track"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			s := filemediatest.NewLibraryStore()
			track := uuid.NewString()
			s.UploadTracks = map[string]bool{track: state != "missing track"}
			row := domain.UploadSession{UploadID: uuid.NewString(), FileID: uuid.NewString(), UploadType: "UPLOAD_TYPE_TRACK_AUDIO", EntityID: track, Status: domain.UploadSessionStatusUploading}
			s.UploadSessions[row.UploadID] = row
			peer := row
			peer.UploadID = uuid.NewString()
			if state == "finalizing" {
				peer.Status = domain.UploadSessionStatusFinalizing
			} else {
				peer.Status = domain.UploadSessionStatusFailed
			}
			s.UploadSessions[peer.UploadID] = peer
			service := filemedia.ExportSessionService(mutationtest.New[filemedia.Store](s))
			rows, err := filemedia.ExportClaimTrackUploadCleanup(service, t.Context(), track)
			if state == "abortable" {
				require.NoError(t, err)
				require.Len(t, rows, 2)
				for _, r := range s.UploadSessions {
					require.Equal(t, domain.UploadSessionStatusAborted, r.Status)
				}
			} else {
				require.Error(t, err)
				if state == "finalizing" {
					require.ErrorIs(t, err, mediaasset.ErrUploadSessionNotAbortable)
				}
				require.Equal(t, row, s.UploadSessions[row.UploadID])
				require.Equal(t, peer, s.UploadSessions[peer.UploadID])
			}
		})
	}
}

func TestTrackDeletionChecksSessionsInJoinedStore(t *testing.T) {
	s := filemediatest.NewLibraryStore()
	track := uuid.NewString()
	row := domain.UploadSession{UploadID: uuid.NewString(), UploadType: "UPLOAD_TYPE_TRACK_AUDIO", EntityID: track, Status: domain.UploadSessionStatusFinalizing}
	s.UploadSessions[row.UploadID] = row
	service := filemedia.ExportSessionService(mutationtest.New[filemedia.Store](s))
	require.ErrorIs(t, service.RequireNoTrackUploadSessions(t.Context(), filemedia.Joined{Store: s}, track), filemedia.ErrTrackUploadSessionsChanged)
	require.NoError(t, service.RequireNoTrackUploadSessions(t.Context(), filemedia.Joined{Store: s}, uuid.NewString()))
}
