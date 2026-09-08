package ffmpeg

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// SpectrogramOptions contains options for spectrogram image generation.
type SpectrogramOptions struct {
	Width      int
	Height     int
	SampleRate int
}

// DefaultSpectrogramOptions returns default spectrogram image options.
func DefaultSpectrogramOptions() SpectrogramOptions {
	return SpectrogramOptions{
		Width:      1600,
		Height:     224,
		SampleRate: 8000,
	}
}

// GenerateAudioSpectrogramWithProgress renders a spectrogram image with progress reporting.
func (e *Executor) GenerateAudioSpectrogramWithProgress(
	ctx context.Context,
	inputPath, outputPath string,
	opts SpectrogramOptions,
	totalDurationSec float64,
	onProgress ProgressCallback,
) error {
	opts = normalizeSpectrogramOptions(opts)

	var progressURL string
	var wg sync.WaitGroup
	var cancelProgress context.CancelFunc
	if onProgress != nil {
		progressCtx, cancel := context.WithCancel(ctx)
		cancelProgress = cancel
		defer cancelProgress()

		sockPath := filepath.Join(os.TempDir(), fmt.Sprintf("ffs-%d.sock", time.Now().UnixNano()))
		defer func() { _ = os.Remove(sockPath) }()
		progressURL = "unix://" + sockPath

		wg.Add(1)
		go func() {
			defer wg.Done()
			e.reportProgress(progressCtx, sockPath, totalDurationSec, onProgress)
		}()
	}

	args := buildAudioSpectrogramArgs(inputPath, outputPath, opts, totalDurationSec, progressURL)

	cmd := exec.CommandContext(ctx, e.ffmpegPath, args...)
	configureGracefulCommand(cmd, 5*time.Second)

	output, err := cmd.CombinedOutput()
	if cancelProgress != nil {
		cancelProgress()
	}
	wg.Wait()
	if err != nil {
		return fmt.Errorf("ffmpeg spectrogram generation failed: %w, output: %s", err, string(output))
	}

	if onProgress != nil {
		onProgress(100)
	}

	slog.Debug("Generated audio spectrogram",
		"input", inputPath,
		"output", outputPath,
		"width", opts.Width,
		"height", opts.Height,
	)

	return nil
}

func normalizeSpectrogramOptions(opts SpectrogramOptions) SpectrogramOptions {
	if opts.Width <= 0 {
		opts.Width = 1600
	}
	if opts.Height <= 0 {
		opts.Height = 224
	}
	if opts.SampleRate <= 0 {
		opts.SampleRate = 8000
	}
	return opts
}

func buildAudioSpectrogramArgs(
	inputPath string,
	outputPath string,
	opts SpectrogramOptions,
	durationSeconds float64,
	progressURL string,
) []string {
	opts = normalizeSpectrogramOptions(opts)
	if durationSeconds <= 0 {
		durationSeconds = 1
	}
	fps := float64(opts.Width) / durationSeconds

	filterTemplate := "[0:a:0]aformat=channel_layouts=mono,aresample=%d,showspectrum=" +
		"s=%dx%d:slide=scroll:mode=combined:color=magma:scale=log:fscale=log:" +
		"legend=disabled:fps=%.6f[spec]"
	filter := fmt.Sprintf(
		filterTemplate,
		opts.SampleRate,
		opts.Width,
		opts.Height,
		fps,
	)

	args := []string{
		"-y",
		"-v", "error",
		"-nostdin",
		"-i", inputPath,
		"-filter_complex", filter,
		"-map", "[spec]",
		"-frames:v", strconv.Itoa(opts.Width),
		"-f", "image2",
		"-update", "1",
	}
	if progressURL != "" {
		args = append(args, "-progress", progressURL)
	}
	args = append(args, outputPath)
	return args
}

// ProgressCallback is called periodically during FFmpeg operations with the current progress (0-100).
