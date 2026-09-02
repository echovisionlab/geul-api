//go:build integration

package testutil

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func RequireFFmpeg(t *testing.T) {
	t.Helper()

	_, err := exec.LookPath("ffmpeg")
	require.NoError(t, err, "ffmpeg is required for media integration tests")
	_, err = exec.LookPath("ffprobe")
	require.NoError(t, err, "ffprobe is required for media integration tests")
}

func RepositoryTestAudioMP3(t *testing.T) string {
	t.Helper()
	return requireRepositoryFixture(t, "testdata/media/release-audio-001.mp3")
}

func RepositoryTestImageJPEG(t *testing.T) string {
	t.Helper()
	return requireRepositoryFixture(t, "testdata/media/artist-gallery-001.jpeg")
}

func RepositoryTestVideoMP4(t *testing.T) string {
	t.Helper()
	return requireRepositoryFixture(t, "testdata/media/editor-video-001.mp4")
}

func RepositoryTestMeshGLB(t *testing.T) string {
	t.Helper()
	return requireRepositoryFixture(t, "testdata/media/triangle.glb")
}

func requireRepositoryFixture(t *testing.T, relativePath string) string {
	t.Helper()

	path := appIntegrationRepoPath(relativePath)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.False(t, info.IsDir(), "repository fixture must be a file: %s", path)
	require.Greater(t, info.Size(), int64(0), "repository fixture must not be empty: %s", path)
	return path
}

func GenerateTestAudioWAV(t *testing.T, dir string, durationSec float64) string {
	t.Helper()
	RequireFFmpeg(t)

	outPath := filepath.Join(dir, fmt.Sprintf("test-audio-%dms.wav", int(durationSec*1000)))
	runFFmpeg(t,
		"-f", "lavfi",
		"-i", fmt.Sprintf("sine=frequency=440:duration=%g", durationSec),
		"-ac", "1",
		"-ar", "44100",
		outPath,
	)
	return outPath
}

func GenerateTestVideoMP4(t *testing.T, dir string, durationSec float64) string {
	t.Helper()
	RequireFFmpeg(t)

	outPath := filepath.Join(dir, fmt.Sprintf("test-video-%dms.mp4", int(durationSec*1000)))
	runFFmpeg(t,
		"-f", "lavfi",
		"-i", fmt.Sprintf("testsrc=size=640x360:rate=24:duration=%g", durationSec),
		"-f", "lavfi",
		"-i", fmt.Sprintf("sine=frequency=330:duration=%g", durationSec),
		"-c:v", "libx264",
		"-pix_fmt", "yuv420p",
		"-c:a", "aac",
		"-shortest",
		outPath,
	)
	return outPath
}

func GenerateTestImagePNG(t *testing.T, dir string) string {
	t.Helper()
	RequireFFmpeg(t)

	outPath := filepath.Join(dir, "test-image.png")
	runFFmpeg(t,
		"-f", "lavfi",
		"-i", "color=c=#3366ff:size=640x360:d=0.1",
		"-frames:v", "1",
		outPath,
	)
	return outPath
}

func GenerateLargeTestGIFImage(t *testing.T, dir string, name string, sizeBytes int64) string {
	t.Helper()

	outPath := filepath.Join(dir, name)
	// Minimal valid 1x1 GIF89a. Extra bytes after the trailer are ignored by browsers,
	// which lets the fixture exercise multipart image upload without raster re-encoding.
	body := []byte{
		'G', 'I', 'F', '8', '9', 'a',
		0x01, 0x00, 0x01, 0x00,
		0x80, 0x00, 0x00,
		0x00, 0x00, 0x00,
		0xff, 0xff, 0xff,
		0x21, 0xf9, 0x04, 0x01, 0x00, 0x00, 0x00, 0x00,
		0x2c,
		0x00, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x01, 0x00,
		0x00,
		0x02, 0x02, 0x44, 0x01, 0x00,
		0x3b,
	}
	require.NoError(t, os.WriteFile(outPath, body, 0o644))
	PadFileToMinimumSize(t, outPath, sizeBytes)
	return outPath
}

func GenerateTestTextAttachment(t *testing.T, dir string, name string) string {
	t.Helper()

	outPath := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(outPath, []byte("integration attachment fixture\n"), 0o644))
	return outPath
}

func GenerateTestPDFAttachment(t *testing.T, dir string, name string) string {
	t.Helper()

	outPath := filepath.Join(dir, name)
	body := []byte("%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\n%%EOF\n")
	require.NoError(t, os.WriteFile(outPath, body, 0o644))
	return outPath
}

func GenerateInvalidBinaryUploadFixture(t *testing.T, dir string) string {
	t.Helper()

	outPath := filepath.Join(dir, "invalid-upload.bin")
	require.NoError(t, os.WriteFile(outPath, []byte{0x00, 0x01, 0x02, 0x03}, 0o644))
	return outPath
}

