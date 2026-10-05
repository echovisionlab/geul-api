package hls

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	transcoderffmpeg "github.com/echovisionlab/geul-api/internal/transcoding/ffmpeg"
	"github.com/stretchr/testify/require"
)

func TestGeneratedAudioHLSPackagePassesValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration is disabled in short mode")
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	require.NoError(t, err, "ffmpeg must be installed for the default integration suite")
	ffprobePath, err := exec.LookPath("ffprobe")
	require.NoError(t, err, "ffprobe must be installed for the default integration suite")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	workDir := t.TempDir()
	sourcePath := filepath.Join(workDir, "source.wav")
	output, err := exec.CommandContext(
		ctx,
		ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-c:a", "pcm_s16le",
		sourcePath,
	).CombinedOutput()
	require.NoError(t, err, string(output))

	executor, err := transcoderffmpeg.NewExecutor(ffmpegPath, ffprobePath, workDir)
	require.NoError(t, err)
	hlsResult, err := executor.GenerateAudioHLSWithProgress(
		ctx,
		sourcePath,
		filepath.Join(workDir, "hls"),
		transcoderffmpeg.DefaultAudioHLSOptions(),
		1,
		func(int) {},
	)
	require.NoError(t, err)

	pkg, err := Inspect(hlsResult.SegmentDir)
	require.NoError(t, err)
	require.NotEmpty(t, pkg.files)
	require.Equal(t, "master.m3u8", pkg.manifest.name)
}

func TestGeneratedVideoHLSPackagePassesValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration is disabled in short mode")
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	require.NoError(t, err, "ffmpeg must be installed for the default integration suite")
	ffprobePath, err := exec.LookPath("ffprobe")
	require.NoError(t, err, "ffprobe must be installed for the default integration suite")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	workDir := t.TempDir()
	sourcePath := filepath.Join(workDir, "source.mp4")
	output, err := exec.CommandContext(
		ctx,
		ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=640x360:rate=30:duration=1",
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
		sourcePath,
	).CombinedOutput()
	require.NoError(t, err, string(output))

	executor, err := transcoderffmpeg.NewExecutor(ffmpegPath, ffprobePath, workDir)
	require.NoError(t, err)
	preset, found := transcoderffmpeg.HLSResolutionPreset("360p")
	require.True(t, found)
	hlsResult, err := executor.GenerateHLSWithProgress(
		ctx,
		sourcePath,
		filepath.Join(workDir, "hls"),
		transcoderffmpeg.HLSOptions{
			Resolutions:     []transcoderffmpeg.HLSResolution{preset},
			SegmentDuration: 1,
			AudioBitrate:    "128k",
		},
		1,
		func(int) {},
	)
	require.NoError(t, err)

	pkg, err := Inspect(hlsResult.SegmentDir)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(pkg.files), 2)
	require.Equal(t, "master.m3u8", pkg.manifest.name)
}
