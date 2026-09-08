package ffmpeg

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ProgressCallback receives an FFmpeg progress percentage.
type ProgressCallback func(percent int)

func (e *Executor) runFFmpegWithProgress(
	ctx context.Context,
	args []string,
	socketPath string,
	totalDurationSec float64,
	onProgress ProgressCallback,
) ([]byte, error) {
	cmd := exec.CommandContext(ctx, e.ffmpegPath, args...)
	configureGracefulCommand(cmd, 10*time.Second)

	var listeners sync.WaitGroup
	listeners.Add(1)
	go func() {
		defer listeners.Done()
		e.reportProgress(ctx, socketPath, totalDurationSec, onProgress)
	}()

	output, err := cmd.CombinedOutput()
	listeners.Wait()
	if err == nil && onProgress != nil {
		onProgress(100)
	}
	return output, err
}

func (e *Executor) reportProgress(
	ctx context.Context,
	sockPath string,
	totalDurationSec float64,
	onProgress ProgressCallback,
) {
	listener := e.listenProgress
	if listener == nil {
		listener = listenFFmpegProgress
	}
	listener(ctx, sockPath, totalDurationSec, onProgress)
}

// listenFFmpegProgress listens on a Unix socket for FFmpeg progress updates.
// FFmpeg sends progress data including out_time_ms which we use to calculate percentage.
func listenFFmpegProgress(ctx context.Context, sockPath string, totalDurationSec float64, onProgress ProgressCallback) {
	listenFFmpegProgressWith(ctx, sockPath, totalDurationSec, onProgress, net.Listen, 30*time.Second)
}

type progressListenerFactory func(network, address string) (net.Listener, error)

func listenFFmpegProgressWith(
	ctx context.Context,
	sockPath string,
	totalDurationSec float64,
	onProgress ProgressCallback,
	listen progressListenerFactory,
	acceptTimeout time.Duration,
) {
	if onProgress == nil {
		return
	}

	// Remove any existing socket file
	_ = os.Remove(sockPath)

	// Create Unix socket listener
	listener, err := listen("unix", sockPath)
	if err != nil {
		slog.Warn("Failed to create progress socket", "error", err)
		return
	}
	defer func() { _ = listener.Close() }()

	// Accept connection with timeout
	type acceptResult struct {
		conn net.Conn
		err  error
	}
	acceptCh := make(chan acceptResult, 1)
	go func() {
		conn, err := listener.Accept()
		acceptCh <- acceptResult{conn, err}
	}()

	var conn net.Conn
	select {
	case <-ctx.Done():
		return
	case result := <-acceptCh:
		if result.err != nil {
			slog.Warn("Failed to accept progress connection", "error", result.err)
			return
		}
		conn = result.conn
	case <-time.After(acceptTimeout):
		slog.Warn("Timeout waiting for FFmpeg progress connection")
		return
	}
	defer func() { _ = conn.Close() }()
	readFFmpegProgress(ctx, conn, totalDurationSec, onProgress)
}

func readFFmpegProgress(ctx context.Context, source io.Reader, totalDurationSec float64, onProgress ProgressCallback) {
	scanner := bufio.NewScanner(source)
	lastProgress := 0

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line := scanner.Text()
		if line == "progress=end" {
			return
		}
		percent, valid := ffmpegProgressPercent(line, totalDurationSec)
		if !valid || percent <= lastProgress {
			continue
		}
		lastProgress = percent
		onProgress(percent)
	}

	if err := scanner.Err(); err != nil && !strings.Contains(err.Error(), "use of closed") {
		slog.Debug("Progress scanner error", "error", err)
	}
}

func ffmpegProgressPercent(line string, totalDurationSec float64) (int, bool) {
	if !strings.HasPrefix(line, "out_time_ms=") || totalDurationSec <= 0 {
		return 0, false
	}
	microseconds, err := strconv.ParseInt(strings.TrimPrefix(line, "out_time_ms="), 10, 64)
	if err != nil {
		return 0, false
	}
	percent := int(float64(microseconds) / 1_000_000.0 / totalDurationSec * 100)
	return min(max(percent, 0), 99), true
}
