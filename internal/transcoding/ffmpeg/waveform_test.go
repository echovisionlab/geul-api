package ffmpeg

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBuildWaveformBucketRangesKeepsOverlapCompatibleWithExportPeaks(t *testing.T) {
	t.Parallel()

	got := buildWaveformBucketRanges(5, 4)
	want := []waveformBucketRange{
		{start: 0, end: 2},
		{start: 1, end: 3},
		{start: 2, end: 4},
		{start: 3, end: 5},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected bucket ranges: got %#v want %#v", got, want)
	}
}

func TestBuildAudioSpectrogramArgsUsesStreamingOverwrite(t *testing.T) {
	t.Parallel()

	args := buildAudioSpectrogramArgs(
		"/tmp/source.ogg",
		"/tmp/spectrogram.png",
		SpectrogramOptions{Width: 1600, Height: 224, SampleRate: 8000},
		5840.712,
		"unix:///tmp/ffmpeg-progress.sock",
	)
	joined := strings.Join(args, " ")

	if strings.Contains(joined, "showspectrumpic") {
		t.Fatalf("spectrogram command must not use memory-bound showspectrumpic: %s", joined)
	}
	for _, expected := range []string{
		"showspectrum=",
		"slide=scroll",
		"color=magma",
		"aresample=8000",
		"-frames:v 1600",
		"-update 1",
		"-progress unix:///tmp/ffmpeg-progress.sock",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("missing %q in spectrogram args: %s", expected, joined)
		}
	}
}

func TestBuildWaveformPeaksFromPCMReaderStreamsBuckets(t *testing.T) {
	t.Parallel()

	var pcm bytes.Buffer
	samples := [][2]int16{
		{1000, -2000},
		{-3000, 4000},
		{2000, -5000},
		{-7000, 6000},
	}
	for _, frame := range samples {
		for _, sample := range frame {
			if err := binary.Write(&pcm, binary.LittleEndian, sample); err != nil {
				t.Fatalf("write pcm sample: %v", err)
			}
		}
	}

	got, err := buildWaveformPeaksFromPCMReader(&pcm, 2, len(samples), 2, 10000)
	if err != nil {
		t.Fatalf("build waveform peaks: %v", err)
	}

	want := [][]float64{
		{-0.0916, -0.2136},
		{0.1221, 0.1831},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected streamed peaks: got %#v want %#v", got, want)
	}
}

func TestCleanupStaleWorkDirsRemovesChildrenButKeepsRoot(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	exec := &Executor{tempDir: tempDir}

	if err := os.MkdirAll(filepath.Join(tempDir, "job-1"), 0o755); err != nil {
		t.Fatalf("mkdir job dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "orphan.tmp"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	removed, err := exec.CleanupStaleWorkDirs()
	if err != nil {
		t.Fatalf("cleanup stale work dirs: %v", err)
	}
	if removed != 2 {
		t.Fatalf("expected 2 removed entries, got %d", removed)
	}

	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("read temp dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty temp dir, got %d entries", len(entries))
	}
}

func TestDefaultOptions(t *testing.T) {
	t.Parallel()

	if DefaultWaveformOptions().NumSamples != 2048 {
		t.Fatalf("unexpected waveform defaults: %#v", DefaultWaveformOptions())
	}
	if DefaultHLSOptions().SegmentDuration != 6 {
		t.Fatalf("unexpected hls defaults: %#v", DefaultHLSOptions())
	}
}

func TestHLSResolutionPresetRejectsUnknownName(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"360p", "480p", "720p", "1080p"} {
		preset, found := HLSResolutionPreset(name)
		if !found || preset.Name != name {
			t.Fatalf("preset %q = %#v, found=%t", name, preset, found)
		}
	}
	preset, found := HLSResolutionPreset("unknown")
	if found || preset != (HLSResolution{}) {
		t.Fatalf("unexpected preset: %#v, found=%t", preset, found)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("mustHLSResolutionPreset should panic for an unknown internal preset")
		}
	}()
	_ = mustHLSResolutionPreset("unknown")
}

func TestExecutorWithFakeBinariesCoversProbeValidationAndWorkDirs(t *testing.T) {
	t.Parallel()

	binDir := t.TempDir()
	ffmpegPath := filepath.Join(binDir, "ffmpeg")
	ffprobePath := filepath.Join(binDir, "ffprobe")
	writeExecutable(t, ffmpegPath, "#!/bin/sh\nexit 0\n")
	writeExecutable(t, ffprobePath, `#!/bin/sh
case "$*" in
  *audio.mp3*)
cat <<'JSON'
{"format":{"duration":"12.5","bit_rate":"256000","format_name":"mp3"},"streams":[{"codec_name":"mp3","sample_rate":"44100","channels":2}]}
JSON
exit 0
;;
esac
cat <<'JSON'
{"format":{"duration":"12.5","bit_rate":"256000","format_name":"mov,mp4"},"streams":[{"codec_name":"h264","width":1280,"height":720},{"codec_name":"aac","sample_rate":"44100","channels":2}]}
JSON
`)

	exec, err := NewExecutor(ffmpegPath, ffprobePath, filepath.Join(t.TempDir(), "work"))
	if err != nil {
		t.Fatalf("NewExecutor returned error: %v", err)
	}
	assertExecutorOperations(t, exec)
}

