package filemedia_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCompletedFileReceiptRequiresMatchingMetadataAttemptAndTarget(t *testing.T) {
	for _, state := range []string{"matching", "renamed", "size", "MIME", "extension", "slot", "attempt", "pending deletion", "missing File", "missing binding", "target", "upload type", "entity type"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			s := filemediatest.NewLibraryStore()
			row := domain.UploadSession{FileID: uuid.NewString(), FileName: "source.webp", FileSize: 12, DetectedMime: new("image/webp"), SlotID: new(uuid.NewString()), AttemptID: new(uuid.NewString())}
			file := filemediatest.CatalogFileRecord{Row: domain.CatalogRow{ID: row.FileID, Name: "source", Extension: new("webp"), MimeType: new("image/webp"), FileSize: new(int64(12))}, IngestSlotID: row.SlotID, IngestAttemptID: row.AttemptID}
			binding := domain.IngestBinding{FileID: row.FileID, UploadType: "UPLOAD_TYPE_GENERAL_FILE"}
			switch state {
			case "renamed":
				file.Row.Name = "another"
			case "size":
				file.Row.FileSize = new(int64(13))
			case "MIME":
				file.Row.MimeType = new("image/png")
			case "extension":
				file.Row.Extension = new("png")
			case "slot":
				file.IngestSlotID = new(uuid.NewString())
			case "attempt":
				file.IngestAttemptID = nil
			case "pending deletion":
				file.PendingDeletion = true
			case "target":
				binding.EntityID = uuid.NewString()
			case "upload type":
				binding.UploadType = "UPLOAD_TYPE_EDITOR_IMAGE"
			case "entity type":
				binding.EntityType = new("TRANSCODE_ENTITY_TYPE_POST")
			}
			if state != "missing File" {
				s.Files[row.FileID] = file
			}
			if state != "missing binding" {
				s.IngestBindings[row.FileID] = binding
			}
			service := filemedia.ExportSessionService(mutationtest.New[filemedia.Store](s))
			err := filemedia.ExportValidateCompletedFile(service, t.Context(), row, int32(managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE), int32(managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED))
			if state == "matching" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestFinishedUploadReplayRequiresNoSessionAndDeliverableReceipt(t *testing.T) {
	for _, state := range []string{"ready", "initiated", "uploading", "finalizing", "failed", "aborted", "missing File", "missing binding", "pending deletion", "empty", "noncanonical", "unknown type"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			s := filemediatest.NewLibraryStore()
			id := uuid.NewString()
			file := filemediatest.CatalogFileRecord{Row: domain.CatalogRow{ID: id, Name: "source", Extension: new("webp"), MimeType: new("image/webp"), FileSize: new(int64(12))}}
			binding := domain.IngestBinding{FileID: id, UploadType: "UPLOAD_TYPE_GENERAL_FILE"}
			switch state {
			case "pending deletion":
				file.PendingDeletion = true
			case "empty":
				file.Row.FileSize = new(int64(0))
			case "noncanonical":
				file.Row.Name = "source.webp"
			case "unknown type":
				binding.UploadType = "unknown"
			case "initiated", "uploading", "finalizing", "failed", "aborted":
				upload := uuid.NewString()
				s.UploadSessions[upload] = domain.UploadSession{UploadID: upload, FileID: id, Status: domain.UploadSessionStatus(state)}
			}
			if state != "missing File" {
				s.Files[id] = file
			}
			if state != "missing binding" {
				s.IngestBindings[id] = binding
			}
			service := filemedia.ExportSessionService(mutationtest.New[filemedia.Store](s))
			got, typ, err := filemedia.ExportLoadFinishedFile(service, t.Context(), id)
			switch state {
			case "ready":
				require.NoError(t, err)
				require.Equal(t, id, got.ID)
				require.Equal(t, int32(managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE), typ)
			case "pending deletion", "empty", "noncanonical", "unknown type":
				require.Error(t, err)
				var outcome *domainerr.Error
				require.ErrorAs(t, err, &outcome)
				require.Equal(t, domainerr.FailedPrecondition, outcome.Kind)
			default:
				require.Error(t, err)
				var outcome *domainerr.Error
				require.ErrorAs(t, err, &outcome)
				require.Equal(t, domainerr.NotFound, outcome.Kind)
			}
		})
	}
}
