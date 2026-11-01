package filemedia

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
)

type fileProcessingSnapshot struct {
	Status     ProcessingStatus
	Percentage *int32
}

func resolveFileProcessingSnapshot(
	mimeType string,
	hasHLS bool,
	hasWaveform bool,
	hasSpectrogram bool,
	transcodeFailures int64,
	waveformFailures int64,
) fileProcessingSnapshot {
	normalizedMime := normalizeMimeType(mimeType)
	if strings.HasPrefix(normalizedMime, "video/") {
		return resolveVideoProcessingSnapshot(hasHLS, transcodeFailures)
	}
	if strings.HasPrefix(normalizedMime, "audio/") {
		return resolveAudioProcessingSnapshot(
			hasHLS, hasWaveform, hasSpectrogram, transcodeFailures, waveformFailures,
		)
	}
	return fileProcessingSnapshot{
		Status: ProcessingReady,
	}
}

func resolveVideoProcessingSnapshot(hasHLS bool, transcodeFailures int64) fileProcessingSnapshot {
	if hasHLS {
		return fileProcessingSnapshot{Status: ProcessingReady}
	}
	if transcodeFailures > 0 {
		return fileProcessingSnapshot{Status: ProcessingFailed}
	}
	return fileProcessingSnapshot{
		Status: ProcessingActive, Percentage: new(int32(0)),
	}
}

func resolveAudioProcessingSnapshot(
	hasHLS bool,
	hasWaveform bool,
	hasSpectrogram bool,
	transcodeFailures int64,
	waveformFailures int64,
) fileProcessingSnapshot {
	completed := countCompletedAudioDerivatives(hasHLS, hasWaveform, hasSpectrogram)
	if completed == 3 {
		return fileProcessingSnapshot{Status: ProcessingReady}
	}
	transcodeFailed := (!hasHLS || !hasSpectrogram) && transcodeFailures > 0
	waveformFailed := !hasWaveform && waveformFailures > 0
	if transcodeFailed || waveformFailed {
		return fileProcessingSnapshot{Status: ProcessingFailed}
	}
	percentage := int32(0)
	if completed > 0 {
		percentage = (completed*100 + 1) / 3
	}
	return fileProcessingSnapshot{
		Status: ProcessingActive, Percentage: new(percentage),
	}
}

func countCompletedAudioDerivatives(hasHLS, hasWaveform, hasSpectrogram bool) int32 {
	var completed int32
	for _, ready := range []bool{hasHLS, hasWaveform, hasSpectrogram} {
		if ready {
			completed++
		}
	}
	return completed
}

func (s *Service) populateFileProcessingStatus(
	ctx context.Context,
	responses map[string]*Delivery,
) error {
	return s.populateFileProcessingStatusWithReader(ctx, s.units, responses)
}
func (s *Service) populateFileProcessingStatusWithReader(ctx context.Context, reader fileStoreReader, responses map[string]*Delivery) error {
	if len(responses) == 0 {
		return nil
	}

	audioIDs := make([]string, 0, len(responses))
	videoIDs := make([]string, 0, len(responses))
	for fileID, response := range responses {
		if response == nil {
			continue
		}
		switch {
		case strings.HasPrefix(normalizeMimeType(response.MimeType), "audio/"):
			audioIDs = append(audioIDs, fileID)
		case strings.HasPrefix(normalizeMimeType(response.MimeType), "video/"):
			videoIDs = append(videoIDs, fileID)
		}
	}

	var failures domain.ProcessingFailureCounts
	if len(audioIDs)+len(videoIDs) > 0 {
		if err := reader.Read(ctx, func(store Store) error {
			var err error
			failures, err = store.DeliveryProcessingFailures(ctx, audioIDs, videoIDs)
			return err
		}); err != nil {
			return err
		}

	}
	for fileID, response := range responses {
		if response == nil {
			continue
		}
		snapshot := resolveFileProcessingSnapshot(
			response.MimeType,
			response.Playback != nil && response.Playback.URL != "",
			response.Waveform != nil && response.Waveform.URL != "",
			response.Spectrogram != nil && response.Spectrogram.URL != "",
			failures.Audio[fileID]+failures.Video[fileID],
			failures.Waveform[fileID],
		)
		response.ProcessingStatus = snapshot.Status
		response.ProcessingPercentage = snapshot.Percentage
	}

	return nil
}
