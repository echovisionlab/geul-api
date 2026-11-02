package application

import (
	"context"
	"strings"
	"time"

	transcodestate "github.com/echovisionlab/geul-api/internal/transcode"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

func (h *Handlers) upsertWaveformJob(ctx context.Context, event *managev1.WaveformGenerateEvent) error {
	if err := transcodestate.ValidateWaveformGenerateEvent(event); err != nil {
		return err
	}
	now := time.Now()
	return h.transaction(ctx, func(store Store) error {
		return store.SaveWaveformJob(ctx, transcodestate.WaveformJob{
			EventID:    event.EventId,
			EntityType: event.EntityType.String(),
			EntityID:   event.EntityId,
			FileID:     event.FileId,
			Status:     transcodestate.WaveformJobStatusQueued,
			CreatedAt:  now,
			UpdatedAt:  now,
		})
	})
}

func (h *Handlers) getWaveformJob(ctx context.Context, eventID string) (*transcodestate.WaveformJob, error) {
	var job *transcodestate.WaveformJob
	err := h.read(ctx, func(store Store) (err error) {
		job, err = store.WaveformJob(ctx, eventID)
		return err
	})
	return job, err
}

func (h *Handlers) markWaveformJobCompleted(ctx context.Context, eventID string) error {
	return h.transaction(ctx, func(store Store) error { return store.DeleteWaveformJob(ctx, eventID) })
}

func (h *Handlers) finishDeletedFileWaveformJob(ctx context.Context, eventID, fileID string) error {
	eventID = strings.TrimSpace(eventID)
	fileID = strings.TrimSpace(fileID)
	if eventID == "" || fileID == "" {
		return nil
	}
	return h.transaction(ctx, func(store Store) error { return store.DeleteFileWaveformJob(ctx, eventID, fileID) })
}

func (h *Handlers) markWaveformJobFailed(ctx context.Context, eventID string, errText string) error {
	return h.transaction(ctx, func(store Store) error { return store.FailWaveformJob(ctx, eventID, errText, time.Now()) })
}

func (h *Handlers) markWaveformJobCancelled(ctx context.Context, eventID string, errText string) error {
	return h.transaction(ctx, func(store Store) error { return store.CancelWaveformJob(ctx, eventID, errText, time.Now()) })
}

func (h *Handlers) shouldEnqueueWaveformJob(ctx context.Context, fileID string) (bool, error) {
	acceptsResults, err := h.fileAcceptsProcessingResults(ctx, fileID)
	if err != nil || !acceptsResults {
		return false, err
	}
	shouldEnqueue := false
	err = h.read(ctx, func(store Store) error {
		derivativeCount, err := store.DerivativeCount(ctx, fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM.String())
		if err != nil || derivativeCount > 0 {
			return err
		}
		queuedCount, err := store.QueuedWaveformJobs(ctx, fileID)
		if err != nil || queuedCount > 0 {
			return err
		}
		failedCount, err := store.FailedWaveformJobs(ctx, fileID)
		shouldEnqueue = failedCount == 0
		return err
	})
	return shouldEnqueue, err
}

func (h *Handlers) refreshTrackAudioProcessingStatus(ctx context.Context, trackID string, fileID string) error {
	return h.transaction(ctx, func(store Store) error {
		status, err := determineTrackAudioProcessingStatus(ctx, store, fileID)
		if err != nil {
			return err
		}
		_, err = store.UpdateTrackProcessing(ctx, trackID, fileID, status, nil)
		return err
	})
}

func determineTrackAudioProcessingStatus(ctx context.Context, store Store, fileID string) (string, error) {
	hasHLS, hasWaveform, hasSpectrogram, err := loadAudioDerivativePresence(ctx, store, fileID)
	if err != nil {
		return "", err
	}
	if hasHLS && hasWaveform && hasSpectrogram {
		return TrackCompleted, nil
	}

	transcodeFailures, err := store.FailedTranscodeJobs(ctx, fileID, eventpkg.QueueTranscoderAudio)
	if err != nil {
		return "", err
	}
	waveformFailures, err := store.FailedWaveformJobs(ctx, fileID)
	if err != nil {
		return "", err
	}

	if ((!hasHLS || !hasSpectrogram) && transcodeFailures > 0) ||
		(!hasWaveform && waveformFailures > 0) {
		return TrackFailed, nil
	}

	return TrackProcessing, nil
}

func loadAudioDerivativePresence(ctx context.Context, store Store, fileID string) (bool, bool, bool, error) {
	derivativeTypes, err := store.DerivativeTypes(ctx, fileID, []string{
		managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String(),
		managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM.String(),
		managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM.String(),
	})
	if err != nil {
		return false, false, false, err
	}

	var hasHLS bool
	var hasWaveform bool
	var hasSpectrogram bool
	for _, derivativeType := range derivativeTypes {
		switch derivativeType {
		case managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String():
			hasHLS = true
		case managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM.String():
			hasWaveform = true
		case managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM.String():
			hasSpectrogram = true
		}
	}

	return hasHLS, hasWaveform, hasSpectrogram, nil
}
