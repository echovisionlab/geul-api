package ffmpeg

import (
	"context"
	"fmt"
	"log/slog"
	"math"
)

// WaveformOptions contains options for waveform extraction
type WaveformOptions struct {
	NumSamples int // Number of waveform points to generate (default: 2048)
	SampleRate int // Waveform PCM sample rate (default: 8000)
	Precision  int // Decimal rounding precision factor (default: 10000)
	Channels   int // 0=auto, otherwise clamp to 1 or 2
}

// DefaultWaveformOptions returns default waveform extraction options
func DefaultWaveformOptions() WaveformOptions {
	return WaveformOptions{
		NumSamples: 2048,
		SampleRate: 8000,
		Precision:  10000,
	}
}

// GeneratePeaks extracts normalized waveform peaks from an audio file.
func (e *Executor) GeneratePeaks(ctx context.Context, inputPath string) ([][]float64, error) {
	return e.generateWaveformPeaks(ctx, inputPath, DefaultWaveformOptions())
}

func (e *Executor) generateWaveformPeaks(
	ctx context.Context,
	inputPath string,
	opts WaveformOptions,
) ([][]float64, error) {
	opts = normalizeWaveformOptions(opts)

	probe, err := e.Probe(ctx, inputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to probe audio: %w", err)
	}
	if probe.DurationSeconds == 0 {
		return nil, fmt.Errorf("audio file has no duration")
	}

	channels := determineWaveformChannels(probe.Channels, opts.Channels)
	totalFrames := int(math.Ceil(probe.DurationSeconds * float64(opts.SampleRate)))
	peaks, err := e.buildWaveformPeaksFromPCMStream(
		ctx,
		inputPath,
		channels,
		opts.SampleRate,
		totalFrames,
		opts.NumSamples,
		opts.Precision,
	)
	if err != nil {
		return nil, err
	}

	slog.Debug("Extracted waveform peaks",
		"input", inputPath,
		"channels", channels,
		"points", opts.NumSamples,
		"duration", probe.DurationSeconds,
	)

	return peaks, nil
}

func normalizeWaveformOptions(opts WaveformOptions) WaveformOptions {
	if opts.NumSamples <= 0 {
		opts.NumSamples = 2048
	}
	if opts.SampleRate <= 0 {
		opts.SampleRate = 8000
	}
	if opts.Precision <= 0 {
		opts.Precision = 10000
	}
	return opts
}

func determineWaveformChannels(sourceChannels int, requested int) int {
	if requested > 0 {
		if requested <= 1 {
			return 1
		}
		return 2
	}
	if sourceChannels <= 1 {
		return 1
	}
	return 2
}
