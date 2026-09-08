package ffmpeg

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// VideoThumbnailOptions contains options for video thumbnail generation.
type VideoThumbnailOptions struct {
	TimeSec int
	Quality int // WebP quality: 0-100, higher is better (default: 90)
}

// GenerateVideoThumbnail extracts a thumbnail from a video at original size.
// No resizing is applied - CDN imgproxy handles resizing on-the-fly.
func (e *Executor) GenerateVideoThumbnail(
	ctx context.Context,
	inputPath, outputPath string,
	opts VideoThumbnailOptions,
) error {
	encoderArgs := []string{"-c:v", "libwebp", "-quality", strconv.Itoa(opts.Quality)}
	if strings.EqualFold(filepath.Ext(outputPath), ".png") {
		encoderArgs = []string{"-c:v", "png"}
	}

	args := []string{
		"-y",
		"-ss", strconv.Itoa(opts.TimeSec),
		"-i", inputPath,
		"-vframes", "1",
	}
	args = append(args, encoderArgs...)
	args = append(args, outputPath)

	cmd := exec.CommandContext(ctx, e.ffmpegPath, args...)
	configureGracefulCommand(cmd, 5*time.Second)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg failed: %w, output: %s", err, string(output))
	}

	slog.Debug("Generated video thumbnail",
		"input", inputPath,
		"output", outputPath,
		"time_sec", opts.TimeSec,
		"quality", opts.Quality,
	)

	return nil
}
