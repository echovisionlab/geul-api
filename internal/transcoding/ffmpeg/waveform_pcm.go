package ffmpeg

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

func (e *Executor) buildWaveformPeaksFromPCMStream(
	ctx context.Context,
	inputPath string,
	channels int,
	sampleRate int,
	totalFrames int,
	maxLength int,
	precision int,
) ([][]float64, error) {
	args := []string{
		"-y",
		"-v", "error",
		"-nostdin",
		"-i", inputPath,
		"-map", "0:a:0",
		"-vn",
		"-ac", strconv.Itoa(channels),
		"-ar", strconv.Itoa(sampleRate),
		"-f", "s16le",
		"-acodec", "pcm_s16le",
		"pipe:1",
	}

	cmd := exec.CommandContext(ctx, e.ffmpegPath, args...)
	configureGracefulCommand(cmd, 5*time.Second)
	return readWaveformCommand(cmd, channels, totalFrames, maxLength, precision)
}

func readWaveformCommand(
	cmd *exec.Cmd,
	channels int,
	totalFrames int,
	maxLength int,
	precision int,
) ([][]float64, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open ffmpeg waveform stdout: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ffmpeg waveform extraction: %w", err)
	}

	peaks, readErr := buildWaveformPeaksFromPCMReader(
		stdout,
		channels,
		totalFrames,
		maxLength,
		precision,
	)
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		return nil, fmt.Errorf("ffmpeg waveform extraction failed: %w, output: %s", waitErr, stderr.String())
	}

	return peaks, nil
}
