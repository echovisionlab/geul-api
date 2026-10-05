package ffmpeg

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// HLSResolution describes one video rendition.
type HLSResolution struct {
	Name    string
	Width   int
	Height  int
	Bitrate string
}

// HLSOptions controls video HLS generation.
type HLSOptions struct {
	Resolutions     []HLSResolution
	SegmentDuration int
	AudioBitrate    string
}

// DefaultHLSOptions returns the standard video HLS settings.
func DefaultHLSOptions() HLSOptions {
	return HLSOptions{
		Resolutions: []HLSResolution{
			mustHLSResolutionPreset("360p"),
			mustHLSResolutionPreset("480p"),
			mustHLSResolutionPreset("720p"),
		},
		SegmentDuration: 6,
		AudioBitrate:    "128k",
	}
}

// HLSResolutionPreset returns a named rendition preset when one exists.
func HLSResolutionPreset(name string) (HLSResolution, bool) {
	switch name {
	case "360p":
		return HLSResolution{Name: name, Width: 640, Height: 360, Bitrate: "800k"}, true
	case "480p":
		return HLSResolution{Name: name, Width: 854, Height: 480, Bitrate: "1500k"}, true
	case "720p":
		return HLSResolution{Name: name, Width: 1280, Height: 720, Bitrate: "3000k"}, true
	case "1080p":
		return HLSResolution{Name: name, Width: 1920, Height: 1080, Bitrate: "6000k"}, true
	default:
		return HLSResolution{}, false
	}
}

func mustHLSResolutionPreset(name string) HLSResolution {
	preset, found := HLSResolutionPreset(name)
	if !found {
		panic("unknown HLS resolution preset: " + name)
	}
	return preset
}

// HLSResult contains the result of HLS generation
type HLSResult struct {
	MasterPlaylist   string            // Path to master.m3u8
	VariantPlaylists map[string]string // Resolution name -> path to variant playlist
	SegmentDir       string            // Directory containing all HLS files
}

// GenerateHLSWithProgress generates HLS streams with progress reporting.
func (e *Executor) GenerateHLSWithProgress(
	ctx context.Context,
	inputPath, outputDir string,
	opts HLSOptions,
	totalDurationSec float64,
	onProgress ProgressCallback,
) (*HLSResult, error) {
	if len(opts.Resolutions) == 0 {
		return nil, fmt.Errorf("at least one resolution must be specified")
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}
	opts = normalizeHLSOptions(opts)

	probe, err := e.Probe(ctx, inputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to probe input: %w", err)
	}
	resolutions := selectHLSResolutions(opts.Resolutions, probe.Width, probe.Height)
	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("ffp-%d.sock", time.Now().UnixNano()))
	defer func() { _ = os.Remove(socketPath) }()

	args := buildHLSArgs(inputPath, outputDir, socketPath, opts, resolutions, probe.HasAudio)
	slog.Info("Generating HLS streams with progress",
		"input", inputPath,
		"output_dir", outputDir,
		"resolutions", len(resolutions),
	)
	output, err := e.runFFmpegWithProgress(ctx, args, socketPath, totalDurationSec, onProgress)
	if err != nil {
		return nil, fmt.Errorf("ffmpeg HLS generation failed: %w, output: %s", err, string(output))
	}

	result := buildHLSResult(outputDir, resolutions)
	slog.Info("HLS generation completed",
		"master_playlist", result.MasterPlaylist,
		"variants", len(result.VariantPlaylists),
	)
	return result, nil
}

func normalizeHLSOptions(opts HLSOptions) HLSOptions {
	if opts.SegmentDuration <= 0 {
		opts.SegmentDuration = 6
	}
	if opts.AudioBitrate == "" {
		opts.AudioBitrate = "128k"
	}
	return opts
}

func selectHLSResolutions(requested []HLSResolution, width, height int) []HLSResolution {
	selected := make([]HLSResolution, 0, len(requested))
	for _, resolution := range requested {
		if resolution.Width <= width && resolution.Height <= height {
			selected = append(selected, resolution)
		}
	}
	if len(selected) > 0 {
		return selected
	}
	fallback := mustHLSResolutionPreset("720p")
	return []HLSResolution{{
		Name:    "original",
		Width:   width,
		Height:  height,
		Bitrate: fallback.Bitrate,
	}}
}