func assertExecutorOperations(t *testing.T, exec *Executor) {
	t.Helper()
	workDir := assertExecutorWorkDir(t, exec)
	assertExecutorMediaOperations(t, exec, workDir)
	if err := exec.CleanupJobWorkDir("job-1"); err != nil {
		t.Fatalf("CleanupJobWorkDir returned error: %v", err)
	}
}

func assertExecutorWorkDir(t *testing.T, exec *Executor) string {
	t.Helper()
	workDir, err := exec.CreateJobWorkDir("job-1")
	if err != nil {
		t.Fatalf("CreateJobWorkDir returned error: %v", err)
	}
	if _, err := os.Stat(workDir); err != nil {
		t.Fatalf("work dir missing: %v", err)
	}
	return workDir
}

func assertExecutorMediaOperations(t *testing.T, exec *Executor, workDir string) {
	t.Helper()
	probe, err := exec.Probe(context.Background(), "input.mp4")
	if err != nil {
		t.Fatalf("Probe returned error: %v", err)
	}
	if !probe.HasAudio || !probe.HasVideo || probe.Width != 1280 || probe.SampleRate != 44100 {
		t.Fatalf("unexpected probe result: %#v", probe)
	}
	if err := exec.ValidateAudioFile(context.Background(), "audio.mp3"); err != nil {
		t.Fatalf("ValidateAudioFile returned error: %v", err)
	}
	if err := exec.ValidateVideoFile(context.Background(), "input.mp4"); err != nil {
		t.Fatalf("ValidateVideoFile returned error: %v", err)
	}
	if err := exec.GenerateVideoThumbnail(context.Background(), "input.mp4", filepath.Join(workDir, "thumb.webp"), VideoThumbnailOptions{TimeSec: 3, Quality: 80}); err != nil {
		t.Fatalf("GenerateVideoThumbnail returned error: %v", err)
	}
	if err := exec.GenerateVideoThumbnail(context.Background(), "input.mp4", filepath.Join(workDir, "thumb.png"), VideoThumbnailOptions{TimeSec: 3, Quality: 80}); err != nil {
		t.Fatalf("GenerateVideoThumbnail png returned error: %v", err)
	}
}

func TestGenerateHLSWithProgressErrorAndDefaultResolutionWithFakeBinaries(t *testing.T) {
	t.Parallel()

	binDir := t.TempDir()
	ffmpegPath := filepath.Join(binDir, "ffmpeg")
	ffprobePath := filepath.Join(binDir, "ffprobe")
	writeExecutable(t, ffmpegPath, "#!/bin/sh\nexit 0\n")
	writeExecutable(t, ffprobePath, `#!/bin/sh
cat <<'JSON'
{"format":{"duration":"8","format_name":"mov,mp4"},"streams":[{"codec_name":"h264","width":320,"height":180}]}
JSON
`)

	exec := &Executor{ffmpegPath: ffmpegPath, ffprobePath: ffprobePath}
	if _, err := exec.GenerateHLSWithProgress(context.Background(), "input.mp4", filepath.Join(t.TempDir(), "bad"), HLSOptions{}, 8, nil); err == nil {
		t.Fatal("expected empty resolution error")
	}

	result, err := exec.GenerateHLSWithProgress(context.Background(), "input.mp4", filepath.Join(t.TempDir(), "hls"), HLSOptions{
		Resolutions: []HLSResolution{{Name: "720p", Width: 1280, Height: 720, Bitrate: "3000k"}},
	}, 8, nil)
	if err != nil {
		t.Fatalf("GenerateHLSWithProgress returned error: %v", err)
	}
	if len(result.VariantPlaylists) != 1 || result.VariantPlaylists["original"] == "" {
		t.Fatalf("unexpected HLS result: %#v", result)
	}
}

func writeExecutable(t *testing.T, path string, content string) {
	t.Helper()

	temporary, err := os.CreateTemp(filepath.Dir(path), ".executable-*")
	if err != nil {
		t.Fatalf("create executable %s: %v", path, err)
	}
	temporaryPath := temporary.Name()
	t.Cleanup(func() { _ = os.Remove(temporaryPath) })
	if _, err := temporary.WriteString(content); err != nil {
		_ = temporary.Close()
		t.Fatalf("write executable %s: %v", path, err)
	}
	if err := temporary.Chmod(0o755); err != nil {
		_ = temporary.Close()
		t.Fatalf("chmod executable %s: %v", path, err)
	}
	if err := temporary.Close(); err != nil {
		t.Fatalf("close executable %s: %v", path, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		t.Fatalf("install executable %s: %v", path, err)
	}
}
