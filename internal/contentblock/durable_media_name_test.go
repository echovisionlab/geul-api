package contentblock

import (
	"testing"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestStorageAdapterRejectsRemovedDurableMediaTitleAcrossRichTextProfiles(t *testing.T) {
	t.Parallel()

	fileID := uuid.NewString()
	shared := []byte(`{"file":{"props":{"attachment":{"activeFileId":"` + fileID + `"},"name":"source.wav","title":"removed"}}}`)
	for _, profile := range []string{"post", "work", "program_event"} {
		profile := profile
		t.Run(profile, func(t *testing.T) {
			t.Parallel()
			_, err := batchFromStorage(uuid.New(), profile, contentv1.ContentStorageMutationBatch{
				ExpectedRevision: uuid.NewString(),
				BaseUpserts: []contentv1.ContentStorageRow{{
					BlockID:       uuid.NewString(),
					ContainerSlot: "content",
					Kind:          "file",
					SharedData:    shared,
				}},
			})
			require.Error(t, err)
			require.ErrorIs(t, err, ErrInvalidMutation)
			require.Contains(t, err.Error(), "title")
		})
	}
}

func TestStorageAdapterAcceptsSharedMediaName(t *testing.T) {
	t.Parallel()

	fileID := uuid.NewString()
	_, err := batchFromStorage(uuid.New(), "post", contentv1.ContentStorageMutationBatch{
		ExpectedRevision: uuid.NewString(),
		BaseUpserts: []contentv1.ContentStorageRow{{
			BlockID:       uuid.NewString(),
			ContainerSlot: "content",
			Kind:          "file",
			SharedData: []byte(`{"file":{"props":{"attachment":{"activeFileId":"` + fileID +
				`"},"name":"source.wav"}}}`),
		}},
	})
	require.NoError(t, err)
}
