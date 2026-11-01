package filemedia

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestDirectS3UploadTypePolicy(t *testing.T) {
	directTypes := map[managev1.UploadType]struct{}{
		managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE:      {},
		managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE:      {},
		managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO:      {},
		managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO:      {},
		managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT: {},
		managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH:       {},
		managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO:       {},
	}

	for stored := range domain.DefaultUploadConfigs {
		require.Contains(t, managev1.UploadType_value, stored)
		uploadType := managev1.UploadType(managev1.UploadType_value[stored])
		_, expectedDirect := directTypes[uploadType]
		require.Equalf(
			t,
			expectedDirect,
			isDirectS3UploadType(UploadType(uploadType)),
			"unexpected multipart transport for %s",
			uploadType.String(),
		)
	}
	require.False(t, isDirectS3UploadType(UploadUnspecified))
}