func buildHLSArgs(
	inputPath string,
	outputDir string,
	socketPath string,
	opts HLSOptions,
	resolutions []HLSResolution,
	hasAudio bool,
) []string {
	args := []string{
		"-y",
		"-i", inputPath,
		"-filter_complex", buildHLSFilterComplex(resolutions),
	}
	args = appendVideoHLSMappings(args, resolutions, opts.SegmentDuration)
	if hasAudio {
		args = appendAudioHLSMappings(args, len(resolutions), opts.AudioBitrate)
	}
	return append(args,
		"-f", "hls",
		"-hls_time", strconv.Itoa(opts.SegmentDuration),
		"-hls_playlist_type", "vod",
		"-hls_flags", "independent_segments+temp_file",
		"-hls_segment_type", "mpegts",
		"-hls_segment_filename", filepath.Join(outputDir, "stream_%v_%03d.ts"),
		"-master_pl_name", "master.m3u8",
		"-var_stream_map", buildVariantStreamMap(resolutions, hasAudio),
		"-progress", "unix://"+socketPath,
		filepath.Join(outputDir, "stream_%v.m3u8"),
	)
}

func buildHLSFilterComplex(resolutions []HLSResolution) string {
	labels := make([]string, len(resolutions))
	filters := make([]string, len(resolutions))
	for index, resolution := range resolutions {
		labels[index] = fmt.Sprintf("[v%d]", index)
		filters[index] = fmt.Sprintf(
			"[v%d]scale=w=%d:h=%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2[v%dout]",
			index,
			resolution.Width,
			resolution.Height,
			resolution.Width,
			resolution.Height,
			index,
		)
	}
	return fmt.Sprintf("[0:v]split=%d%s; %s", len(resolutions), strings.Join(labels, ""), strings.Join(filters, "; "))
}

func appendVideoHLSMappings(args []string, resolutions []HLSResolution, segmentDuration int) []string {
	keyframeInterval := strconv.Itoa(segmentDuration * 30)
	for index, resolution := range resolutions {
		args = append(args,
			"-map", fmt.Sprintf("[v%dout]", index),
			fmt.Sprintf("-c:v:%d", index), "libx264",
			fmt.Sprintf("-b:v:%d", index), resolution.Bitrate,
			fmt.Sprintf("-maxrate:%d", index), resolution.Bitrate,
			fmt.Sprintf("-bufsize:%d", index), resolution.Bitrate,
			fmt.Sprintf("-preset:%d", index), "medium",
			fmt.Sprintf("-g:%d", index), keyframeInterval,
			fmt.Sprintf("-keyint_min:%d", index), keyframeInterval,
			fmt.Sprintf("-sc_threshold:%d", index), "0",
		)
	}
	return args
}

func appendAudioHLSMappings(args []string, streamCount int, bitrate string) []string {
	for index := range streamCount {
		args = append(args,
			"-map", "0:a",
			fmt.Sprintf("-c:a:%d", index), "aac",
			fmt.Sprintf("-b:a:%d", index), bitrate,
			fmt.Sprintf("-ar:%d", index), "44100",
			fmt.Sprintf("-ac:%d", index), "2",
		)
	}
	return args
}

func buildVariantStreamMap(resolutions []HLSResolution, hasAudio bool) string {
	entries := make([]string, len(resolutions))
	for index, resolution := range resolutions {
		if hasAudio {
			entries[index] = fmt.Sprintf("v:%d,a:%d,name:%s", index, index, resolution.Name)
		} else {
			entries[index] = fmt.Sprintf("v:%d,name:%s", index, resolution.Name)
		}
	}
	return strings.Join(entries, " ")
}

func buildHLSResult(outputDir string, resolutions []HLSResolution) *HLSResult {
	variants := make(map[string]string, len(resolutions))
	for _, resolution := range resolutions {
		variants[resolution.Name] = filepath.Join(outputDir, fmt.Sprintf("stream_%s.m3u8", resolution.Name))
	}
	return &HLSResult{
		MasterPlaylist:   filepath.Join(outputDir, "master.m3u8"),
		VariantPlaylists: variants,
		SegmentDir:       outputDir,
	}
}
