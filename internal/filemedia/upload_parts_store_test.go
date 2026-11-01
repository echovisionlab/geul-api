package filemedia_test

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestUploadPartRecordingRejectsTerminalSessionsAndRollsBackInvalidReceipt(t *testing.T) {
	for _, state := range []string{"uploading", "finalizing", "failed", "aborted", "invalid receipt"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			s := filemediatest.NewLibraryStore()
			now := time.Now().UTC()
			row := domain.UploadSession{UploadID: uuid.NewString(), FileID: uuid.NewString(), Status: domain.UploadSessionStatus(state), LastActivityAt: now.Add(-time.Hour)}
			part := domain.UploadPart{UploadID: row.UploadID, PartNumber: 1, ETag: "receipt", Size: 6, CreatedAt: now, UpdatedAt: now}
			if state == "invalid receipt" {
				row.Status = domain.UploadSessionStatusInitiated
				part.PartNumber = 0
			}
			s.UploadSessions[row.UploadID] = row
			service := filemedia.ExportSessionService(mutationtest.New[filemedia.Store](s))
			changed, err := filemedia.ExportRecordUploadedPart(service, t.Context(), row, part)
			if state == "invalid receipt" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			if state == "uploading" {
				require.True(t, changed)
				require.Equal(t, []domain.UploadPart{part}, s.UploadPartRows[row.UploadID])
				require.Equal(t, now, s.UploadSessions[row.UploadID].LastActivityAt)
			} else {
				require.False(t, changed)
				require.Empty(t, s.UploadPartRows[row.UploadID])
				require.Equal(t, row, s.UploadSessions[row.UploadID])
			}
		})
	}
}

func TestUploadMIMEDetectionRetainsFirstResultAndChecksCurrentState(t *testing.T) {
	for _, state := range []string{"empty", "same", "conflict", "finalizing", "missing"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			s := filemediatest.NewLibraryStore()
			row := domain.UploadSession{UploadID: uuid.NewString(), FileID: uuid.NewString(), Status: domain.UploadSessionStatusUploading}
			if state == "same" || state == "finalizing" {
				row.DetectedMime = new("image/webp")
			}
			if state == "conflict" {
				row.DetectedMime = new("image/png")
			}
			if state == "finalizing" {
				row.Status = domain.UploadSessionStatusFinalizing
			}
			if state != "missing" {
				s.UploadSessions[row.UploadID] = row
			}
			service := filemedia.ExportSessionService(mutationtest.New[filemedia.Store](s))
			err := filemedia.ExportRecordUploadMIME(service, t.Context(), row, "image/webp", time.Now())
			if state == "empty" || state == "same" {
				require.NoError(t, err)
				require.Equal(t, new("image/webp"), s.UploadSessions[row.UploadID].DetectedMime)
			} else {
				require.Error(t, err)
				if state != "missing" {
					require.Equal(t, row, s.UploadSessions[row.UploadID])
				}
			}
		})
	}
}

func TestUploadPartReconciliationRejectsSessionDriftAndRestoresFailedReplacement(t *testing.T) {
	for _, state := range []string{"unchanged", "shape changed", "finalizing", "invalid receipt"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			s := filemediatest.NewLibraryStore()
			row := domain.UploadSession{UploadID: uuid.NewString(), FileID: uuid.NewString(), Status: domain.UploadSessionStatusUploading, FileSize: 6}
			expected := row
			if state == "shape changed" {
				row.FileSize++
			}
			if state == "finalizing" {
				row.Status = domain.UploadSessionStatusFinalizing
			}
			s.UploadSessions[row.UploadID] = row
			old := domain.UploadPart{UploadID: row.UploadID, PartNumber: 2, ETag: "stale", Size: 6}
			s.UploadPartRows[row.UploadID] = []domain.UploadPart{old}
			part := domain.UploadPart{UploadID: row.UploadID, PartNumber: 1, ETag: "remote", Size: 6}
			if state == "invalid receipt" {
				part.Size = -1
			}
			service := filemedia.ExportSessionService(mutationtest.New[filemedia.Store](s))
			err := filemedia.ExportReconcileUploadParts(service, t.Context(), expected, []domain.UploadPart{part})
			if state == "unchanged" {
				require.NoError(t, err)
				require.Len(t, s.UploadPartRows[row.UploadID], 1)
				require.Equal(t, "remote", s.UploadPartRows[row.UploadID][0].ETag)
				require.False(t, s.UploadSessions[row.UploadID].LastActivityAt.IsZero())
			} else {
				require.Error(t, err)
				require.Equal(t, row, s.UploadSessions[row.UploadID])
				require.Equal(t, []domain.UploadPart{old}, s.UploadPartRows[row.UploadID])
			}
		})
	}
}
