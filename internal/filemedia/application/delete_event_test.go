package application

import (
	"testing"

	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestValidateDeleteEventAcceptsCanonicalFileAndGenerationTargets(t *testing.T) {
	t.Parallel()
	fileID := uuid.NewString()
	generationID := uuid.NewString()
	originalKey, err := mediaauth.MediaObjectKey(fileID, "mp4")
	require.NoError(t, err)
	generationPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, generationID)
	require.NoError(t, err)

	require.NoError(t, ValidateDeleteEvent(&managev1.FileDeleteEvent{
		FileId: fileID,
		Original: &commonv1.MediaObjectTarget{
			FileId: fileID, ObjectKey: originalKey, Extension: "mp4", MimeType: "video/mp4",
		},
		Generations: []*commonv1.MediaGenerationWriteTarget{{
			GenerationId: generationID, FileId: fileID, ObjectPrefix: generationPrefix,
		}},
	}))
}

func TestValidateDeleteEventRejectsNonCanonicalAndPublicAssetTargets(t *testing.T) {
	t.Parallel()
	fileID := uuid.NewString()
	originalKey, err := mediaauth.MediaObjectKey(fileID, "mp4")
	require.NoError(t, err)
	base := &managev1.FileDeleteEvent{
		FileId: fileID,
		Original: &commonv1.MediaObjectTarget{
			FileId: fileID, ObjectKey: originalKey, Extension: "mp4", MimeType: "video/mp4",
		},
	}

	invalidOriginal := protoCloneDeleteEvent(t, base)
	invalidOriginal.Original.ObjectKey = "other/key"
	require.ErrorContains(t, ValidateDeleteEvent(invalidOriginal), "non-canonical original")

	withAsset := protoCloneDeleteEvent(t, base)
	withAsset.Assets = []*commonv1.AssetWriteTarget{{AssetId: uuid.NewString()}}
	require.ErrorContains(t, ValidateDeleteEvent(withAsset), "must not include public assets")
}

func protoCloneDeleteEvent(t *testing.T, event *managev1.FileDeleteEvent) *managev1.FileDeleteEvent {
	t.Helper()
	return &managev1.FileDeleteEvent{
		FileId: event.GetFileId(),
		Original: &commonv1.MediaObjectTarget{
			FileId:    event.GetOriginal().GetFileId(),
			ObjectKey: event.GetOriginal().GetObjectKey(),
			Extension: event.GetOriginal().GetExtension(),
			MimeType:  event.GetOriginal().GetMimeType(),
		},
	}
}
