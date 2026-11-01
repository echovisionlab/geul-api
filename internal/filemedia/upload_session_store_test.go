package filemedia_test

import (
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestUploadSessionDeletionFailurePreservesRetry(t *testing.T) {
	for _, status := range []domain.UploadSessionStatus{domain.UploadSessionStatusFinalizing, domain.UploadSessionStatusAborted} {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			s := filemediatest.NewLibraryStore()
			upload := uuid.NewString()
			s.UploadSessions[upload] = domain.UploadSession{UploadID: upload, Status: status}
			service := filemedia.ExportSessionService(mutationtest.New[filemedia.Store](s))
			remove := filemedia.ExportDeleteCompletedUploadSession
			if status == domain.UploadSessionStatusAborted {
				remove = filemedia.ExportDeleteAbortedUploadSession
			}
			failure := errors.New("delete failed")
			s.Fail = failure
			require.ErrorIs(t, remove(service, t.Context(), upload), failure)
			require.Contains(t, s.UploadSessions, upload)
			s.Fail = nil
			require.NoError(t, remove(service, t.Context(), upload))
			require.NotContains(t, s.UploadSessions, upload)
			require.Error(t, remove(service, t.Context(), upload))
		})
	}
}
func TestMultipartCompletionClaimValidatesLockedSessionAndSortedPartShape(t *testing.T) {
	for _, state := range []string{"uploading", "finalizing", "failed", "aborted", "missing", "wrong file", "count", "sequence", "size", "etag", "read failed"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			s := filemediatest.NewLibraryStore()
			upload, file := uuid.NewString(), uuid.NewString()
			row := domain.UploadSession{UploadID: upload, FileID: file, FileSize: 10, ChunkSize: 6, TotalParts: 2, Status: domain.UploadSessionStatusUploading}
			parts := []domain.UploadPart{{UploadID: upload, PartNumber: 2, ETag: "second", Size: 4}, {UploadID: upload, PartNumber: 1, ETag: "first", Size: 6}}
			switch state {
			case "finalizing":
				row.Status = domain.UploadSessionStatusFinalizing
			case "failed":
				row.Status = domain.UploadSessionStatusFailed
			case "aborted":
				row.Status = domain.UploadSessionStatusAborted
			case "count":
				parts = parts[:1]
			case "sequence":
				parts[0].PartNumber = 3
			case "size":
				parts[0].Size = 5
			case "etag":
				parts[0].ETag = " "
			}
			if state != "missing" {
				s.UploadSessions[upload] = row
			}
			s.UploadPartRows[upload] = parts
			if state == "wrong file" {
				file = uuid.NewString()
			}
			if state == "read failed" {
				s.Fail = errors.New("read failed")
			}
			service := filemedia.ExportSessionService(mutationtest.New[filemedia.Store](s))
			now := time.Now().UTC()
			claimed, loaded, err := filemedia.ExportClaimMultipartCompletion(service, t.Context(), upload, file, now)
			switch state {
			case "uploading", "finalizing":
				require.NoError(t, err)
				require.True(t, claimed)
				require.Len(t, loaded, 2)
				require.Equal(t, int32(1), loaded[0].PartNumber)
				require.Equal(t, "first", loaded[0].ETag)
				require.Equal(t, domain.UploadSessionStatusFinalizing, s.UploadSessions[upload].Status)
				require.True(t, now.Equal(s.UploadSessions[upload].LastActivityAt))
			case "failed", "aborted", "missing", "wrong file":
				require.NoError(t, err)
				require.False(t, claimed)
				require.Nil(t, loaded)
			default:
				require.Error(t, err)
				require.False(t, claimed)
				require.Nil(t, loaded)
				require.Equal(t, row, s.UploadSessions[upload])
			}
		})
	}
}
func TestMultipartPartFailureKeepsOnlyWritableSessionsResumable(t *testing.T) {
	for _, status := range []domain.UploadSessionStatus{domain.UploadSessionStatusInitiated, domain.UploadSessionStatusUploading, domain.UploadSessionStatusFinalizing, domain.UploadSessionStatusAborted} {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			s := filemediatest.NewLibraryStore()
			upload, file := uuid.NewString(), uuid.NewString()
			before := time.Now().UTC().Add(-time.Hour)
			row := domain.UploadSession{UploadID: upload, FileID: file, Status: status, DetectedMime: new("image/webp"), LastActivityAt: before, UpdatedAt: before}
			s.UploadSessions[upload] = row
			service := filemedia.ExportSessionService(mutationtest.New[filemedia.Store](s))
			now := time.Now().UTC()
			claimed, err := filemedia.ExportClaimUploadPartActivity(service, t.Context(), upload, file, now)
			require.NoError(t, err)
			writable := status == domain.UploadSessionStatusInitiated || status == domain.UploadSessionStatusUploading
			require.Equal(t, writable, claimed)
			require.NoError(t, filemedia.ExportRetryableMultipartFailure(service, t.Context(), upload, file, now))
			got := s.UploadSessions[upload]
			require.Equal(t, status, got.Status)
			require.Equal(t, row.DetectedMime, got.DetectedMime)
			if writable {
				require.True(t, now.Equal(got.LastActivityAt))
			} else {
				require.Equal(t, row, got)
			}
		})
	}
}
func TestUploadAbortClaimCannotReplaceCompletionAuthority(t *testing.T) {
	s := filemediatest.NewLibraryStore()
	upload, file := uuid.NewString(), uuid.NewString()
	row := domain.UploadSession{UploadID: upload, FileID: file, Status: domain.UploadSessionStatusFinalizing}
	s.UploadSessions[upload] = row
	service := filemedia.ExportSessionService(mutationtest.New[filemedia.Store](s))
	claimed, err := filemedia.ExportClaimUploadSessionAbort(service, t.Context(), upload, file, time.Now())
	require.NoError(t, err)
	require.False(t, claimed)
	require.Equal(t, row, s.UploadSessions[upload])
}
