package filemedia_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRemoteImportRetryRequiresSamePersistedOperationAndTarget(t *testing.T) {
	for _, state := range []string{"matching", "missing File", "read failure", "missing binding", "attempt", "slot", "pending deletion", "metadata", "target", "upload type", "entity type"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			s := filemediatest.NewLibraryStore()
			id, slot, attempt, target := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
			file := filemediatest.CatalogFileRecord{Row: domain.CatalogRow{ID: id, Name: "source", Extension: new("webp"), MimeType: new("image/webp"), FileSize: new(int64(64))}, SHA256: bytes.Repeat([]byte{7}, 32), IngestSlotID: &slot, IngestAttemptID: &attempt}
			binding := domain.IngestBinding{FileID: id, UploadType: "UPLOAD_TYPE_TRACK_AUDIO", EntityType: new("TRANSCODE_ENTITY_TYPE_TRACK"), EntityID: target}
			switch state {
			case "read failure":
				s.Fail = errors.New("receipt read failed")
			case "attempt":
				file.IngestAttemptID = new(uuid.NewString())
			case "slot":
				file.IngestSlotID = nil
			case "pending deletion":
				file.PendingDeletion = true
			case "metadata":
				file.SHA256 = nil
			case "target":
				binding.EntityID = uuid.NewString()
			case "upload type":
				binding.UploadType = "UPLOAD_TYPE_GENERAL_FILE"
			case "entity type":
				binding.EntityType = nil
			}
			if state != "missing File" {
				s.Files[id] = file
			}
			if state != "missing binding" {
				s.IngestBindings[id] = binding
			}
			service := filemedia.ExportSessionService(mutationtest.New[filemedia.Store](s))
			got, found, err := filemedia.ExportRestoreRemoteReceipt(service, t.Context(), id, slot, attempt, target, int32(managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO), int32(managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK))
			switch state {
			case "matching":
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, id, got.ID)
			case "missing File":
				require.NoError(t, err)
				require.False(t, found)
			case "read failure":
				require.ErrorIs(t, err, s.Fail)
				require.False(t, found)
			default:
				require.Error(t, err)
				require.True(t, found)
			}
		})
	}
}
