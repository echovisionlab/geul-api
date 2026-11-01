package filemedia_test

import (
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestClientArtifactRepairNeverReopensRegisteredOrDeletingFile(t *testing.T) {
	for _, state := range []string{"uploading", "missing File", "unregistered File", "registered File", "deleting File", "aborted", "read failure"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			s := filemediatest.NewLibraryStore()
			row := domain.UploadSession{UploadID: uuid.NewString(), FileID: uuid.NewString(), Status: domain.UploadSessionStatusFinalizing}
			if state == "uploading" {
				row.Status = domain.UploadSessionStatusUploading
			}
			if state == "aborted" {
				row.Status = domain.UploadSessionStatusAborted
			}
			file := filemediatest.CatalogFileRecord{Row: domain.CatalogRow{ID: row.FileID}}
			if state == "registered File" {
				file.ClientMediaBundleID = new(uuid.NewString())
			}
			if state == "deleting File" {
				file.PendingDeletion = true
			}
			if state == "read failure" {
				s.Fail = errors.New("receipt read failed")
			}
			if state != "missing File" {
				s.Files[row.FileID] = file
			}
			service := filemedia.ExportSessionService(mutationtest.New[filemedia.Store](s))
			allowed, err := filemedia.ExportArtifactWritable(service, t.Context(), row)
			if state == "read failure" {
				require.ErrorIs(t, err, s.Fail)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, state == "uploading" || state == "missing File" || state == "unregistered File", allowed)
		})
	}
}