func GenerateInvalidProcessingMediaFixture(t *testing.T, dir string, blockType EditorMediaBlockType) string {
	t.Helper()

	var fileName string
	var body []byte
	switch blockType {
	case EditorMediaBlockTypeAudio:
		fileName = "invalid-processing-audio.mp3"
		body = append([]byte("ID3\x03\x00\x00\x00\x00\x00\x10"), []byte("not decodable mp3 frames\n")...)
	case EditorMediaBlockTypeVideo:
		fileName = "invalid-processing-video.mp4"
		body = []byte{
			0x00, 0x00, 0x00, 0x18,
			'f', 't', 'y', 'p',
			'i', 's', 'o', 'm',
			0x00, 0x00, 0x00, 0x01,
			'i', 's', 'o', 'm',
			'a', 'v', 'c', '1',
			'n', 'o', 't', ' ',
			'a', ' ',
			'd', 'e', 'c', 'o', 'd', 'a', 'b', 'l', 'e', ' ',
			'm', 'p', '4', '\n',
		}
	default:
		t.Fatalf("unsupported processing failure block type: %s", blockType)
	}

	outPath := filepath.Join(dir, fileName)
	require.NoError(t, os.WriteFile(outPath, body, 0o644))
	return outPath
}

func GenerateFailingFFmpegExecutable(t *testing.T, dir string) string {
	t.Helper()

	outPath := filepath.Join(dir, "ffmpeg-fail")
	script := "#!/bin/sh\nprintf '%s\\n' 'forced waveform ffmpeg failure' >&2\nexit 1\n"
	require.NoError(t, os.WriteFile(outPath, []byte(script), 0o755))
	return outPath
}

func GenerateOversizedTrackAudioFixture(t *testing.T, dir string) string {
	t.Helper()
	return generateSparseUploadFixture(t, filepath.Join(dir, "oversized-track.wav"), 4*1024*1024*1024+1)
}

func GenerateOversizedEditorMediaFixture(t *testing.T, blockType EditorMediaBlockType) string {
	t.Helper()

	dir := t.TempDir()
	switch blockType {
	case EditorMediaBlockTypeAudio:
		return generateSparseUploadFixture(t, filepath.Join(dir, "oversized-audio.wav"), 4*1024*1024*1024+1)
	case EditorMediaBlockTypeVideo:
		return generateSparseUploadFixture(t, filepath.Join(dir, "oversized-video.mp4"), 8*1024*1024*1024+1)
	case EditorMediaBlockTypeImage:
		return generateSparseUploadFixture(t, filepath.Join(dir, "oversized-image.jpg"), 20*1024*1024+1)
	case EditorMediaBlockTypeAttachment:
		return generateSparseUploadFixture(t, filepath.Join(dir, "oversized-attachment.txt"), 500*1024*1024+1)
	default:
		t.Fatalf("unsupported editor media block type: %s", blockType)
		return ""
	}
}

func generateSparseUploadFixture(t *testing.T, path string, sizeBytes int64) string {
	t.Helper()

	file, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(sizeBytes))
	require.NoError(t, file.Close())
	return path
}

func GenerateLargeTestTextAttachment(t *testing.T, dir string, name string, sizeBytes int64) string {
	t.Helper()

	outPath := filepath.Join(dir, name)
	file, err := os.Create(outPath)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, file.Close())
	}()

	chunk := bytes.Repeat([]byte("integration attachment fixture line\n"), 1024)
	var written int64
	for written < sizeBytes {
		remaining := sizeBytes - written
		next := chunk
		if remaining < int64(len(next)) {
			next = next[:remaining]
		}
		n, writeErr := file.Write(next)
		require.NoError(t, writeErr)
		written += int64(n)
	}
	return outPath
}

func PadFileToMinimumSize(t *testing.T, path string, minSizeBytes int64) {
	t.Helper()

	info, err := os.Stat(path)
	require.NoError(t, err)
	if info.Size() >= minSizeBytes {
		return
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, file.Close())
	}()

	padding := bytes.Repeat([]byte{0}, 1024*1024)
	remaining := minSizeBytes - info.Size()
	for remaining > 0 {
		next := padding
		if remaining < int64(len(next)) {
			next = next[:remaining]
		}
		n, writeErr := file.Write(next)
		require.NoError(t, writeErr)
		remaining -= int64(n)
	}
}

func runFFmpeg(t *testing.T, args ...string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fullArgs := append([]string{"-y", "-hide_banner", "-loglevel", "error"}, args...)
	cmd := exec.CommandContext(ctx, "ffmpeg", fullArgs...)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg command failed: %v\nstderr:\n%s", err, stderr.String())
	}
}
