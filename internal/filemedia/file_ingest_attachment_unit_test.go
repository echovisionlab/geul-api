package filemedia

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestEditorFileIngestHasNoDocumentAttachmentTarget(t *testing.T) {
	t.Parallel()
	for _, uploadType := range []managev1.UploadType{
		managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH,
	} {
		target, err := normalizeFileIngestProjectionIdentity(UploadType(uploadType), EntityUnspecified,

			"",
			nil,
		)
		require.NoError(t, err)
		require.Equal(t, fileIngestTargetModeEditorFile, target.mode)
		require.False(t, target.requiresDurableAttachment())

		_, err = normalizeFileIngestProjectionIdentity(UploadType(uploadType), EntityPost,

			"",
			nil,
		)
		require.ErrorContains(t, err, "must omit document entity type")
		_, err = normalizeFileIngestProjectionIdentity(UploadType(uploadType), EntityUnspecified,

			"block-or-slot",
			nil,
		)
		require.ErrorContains(t, err, "independent of document block attachment")
		expected := uuid.NewString()
		_, err = normalizeFileIngestProjectionIdentity(UploadType(uploadType), EntityUnspecified,

			"",
			&expected,
		)
		require.ErrorContains(t, err, "independent of document block attachment")
	}
}

func TestStoredEditorSessionRejectsLegacyDocumentTarget(t *testing.T) {
	t.Parallel()
	entityType := managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST.String()
	_, err := fileIngestTargetFromStoredSession(domain.UploadSession{
		UploadType: managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE.String(),
		EntityType: &entityType,
		EntityID:   uuid.NewString(),
	})
	require.ErrorContains(t, err, "must omit document entity type")
}
