package handler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/transcoding/ffmpeg"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type progressReporter func(stage apiv1.TranscodeStage, percent int)

type progressState struct {
	mu           sync.Mutex
	sequence     int64
	lastSentAt   time.Time
	lastProgress int
	lastStage    apiv1.TranscodeStage
}

type progressPublisher struct {
	publisher EventPublisher
}

func (p progressPublisher) newReporter(
	ctx context.Context,
	jobID string,
	entityType apiv1.TranscodeEntityType,
	entityID, fileID string,
) progressReporter {
	state := &progressState{}
	return func(stage apiv1.TranscodeStage, percent int) {
		state.publish(ctx, p.publisher, progressIdentity{
			jobID:      jobID,
			entityType: entityType,
			entityID:   entityID,
			fileID:     fileID,
		}, stage, percent)
	}
}

type progressIdentity struct {
	jobID      string
	entityType apiv1.TranscodeEntityType
	entityID   string
	fileID     string
}

func (s *progressState) publish(
	ctx context.Context,
	publisher EventPublisher,
	identity progressIdentity,
	stage apiv1.TranscodeStage,
	percent int,
) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	stageChanged := stage != s.lastStage
	boundary := percent == 0 || percent == 100
	if !stageChanged && !boundary && now.Sub(s.lastSentAt) < 500*time.Millisecond {
		return
	}
	if !stageChanged && !boundary && percent <= s.lastProgress {
		return
	}

	s.sequence++
	s.lastSentAt = now
	s.lastProgress = percent
	s.lastStage = stage
	if err := publisher.PublishProgress(ctx, &apiv1.TranscodeProgressEvent{
		EventId:        identity.jobID,
		EntityType:     identity.entityType,
		EntityId:       identity.entityID,
		FileId:         identity.fileID,
		SequenceNumber: s.sequence,
		Progress:       int32(percent),
		Stage:          &stage,
		TimestampMs:    now.UnixMilli(),
	}); err != nil {
		slog.Debug("Failed to publish progress event", "job_id", identity.jobID, "error", err)
	}
}

func videoResolutionsToFFmpeg(resolutions []apiv1.VideoResolution) []ffmpeg.HLSResolution {
	result := make([]ffmpeg.HLSResolution, 0, len(resolutions))
	for _, resolution := range resolutions {
		name := videoResolutionPresetName(resolution)
		if preset, found := ffmpeg.HLSResolutionPreset(name); found {
			result = append(result, preset)
		}
	}
	return result
}

func videoResolutionPresetName(resolution apiv1.VideoResolution) string {
	switch resolution {
	case apiv1.VideoResolution_VIDEO_RESOLUTION_360P:
		return "360p"
	case apiv1.VideoResolution_VIDEO_RESOLUTION_480P:
		return "480p"
	case apiv1.VideoResolution_VIDEO_RESOLUTION_720P:
		return "720p"
	case apiv1.VideoResolution_VIDEO_RESOLUTION_1080P:
		return "1080p"
	default:
		return ""
	}
}

func ptrInt32(value int32) *int32 {
	return &value
}
