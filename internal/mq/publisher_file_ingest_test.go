package mq

import (
	"testing"

	"github.com/stretchr/testify/require"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestFileIngestAttachedMessageIDUsesStableIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		event *managev1.FileIngestAttachedEvent
		want  string
	}{
		{
			name: "correlation id",
			event: &managev1.FileIngestAttachedEvent{
				CorrelationId:  "correlation-1",
				SequenceNumber: 4,
				Identity: &managev1.FileIngestIdentity{
					EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
					MediaKind:  managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_TRACK_AUDIO,
					FileId:     "file-1",
				},
			},
			want: "file-ingest-attached:file-1",
		},
		{
			name: "attempt id fallback",
			event: &managev1.FileIngestAttachedEvent{
				SequenceNumber: 2,
				Identity: &managev1.FileIngestIdentity{
					EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
					MediaKind:  managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_TRACK_AUDIO,
					AttemptId:  new("attempt-1"),
					FileId:     "file-1",
				},
			},
			want: "file-ingest-attached:file-1",
		},
		{
			name: "file id fallback",
			event: &managev1.FileIngestAttachedEvent{
				SequenceNumber: 1,
				Identity: &managev1.FileIngestIdentity{
					EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
					MediaKind:  managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_TRACK_AUDIO,
					FileId:     "file-1",
				},
			},
			want: "file-ingest-attached:file-1",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := FileIngestAttachedMessageID(test.event)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestFileIngestAttachedMessageIDRejectsMissingIdentity(t *testing.T) {
	t.Parallel()

	_, err := FileIngestAttachedMessageID(&managev1.FileIngestAttachedEvent{})
	require.Error(t, err)

	_, err = FileIngestAttachedMessageID(&managev1.FileIngestAttachedEvent{
		Identity: &managev1.FileIngestIdentity{},
	})
	require.Error(t, err)

	_, err = FileIngestAttachedMessageID(&managev1.FileIngestAttachedEvent{
		Identity: &managev1.FileIngestIdentity{
			EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
			MediaKind:  managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_IMAGE,
			FileId:     "file-1",
		},
	})
	require.Error(t, err)
}
