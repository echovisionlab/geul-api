package ffmpeg

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewExecutorAndWorkDirectoryFailures(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "missing")
	_, err := NewExecutor(missing, missing, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "ffmpeg not found") {
		t.Fatalf("ffmpeg lookup error = %v", err)
	}
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewExecutor(truePath, missing, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "ffprobe not found") {
		t.Fatalf("ffprobe lookup error = %v", err)
	}
	parentFile := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parentFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = NewExecutor(truePath, truePath, filepath.Join(parentFile, "temp"))
	if err == nil || !strings.Contains(err.Error(), "create temp directory") {
		t.Fatalf("temp directory error = %v", err)
	}

	executor := &Executor{tempDir: parentFile}
	_, err = executor.CreateJobWorkDir("job")
	if err == nil || !strings.Contains(err.Error(), "create work directory") {
		t.Fatalf("work directory error = %v", err)
	}
}

func TestCleanupStaleWorkDirectoryBoundaries(t *testing.T) {
	t.Parallel()
	executor := &Executor{tempDir: filepath.Join(t.TempDir(), "missing")}
	removed, err := executor.CleanupStaleWorkDirs()
	if err != nil || removed != 0 {
		t.Fatalf("missing cleanup = %d, %v", removed, err)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	executor.tempDir = file
	_, err = executor.CleanupStaleWorkDirs()
	if err == nil || !strings.Contains(err.Error(), "read temp directory") {
		t.Fatalf("read cleanup error = %v", err)
	}

	entries := []os.DirEntry{namedDirEntry("one"), namedDirEntry("two")}
	removed, err = cleanupStaleWorkDirs("root", func(string) ([]os.DirEntry, error) {
		return entries, nil
	}, func(path string) error {
		if strings.HasSuffix(path, "two") {
			return errors.New("remove")
		}
		return nil
	})
	if err == nil || removed != 1 || !strings.Contains(err.Error(), "remove stale temp path") {
		t.Fatalf("remove cleanup = %d, %v", removed, err)
	}
}

type namedDirEntry string

func (e namedDirEntry) Name() string             { return string(e) }
func (namedDirEntry) IsDir() bool                { return true }
func (namedDirEntry) Type() os.FileMode          { return os.ModeDir }
func (namedDirEntry) Info() (os.FileInfo, error) { return nil, nil }

func TestProbeAndValidationFailureBoundaries(t *testing.T) {
	t.Parallel()
	binDir := t.TempDir()
	probePath := filepath.Join(binDir, "ffprobe")
	writeExecutable(t, probePath, `#!/bin/sh
case "$*" in
  *probe-error*) exit 7 ;;
  *zero*) echo '{"format":{"duration":"0"},"streams":[]}' ;;
  *no-audio*) echo '{"format":{"duration":"1"},"streams":[]}' ;;
  *audio-video*) echo '{"format":{"duration":"1"},"streams":[{"codec_name":"aac","sample_rate":"8000","channels":1},{"codec_name":"h264","width":10,"height":10}]}' ;;
  *no-dimensions*) echo '{"format":{"duration":"1"},"streams":[{"codec_name":"h264"}]}' ;;
  *invalid-json*) echo '{' ;;
  *) echo '{"format":{"duration":"1"},"streams":[{"codec_name":"aac","sample_rate":"bad","channels":1}]}' ;;
esac
	`)
	executor := &Executor{ffprobePath: probePath}
	assertProbeFailures(t, executor)
	assertAudioValidationFailures(t, executor)
	assertVideoValidationFailures(t, executor)
	assertProbeNumericFallbacks(t, executor)
}

func assertProbeFailures(t *testing.T, executor *Executor) {
	t.Helper()
	if _, err := executor.Probe(context.Background(), "probe-error"); err == nil {
		t.Fatal("expected probe command error")
	}
	if _, err := executor.Probe(context.Background(), "invalid-json"); err == nil {
		t.Fatal("expected probe parse error")
	}
	if _, err := parseProbeOutput([]byte("{")); err == nil {
		t.Fatal("expected direct probe parse error")
	}
}

func assertAudioValidationFailures(t *testing.T, executor *Executor) {
	t.Helper()
	for _, path := range []string{"probe-error", "zero", "no-audio", "audio-video"} {
		if err := executor.ValidateAudioFile(context.Background(), path); err == nil {
			t.Fatalf("expected audio validation error for %s", path)
		}
	}
}

func assertVideoValidationFailures(t *testing.T, executor *Executor) {
	t.Helper()
	for _, path := range []string{"probe-error", "zero", "no-dimensions"} {
		if err := executor.ValidateVideoFile(context.Background(), path); err == nil {
			t.Fatalf("expected video validation error for %s", path)
		}
	}
}

func assertProbeNumericFallbacks(t *testing.T, executor *Executor) {
	t.Helper()
	probe, err := executor.Probe(context.Background(), "bad-sample-rate")
	if err != nil || probe.SampleRate != 0 || !probe.HasAudio {
		t.Fatalf("bad sample rate probe = %#v, %v", probe, err)
	}
	probe, err = parseProbeOutput([]byte(`{"format":{"duration":"bad","bit_rate":"bad"},"streams":[]}`))
	if err != nil || probe.DurationSeconds != 0 || probe.Bitrate != 0 {
		t.Fatalf("invalid numeric probe = %#v, %v", probe, err)
	}
}

func TestAudioHLSGenerationSuccessDefaultsOptionsAndErrors(t *testing.T) {
	t.Parallel()
	binDir := t.TempDir()
	success := filepath.Join(binDir, "success")
	failure := filepath.Join(binDir, "failure")
	argsLog := filepath.Join(binDir, "args.log")
	writeExecutable(t, success, "#!/bin/sh\nprintf '%s\\n' \"$@\" > \""+argsLog+"\"\n")
	writeExecutable(t, failure, "#!/bin/sh\necho failed >&2\nexit 7\n")

	var progress []int
	executor := &Executor{
		ffmpegPath: success,
		listenProgress: func(_ context.Context, _ string, _ float64, callback ProgressCallback) {
			if callback != nil {
				callback(25)
			}
		},
	}
	result, err := executor.GenerateAudioHLSWithProgress(context.Background(), "input", filepath.Join(t.TempDir(), "default"), AudioHLSOptions{}, 10, func(value int) {
		progress = append(progress, value)
	})
	if err != nil || filepath.Base(result.MasterPlaylist) != "master.m3u8" || !reflect.DeepEqual(progress, []int{25, 100}) {
		t.Fatalf("audio HLS = %#v, %v, progress=%v", result, err, progress)
	}
	assertAudioHLSOptionsAndFailures(t, executor, argsLog, failure)
}

func assertAudioHLSOptionsAndFailures(t *testing.T, executor *Executor, argsLog, failure string) {
	t.Helper()
	_, err := executor.GenerateAudioHLSWithProgress(context.Background(), "input", filepath.Join(t.TempDir(), "options"), AudioHLSOptions{
		SegmentDuration: 8,
		Bitrate:         "96k",
		SampleRate:      48000,
		Channels:        1,
	}, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	commandArgs, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, removedOption := range []string{"-ss", "-t", "-af"} {
		if slices.Contains(strings.Fields(string(commandArgs)), removedOption) {
			t.Fatalf("audio HLS command unexpectedly includes removed option %q: %s", removedOption, commandArgs)
		}
	}

	parentFile := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parentFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = executor.GenerateAudioHLSWithProgress(context.Background(), "input", filepath.Join(parentFile, "output"), AudioHLSOptions{}, 10, nil)
	if err == nil || !strings.Contains(err.Error(), "create output directory") {
		t.Fatalf("audio mkdir error = %v", err)
	}
	executor.ffmpegPath = failure
	_, err = executor.GenerateAudioHLSWithProgress(context.Background(), "input", filepath.Join(t.TempDir(), "failure"), AudioHLSOptions{}, 10, nil)
	if err == nil || !strings.Contains(err.Error(), "audio HLS generation failed") {
		t.Fatalf("audio command error = %v", err)
	}
}

func TestThumbnailSpectrogramAndHLSCommandFailures(t *testing.T) {
	t.Parallel()
	binDir := t.TempDir()
	failure := filepath.Join(binDir, "failure")
	probeFailure := filepath.Join(binDir, "probe-failure")
	writeExecutable(t, failure, "#!/bin/sh\nexit 7\n")
	writeExecutable(t, probeFailure, "#!/bin/sh\nexit 7\n")
	executor := &Executor{ffmpegPath: failure, ffprobePath: probeFailure, listenProgress: func(context.Context, string, float64, ProgressCallback) {}}
	if err := executor.GenerateVideoThumbnail(context.Background(), "input", "output.webp", VideoThumbnailOptions{}); err == nil {
		t.Fatal("expected thumbnail command error")
	}
	if err := executor.GenerateAudioSpectrogramWithProgress(context.Background(), "input", "output.png", SpectrogramOptions{}, 0, nil); err == nil {
		t.Fatal("expected spectrogram command error")
	}
	_, err := executor.GenerateHLSWithProgress(context.Background(), "input", t.TempDir(), HLSOptions{Resolutions: []HLSResolution{{Name: "x", Width: 1, Height: 1}}}, 1, nil)
	if err == nil || !strings.Contains(err.Error(), "failed to probe input") {
		t.Fatalf("HLS probe error = %v", err)
	}
}

func TestVideoHLSGenerationCoversAudioVariantsProgressAndOutputErrors(t *testing.T) {
	t.Parallel()
	binDir := t.TempDir()
	probe := filepath.Join(binDir, "ffprobe")
	success := filepath.Join(binDir, "success")
	failure := filepath.Join(binDir, "failure")
	writeExecutable(t, probe, "#!/bin/sh\necho '{\"format\":{\"duration\":\"10\"},\"streams\":[{\"codec_name\":\"h264\",\"width\":1920,\"height\":1080},{\"codec_name\":\"aac\",\"sample_rate\":\"44100\",\"channels\":2}]}'\n")
	writeExecutable(t, success, "#!/bin/sh\nexit 0\n")
	writeExecutable(t, failure, "#!/bin/sh\nexit 7\n")
	var progress []int
	executor := &Executor{
		ffmpegPath:  success,
		ffprobePath: probe,
		listenProgress: func(_ context.Context, _ string, _ float64, callback ProgressCallback) {
			if callback != nil {
				callback(40)
			}
		},
	}
	result, err := executor.GenerateHLSWithProgress(context.Background(), "input", filepath.Join(t.TempDir(), "hls"), HLSOptions{
		Resolutions: []HLSResolution{
			{Name: "360p", Width: 640, Height: 360, Bitrate: "800k"},
			{Name: "720p", Width: 1280, Height: 720, Bitrate: "3000k"},
		},
		SegmentDuration: 8,
		AudioBitrate:    "96k",
	}, 10, func(value int) { progress = append(progress, value) })
	if err != nil || len(result.VariantPlaylists) != 2 || !reflect.DeepEqual(progress, []int{40, 100}) {
		t.Fatalf("video HLS = %#v, %v, progress=%v", result, err, progress)
	}

	parentFile := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parentFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = executor.GenerateHLSWithProgress(context.Background(), "input", filepath.Join(parentFile, "output"), HLSOptions{Resolutions: []HLSResolution{{Name: "x", Width: 1, Height: 1}}}, 1, nil)
	if err == nil || !strings.Contains(err.Error(), "create output directory") {
		t.Fatalf("HLS mkdir error = %v", err)
	}
	executor.ffmpegPath = failure
	_, err = executor.GenerateHLSWithProgress(context.Background(), "input", filepath.Join(t.TempDir(), "failure"), HLSOptions{Resolutions: []HLSResolution{{Name: "x", Width: 1, Height: 1}}}, 1, nil)
	if err == nil || !strings.Contains(err.Error(), "HLS generation failed") {
		t.Fatalf("HLS command error = %v", err)
	}
}

func TestSpectrogramSuccessDefaultsAndProgress(t *testing.T) {
	t.Parallel()
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	writeExecutable(t, bin, "#!/bin/sh\nexit 0\n")
	var progress []int
	executor := &Executor{ffmpegPath: bin, listenProgress: func(_ context.Context, _ string, _ float64, callback ProgressCallback) {
		callback(30)
	}}
	if err := executor.GenerateAudioSpectrogramWithProgress(context.Background(), "input", "output.png", SpectrogramOptions{}, 0, func(value int) {
		progress = append(progress, value)
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(progress, []int{30, 100}) {
		t.Fatalf("progress = %v", progress)
	}
	if DefaultSpectrogramOptions().Width != 1600 || DefaultAudioHLSOptions().Bitrate != "192k" {
		t.Fatal("unexpected defaults")
	}
	args := buildAudioSpectrogramArgs("input", "output", SpectrogramOptions{}, 0, "")
	if strings.Contains(strings.Join(args, " "), "-progress") {
		t.Fatalf("unexpected progress args: %v", args)
	}
}

func TestWaveformGenerationAndCommandBoundaries(t *testing.T) {
	t.Parallel()
	binDir := t.TempDir()
	probe := filepath.Join(binDir, "ffprobe")
	pcm := filepath.Join(binDir, "pcm")
	writeExecutable(t, probe, `#!/bin/sh
case "$*" in
  *probe-error*) exit 7 ;;
  *zero*) echo '{"format":{"duration":"0"},"streams":[{"codec_name":"aac","sample_rate":"8000","channels":1}]}' ;;
  *) echo '{"format":{"duration":"0.00025"},"streams":[{"codec_name":"aac","sample_rate":"8000","channels":1}]}' ;;
esac
`)
	writeExecutable(t, pcm, "#!/bin/sh\nprintf '\\001\\000\\002\\000'\n")
	executor := &Executor{ffmpegPath: pcm, ffprobePath: probe}
	peaks, err := executor.GeneratePeaks(context.Background(), "audio")
	if err != nil || len(peaks) != 1 || len(peaks[0]) != 2048 {
		t.Fatalf("GeneratePeaks = %#v, %v", peaks, err)
	}
	peaks, err = executor.generateWaveformPeaks(context.Background(), "audio", WaveformOptions{})
	if err != nil || len(peaks) != 1 {
		t.Fatalf("generateWaveformPeaks = %#v, %v", peaks, err)
	}
	assertWaveformGenerationErrors(t, executor)
	assertWaveformOptionBoundaries(t)
}

func assertWaveformGenerationErrors(t *testing.T, executor *Executor) {
	t.Helper()
	if _, err := executor.generateWaveformPeaks(context.Background(), "probe-error", WaveformOptions{}); err == nil {
		t.Fatal("expected waveform probe error")
	}
	if _, err := executor.generateWaveformPeaks(context.Background(), "zero", WaveformOptions{}); err == nil {
		t.Fatal("expected waveform zero duration error")
	}
	executor.ffmpegPath = filepath.Join(t.TempDir(), "missing")
	if _, err := executor.generateWaveformPeaks(context.Background(), "audio", WaveformOptions{}); err == nil {
		t.Fatal("expected waveform command error")
	}
}

func assertWaveformOptionBoundaries(t *testing.T) {
	t.Helper()
	normalized := normalizeWaveformOptions(WaveformOptions{})
	if normalized.NumSamples != 2048 || normalized.SampleRate != 8000 || normalized.Precision != 10000 {
		t.Fatalf("normalized = %#v", normalized)
	}
	for _, tc := range []struct{ source, requested, want int }{{2, 1, 1}, {2, 3, 2}, {1, 0, 1}, {2, 0, 2}} {
		if got := determineWaveformChannels(tc.source, tc.requested); got != tc.want {
			t.Fatalf("channels = %d, want %d", got, tc.want)
		}
	}
}

func TestReadWaveformCommandAndPCMReaderErrors(t *testing.T) {
	t.Parallel()
	command := exec.CommandContext(context.Background(), "sh", "-c", "printf '\\001\\000\\002\\000'")
	configureGracefulCommand(command, time.Second)
	peaks, err := readWaveformCommand(command, 1, 2, 2, 10000)
	if err != nil || len(peaks) != 1 {
		t.Fatalf("read command = %#v, %v", peaks, err)
	}
	assertWaveformCommandFailures(t)
	assertPCMReaderFailures(t)
	assertWaveformBucketRanges(t)
}

func assertWaveformCommandFailures(t *testing.T) {
	t.Helper()
	command := exec.Command("true")
	command.Stdout = io.Discard
	if _, err := readWaveformCommand(command, 1, 1, 1, 1); err == nil || !strings.Contains(err.Error(), "stdout") {
		t.Fatalf("stdout error = %v", err)
	}
	command = exec.Command(filepath.Join(t.TempDir(), "missing"))
	if _, err := readWaveformCommand(command, 1, 1, 1, 1); err == nil || !strings.Contains(err.Error(), "start ffmpeg") {
		t.Fatalf("start error = %v", err)
	}
	command = exec.Command("sh", "-c", "printf '\\001\\000\\002'")
	if _, err := readWaveformCommand(command, 1, 1, 1, 1); err == nil || !strings.Contains(err.Error(), "incomplete frame") {
		t.Fatalf("read error = %v", err)
	}
	command = exec.Command("sh", "-c", "printf '\\001\\000'; exit 7")
	if _, err := readWaveformCommand(command, 1, 1, 1, 1); err == nil || !strings.Contains(err.Error(), "extraction failed") {
		t.Fatalf("wait error = %v", err)
	}
}

func assertPCMReaderFailures(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name                           string
		source                         io.Reader
		channels, frames, length, prec int
		want                           string
	}{
		{name: "channels", source: strings.NewReader(""), channels: 0, frames: 1, length: 1, prec: 1, want: "channel count"},
		{name: "frames", source: strings.NewReader(""), channels: 1, frames: 0, length: 1, prec: 1, want: "no frames"},
		{name: "length", source: strings.NewReader(""), channels: 1, frames: 1, length: 0, prec: 1, want: "must be positive"},
		{name: "reader", source: errorReader{}, channels: 1, frames: 1, length: 1, prec: 1, want: "read waveform pcm"},
		{name: "empty", source: strings.NewReader(""), channels: 1, frames: 1, length: 1, prec: 1, want: "pcm is empty"},
		{name: "leftover", source: strings.NewReader("xyz"), channels: 1, frames: 1, length: 1, prec: 1, want: "incomplete frame"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildWaveformPeaksFromPCMReader(tc.source, tc.channels, tc.frames, tc.length, tc.prec)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("PCM reader error = %v", err)
			}
		})
	}
}

func assertWaveformBucketRanges(t *testing.T) {
	t.Helper()
	if buildWaveformBucketRanges(0, 1) != nil || buildWaveformBucketRanges(1, 0) != nil {
		t.Fatal("invalid ranges must be nil")
	}
	if ranges := buildWaveformBucketRanges(1, 3); len(ranges) != 3 {
		t.Fatalf("ranges = %#v", ranges)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("read") }

func TestGracefulCommandConfiguration(t *testing.T) {
	t.Parallel()
	command := exec.Command("true")
	configureGracefulCommand(command, 123*time.Millisecond)
	if command.WaitDelay != 123*time.Millisecond || command.Cancel == nil {
		t.Fatal("command was not configured")
	}
	if err := command.Cancel(); err != nil {
		t.Fatalf("cancel before start = %v", err)
	}
	sleep := exec.Command("sh", "-c", "sleep 5")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	if err := interruptProcess(sleep.Process); err != nil {
		t.Fatal(err)
	}
	_ = sleep.Wait()
}

type scriptedListener struct {
	accept func() (net.Conn, error)
	closed chan struct{}
	once   sync.Once
}

func (l *scriptedListener) Accept() (net.Conn, error) { return l.accept() }
func (l *scriptedListener) Close() error {
	l.once.Do(func() {
		if l.closed != nil {
			close(l.closed)
		}
	})
	return nil
}
func (*scriptedListener) Addr() net.Addr { return dummyAddr("progress") }

type dummyAddr string

func (a dummyAddr) Network() string { return string(a) }
func (a dummyAddr) String() string  { return string(a) }

func TestProgressListenerBoundaries(t *testing.T) {
	t.Parallel()
	listenFFmpegProgress(context.Background(), "unused", 1, nil)
	listenFFmpegProgressWith(context.Background(), "unused", 1, func(int) {}, func(string, string) (net.Listener, error) {
		return nil, errors.New("listen")
	}, time.Second)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	blocking := &scriptedListener{closed: make(chan struct{})}
	blocking.accept = func() (net.Conn, error) {
		<-blocking.closed
		return nil, errors.New("closed")
	}
	listenFFmpegProgressWith(cancelled, "unused", 1, func(int) {}, func(string, string) (net.Listener, error) {
		return blocking, nil
	}, time.Second)

	acceptFailure := &scriptedListener{accept: func() (net.Conn, error) { return nil, errors.New("accept") }}
	listenFFmpegProgressWith(context.Background(), "unused", 1, func(int) {}, func(string, string) (net.Listener, error) {
		return acceptFailure, nil
	}, time.Second)

	timeoutListener := &scriptedListener{closed: make(chan struct{})}
	timeoutListener.accept = func() (net.Conn, error) {
		<-timeoutListener.closed
		return nil, errors.New("closed")
	}
	listenFFmpegProgressWith(context.Background(), "unused", 1, func(int) {}, func(string, string) (net.Listener, error) {
		return timeoutListener, nil
	}, time.Millisecond)

	server, client := net.Pipe()
	go func() {
		_, _ = io.WriteString(client, "progress=end\n")
		_ = client.Close()
	}()
	success := &scriptedListener{accept: func() (net.Conn, error) { return server, nil }}
	listenFFmpegProgressWith(context.Background(), "unused", 1, func(int) {}, func(string, string) (net.Listener, error) {
		return success, nil
	}, time.Second)
}

func TestProgressReaderBoundaries(t *testing.T) {
	t.Parallel()
	var progress []int
	readFFmpegProgress(context.Background(), strings.NewReader(strings.Join([]string{
		"out_time_ms=bad",
		"out_time_ms=-1000000",
		"out_time_ms=50000000",
		"out_time_ms=50000000",
		"out_time_ms=200000000",
		"ignored",
		"progress=end",
	}, "\n")), 100, func(value int) { progress = append(progress, value) })
	if !reflect.DeepEqual(progress, []int{50, 99}) {
		t.Fatalf("progress = %v", progress)
	}
	readFFmpegProgress(context.Background(), strings.NewReader("out_time_ms=100\n"), 0, func(int) {
		t.Fatal("zero duration must not report")
	})
	ctx, stop := context.WithCancel(context.Background())
	stop()
	readFFmpegProgress(ctx, strings.NewReader("out_time_ms=100\n"), 1, func(int) {
		t.Fatal("cancelled reader must not report")
	})
	readFFmpegProgress(context.Background(), errorReader{}, 1, func(int) {})
}

func TestCalculateTimeoutBounds(t *testing.T) {
	t.Parallel()
	if CalculateTimeout(1, 5) != 5*time.Minute {
		t.Fatal("minimum timeout failed")
	}
	if CalculateTimeout(3600, 5) != 25*time.Minute {
		t.Fatal("maximum timeout failed")
	}
	if CalculateTimeout(200, 5) != 10*time.Minute {
		t.Fatal("estimated timeout failed")
	}
}
