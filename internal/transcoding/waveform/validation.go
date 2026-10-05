package waveform

import (
	"fmt"

	"github.com/echovisionlab/geul-api/internal/transcoding/media"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func validateWaveformEvent(event *apiv1.WaveformGenerateEvent) error {
	if event == nil {
		return fmt.Errorf("waveform job is required")
	}
	if err := media.ValidateIdentity(
		event.GetEventId(),
		event.GetEntityType(),
		event.GetEntityId(),
		event.GetFileId(),
	); err != nil {
		return err
	}
	if err := media.ValidateSource(event.GetSource(), event.GetFileId(), "audio"); err != nil {
		return fmt.Errorf("invalid waveform source: %w", err)
	}
	return validateWaveformOutput(event.GetOutput())
}

func validateWaveformOutput(output *commonv1.AssetWriteTarget) error {
	if err := media.ValidateAssetOutput(output, "json", "application/json"); err != nil {
		return fmt.Errorf("invalid waveform output: %w", err)
	}
	return nil
}
