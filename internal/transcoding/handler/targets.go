package handler

import (
	"fmt"

	"github.com/echovisionlab/geul-api/internal/transcoding/media"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func validateAudioJob(job *apiv1.TranscodeAudioEvent) error {
	if job == nil {
		return fmt.Errorf("audio job is required")
	}
	if err := validateCommandIdentity(job); err != nil {
		return err
	}
	if err := media.ValidateSource(job.GetSource(), job.GetFileId(), "audio"); err != nil {
		return fmt.Errorf("invalid audio source: %w", err)
	}
	if err := media.ValidateHLSOutput(job.GetHlsOutput(), job.GetFileId()); err != nil {
		return fmt.Errorf("invalid audio HLS target: %w", err)
	}
	if err := media.ValidateAssetOutput(job.GetSpectrogramOutput(), "png", "image/png"); err != nil {
		return fmt.Errorf("invalid spectrogram target: %w", err)
	}
	return nil
}

func validateVideoJob(job *apiv1.TranscodeVideoEvent) error {
	if job == nil {
		return fmt.Errorf("video job is required")
	}
	if err := validateCommandIdentity(job); err != nil {
		return err
	}
	if err := media.ValidateSource(job.GetSource(), job.GetFileId(), "video"); err != nil {
		return fmt.Errorf("invalid video source: %w", err)
	}
	if err := media.ValidateHLSOutput(job.GetHlsOutput(), job.GetFileId()); err != nil {
		return fmt.Errorf("invalid video HLS target: %w", err)
	}
	return validateThumbnailTarget(job)
}

func validateCommandIdentity(command transcodeCommand) error {
	return media.ValidateIdentity(
		command.GetEventId(),
		command.GetEntityType(),
		command.GetEntityId(),
		command.GetFileId(),
	)
}

func validateThumbnailTarget(job *apiv1.TranscodeVideoEvent) error {
	generate := job.GetOptions() == nil || job.GetOptions().GetGenerateThumbnail()
	if !generate && job.GetThumbnailOutput() != nil {
		return fmt.Errorf("thumbnail target must be omitted when thumbnail generation is disabled")
	}
	if !generate {
		return nil
	}
	if err := media.ValidateAssetOutput(job.GetThumbnailOutput(), "webp", "image/webp"); err != nil {
		return fmt.Errorf("invalid thumbnail target: %w", err)
	}
	return nil
}
