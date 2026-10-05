package ffmpeg

import (
	"context"
	"fmt"
)

// ValidateAudioFile validates that a file is a valid audio file
func (e *Executor) ValidateAudioFile(ctx context.Context, path string) error {
	probe, err := e.Probe(ctx, path)
	if err != nil {
		return fmt.Errorf("invalid audio file: %w", err)
	}

	if probe.DurationSeconds == 0 {
		return fmt.Errorf("invalid audio file: no duration detected")
	}

	if !probe.HasAudio {
		return fmt.Errorf("invalid audio file: no audio track detected")
	}

	if probe.HasVideo {
		return fmt.Errorf("file contains video track, expected audio only")
	}

	return nil
}

// ValidateVideoFile validates that a file is a valid video file
func (e *Executor) ValidateVideoFile(ctx context.Context, path string) error {
	probe, err := e.Probe(ctx, path)
	if err != nil {
		return fmt.Errorf("invalid video file: %w", err)
	}

	if probe.DurationSeconds == 0 {
		return fmt.Errorf("invalid video file: no duration detected")
	}

	if !probe.HasVideo || probe.Width == 0 || probe.Height == 0 {
		return fmt.Errorf("invalid video file: no video dimensions detected")
	}

	return nil
}
