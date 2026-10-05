package ffmpeg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// AudioHLSOptions contains options for audio-only HLS generation.
type AudioHLSOptions struct {
	SegmentDuration int
	Bitrate         string
	SampleRate      int
	Channels        int
}

// DefaultAudioHLSOptions returns default HLS options for audio playback.
func DefaultAudioHLSOptions() AudioHLSOptions {
	return AudioHLSOptions{
		SegmentDuration: 6,
		Bitrate:         "192k",
		SampleRate:      44100,
		Channels:        2,
	}
}

// GenerateAudioHLSWithProgress generates an audio-only HLS VOD playlist with progress reporting.
func (e *Executor) GenerateAudioHLSWithProgress(
	ctx context.Context,
	inputPath, outputDir string,
	opts AudioHLSOptions,
	totalDurationSec float64,
	onProgress ProgressCallback,
) (*HLSResult, error) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}
	opts = normalizeAudioHLSOptions(opts)

	sockPath := filepath.Join(os.TempDir(), fmt.Sprintf("ffa-%d.sock", time.Now().UnixNano()))
	defer func() { _ = os.Remove(sockPath) }()

	masterPlaylistPath := filepath.Join(outputDir, "master.m3u8")
	args := buildAudioHLSArgs(inputPath, outputDir, masterPlaylistPath, sockPath, opts)
	output, err := e.runFFmpegWithProgress(ctx, args, sockPath, totalDurationSec, onProgress)
	if err != nil {
		return nil, fmt.Errorf("ffmpeg audio HLS generation failed: %w, output: %s", err, string(output))
	}

	return &HLSResult{
		MasterPlaylist:   masterPlaylistPath,
		VariantPlaylists: map[string]string{},
		SegmentDir:       outputDir,
	}, nil
}

func normalizeAudioHLSOptions(options AudioHLSOptions) AudioHLSOptions {
	defaults := DefaultAudioHLSOptions()
	if options.SegmentDuration <= 0 {
		options.SegmentDuration = defaults.SegmentDuration
	}
	if options.Bitrate == "" {
		options.Bitrate = defaults.Bitrate
	}
	if options.SampleRate <= 0 {
		options.SampleRate = defaults.SampleRate
	}
	if options.Channels <= 0 {
		options.Channels = defaults.Channels
	}
	return options
}

func buildAudioHLSArgs(
	inputPath, outputDir, masterPlaylistPath, socketPath string,
	options AudioHLSOptions,
) []string {
	return []string{
		"-y", "-i", inputPath,
		"-vn",
		"-c:a", "aac",
		"-b:a", options.Bitrate,
		"-ar", strconv.Itoa(options.SampleRate),
		"-ac", strconv.Itoa(options.Channels),
		"-f", "hls",
		"-hls_time", strconv.Itoa(options.SegmentDuration),
		"-hls_playlist_type", "vod",
		"-hls_flags", "independent_segments+temp_file",
		"-hls_segment_type", "mpegts",
		"-hls_segment_filename", filepath.Join(outputDir, "segment_%03d.ts"),
		"-progress", "unix://" + socketPath,
		masterPlaylistPath,
	}
}
