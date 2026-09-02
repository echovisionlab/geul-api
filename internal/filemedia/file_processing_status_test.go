package filemedia

import (
	"testing"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func TestResolveFileProcessingSnapshot(t *testing.T) {
	t.Run("audio stays processing without terminal derivative failures", func(t *testing.T) {
		snapshot := resolveFileProcessingSnapshot("audio/flac", true, false, true, 0, 0)
		if snapshot.Status != commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_PROCESSING {
			t.Fatalf("expected processing status, got %s", snapshot.Status.String())
		}
		if snapshot.Percentage == nil || *snapshot.Percentage != 67 {
			t.Fatalf("expected 67%% processing percentage, got %v", snapshot.Percentage)
		}
	})

	t.Run("audio fails when a terminal waveform failure exists and derivative is still missing", func(t *testing.T) {
		snapshot := resolveFileProcessingSnapshot("audio/flac", true, false, true, 0, 1)
		if snapshot.Status != commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_FAILED {
			t.Fatalf("expected audio snapshot to become terminal failed")
		}
	})

	t.Run("video fails when a terminal hls failure exists and hls is still missing", func(t *testing.T) {
		snapshot := resolveFileProcessingSnapshot("video/mp4", false, false, false, 1, 0)
		if snapshot.Status != commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_FAILED {
			t.Fatalf("expected video snapshot to become terminal failed")
		}
	})

	t.Run("non-processing files are ready once the durable file exists", func(t *testing.T) {
		snapshot := resolveFileProcessingSnapshot("application/pdf", false, false, false, 99, 99)
		if snapshot.Status != commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY {
			t.Fatalf("expected ready status, got %s", snapshot.Status.String())
		}
		if snapshot.Percentage != nil {
			t.Fatalf("expected nil processing percentage, got %v", snapshot.Percentage)
		}
	})
}
