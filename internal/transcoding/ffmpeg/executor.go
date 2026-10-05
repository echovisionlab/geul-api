// Package ffmpeg wraps FFmpeg and FFprobe media operations.
package ffmpeg

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Executor wraps FFmpeg and FFprobe operations
type Executor struct {
	ffmpegPath     string
	ffprobePath    string
	tempDir        string
	listenProgress func(context.Context, string, float64, ProgressCallback)
}

func configureGracefulCommand(cmd *exec.Cmd, waitDelay time.Duration) {
	cmd.Cancel = func() error { return interruptProcess(cmd.Process) }
	cmd.WaitDelay = waitDelay
}

func interruptProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	return process.Signal(syscall.SIGINT)
}

// NewExecutor creates a new FFmpeg executor
func NewExecutor(ffmpegPath, ffprobePath, tempDir string) (*Executor, error) {
	// Verify FFmpeg is available
	if _, err := exec.LookPath(ffmpegPath); err != nil {
		return nil, fmt.Errorf("ffmpeg not found at %s: %w", ffmpegPath, err)
	}

	// Verify FFprobe is available
	if _, err := exec.LookPath(ffprobePath); err != nil {
		return nil, fmt.Errorf("ffprobe not found at %s: %w", ffprobePath, err)
	}

	// Create temp directory
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}

	slog.Info("FFmpeg executor initialized",
		"ffmpeg", ffmpegPath,
		"ffprobe", ffprobePath,
		"temp_dir", tempDir,
	)

	return &Executor{
		ffmpegPath:     ffmpegPath,
		ffprobePath:    ffprobePath,
		tempDir:        tempDir,
		listenProgress: listenFFmpegProgress,
	}, nil
}
