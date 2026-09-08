package hls

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"github.com/stretchr/testify/require"
)

type recordingUploadStorage struct {
	keys        []string
	mimes       []string
	uploadErr   error
	uploadErrAt int
	uploadCalls int
}

type controlledHLSFileSystem struct {
	entries  []os.DirEntry
	readAt   int
	readCall int
}

func (f *controlledHLSFileSystem) ReadDir(string) ([]os.DirEntry, error) {
	return f.entries, nil
}

func (f *controlledHLSFileSystem) ReadFile(path string) ([]byte, error) {
	f.readCall++
	if f.readCall == f.readAt {
		return nil, errors.New("read failure")
	}
	return os.ReadFile(path)
}

type infoErrorDirEntry struct{ os.DirEntry }

func (infoErrorDirEntry) Info() (os.FileInfo, error) {
	return nil, errors.New("info failure")
}

func (s *recordingUploadStorage) Download(context.Context, string, string) error { return nil }
func (s *recordingUploadStorage) Upload(_ context.Context, key, _ string, mime string) error {
	s.uploadCalls++
	if s.uploadErr != nil && (s.uploadErrAt == 0 || s.uploadCalls == s.uploadErrAt) {
		return s.uploadErr
	}
	s.keys = append(s.keys, key)
	s.mimes = append(s.mimes, mime)
	return nil
}
func (s *recordingUploadStorage) UploadCompleted(ctx context.Context, key, localPath, mime string, _ []byte) error {
	return s.Upload(ctx, key, localPath, mime)
}
func (s *recordingUploadStorage) Completion(context.Context, string) ([]byte, bool, error) {
	return nil, false, nil
}

func TestInspectAndUploadAudioHLSPackagePublishesManifestLast(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, writeAudioHLSPackage(dir))

	pkg, err := Inspect(dir)
	require.NoError(t, err)
	require.Len(t, pkg.files, 1)
	require.Equal(t, "segment_000.ts", pkg.files[0].name)

	target := &commonv1.MediaGenerationWriteTarget{
		GenerationId: "generation-id",
		ObjectPrefix: "media/file-id/hls/generation-id",
	}
	storage := &recordingUploadStorage{}
	var progress []int
	result, err := Upload(context.Background(), storage, target, pkg, []byte("completion"), func(percent int) {
		progress = append(progress, percent)
	})
	require.NoError(t, err)
	require.Equal(t, []string{
		"media/file-id/hls/generation-id/segment_000.ts",
		"media/file-id/hls/generation-id/master.m3u8",
	}, storage.keys)
	require.Equal(t, []string{"video/mp2t", "application/vnd.apple.mpegurl"}, storage.mimes)
	require.Equal(t, []int{50, 100}, progress)
	require.Equal(t, "generation-id", result.GetGenerationId())
	require.Equal(t, int32(2), result.GetObjectCount())
	manifest := []byte("#EXTM3U\n#EXTINF:6,\nsegment_000.ts\n#EXT-X-ENDLIST\n")
	wantHash := sha256.Sum256(manifest)
	require.Equal(t, wantHash[:], result.GetManifestSha256())
	require.Equal(t, int64(len("segment")+len(manifest)), result.GetTotalSize())
}

func TestInspectVideoHLSPackageOrdersSegmentsBeforePlaylists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, writeVideoHLSPackage(dir))

	pkg, err := Inspect(dir)
	require.NoError(t, err)
	require.Equal(t, []string{"stream_360p_000.ts", "stream_360p_001.ts", "stream_360p.m3u8"}, []string{pkg.files[0].name, pkg.files[1].name, pkg.files[2].name})
}

func TestInspectHLSPackageAllowsRepeatedReachablePlaylist(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "segment.ts"), []byte("segment"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "media.m3u8"), []byte("#EXTM3U\n#EXTINF:6,\nsegment.ts\n#EXT-X-ENDLIST\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "master.m3u8"), []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nmedia.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=1\nmedia.m3u8\n"), 0o644))
	_, err := Inspect(dir)
	require.NoError(t, err)
}

func TestInspectHLSPackageValidatesUppercasePlaylistExtension(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SEGMENT.TS"), []byte("segment"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "MEDIA.M3U8"), []byte("#EXTM3U\n#EXTINF:6,\nSEGMENT.TS\n#EXT-X-ENDLIST\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "master.m3u8"), []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nMEDIA.M3U8\n"), 0o644))
	pkg, err := Inspect(dir)
	require.NoError(t, err)
	require.Equal(t, []string{"SEGMENT.TS", "MEDIA.M3U8"}, []string{pkg.files[0].name, pkg.files[1].name})
}

func TestInspectHLSPackageRejectsInvalidPackages(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{name: "empty", want: "HLS output directory is empty"},
		{name: "missing manifest", files: map[string]string{"segment.ts": "x"}, want: "HLS manifest master.m3u8 is missing"},
		{name: "empty object", files: map[string]string{"master.m3u8": ""}, want: "non-empty regular file"},
		{name: "temporary object", files: map[string]string{"master.m3u8.tmp": "x"}, want: "invalid HLS object name"},
		{name: "escaped object", files: map[string]string{"master.m3u8": "#EXTM3U\n#EXTINF:1,\nbad%2Fname.ts\n#EXT-X-ENDLIST\n", "bad%2Fname.ts": "x"}, want: "invalid HLS object name"},
		{name: "unsupported object", files: map[string]string{"master.m3u8": "#EXTM3U\n", "image.jpg": "x"}, want: "unsupported HLS object"},
		{name: "bad header", files: map[string]string{"master.m3u8": "bad\n"}, want: "missing #EXTM3U header"},
		{name: "empty playlist", files: map[string]string{"master.m3u8": "\n"}, want: "empty playlist"},
		{name: "no entries", files: map[string]string{"master.m3u8": "#EXTM3U\n#EXT-X-VERSION:3\n"}, want: "has no media entries"},
		{name: "uri tag", files: map[string]string{"master.m3u8": "#EXTM3U\n#EXT-X-KEY:URI=\"key\"\n"}, want: "URI-bearing HLS tags"},
		{name: "lowercase uri tag", files: map[string]string{"master.m3u8": "#EXTM3U\n#EXT-X-KEY:uri=\"key\"\n"}, want: "URI-bearing HLS tags"},
		{name: "remote reference", files: map[string]string{"master.m3u8": "#EXTM3U\nhttps://x.test/a.ts\n"}, want: "non-local HLS reference"},
		{name: "bare reference", files: map[string]string{"master.m3u8": "#EXTM3U\nfile.ts\n"}, want: "has no entry tag"},
		{name: "unsupported reference", files: map[string]string{"master.m3u8": "#EXTM3U\n#EXTINF:6,\nfile.mp4\n#EXT-X-ENDLIST\n"}, want: "unsupported HLS reference"},
		{name: "missing reference", files: map[string]string{"master.m3u8": "#EXTM3U\n#EXTINF:6,\nmissing.ts\n#EXT-X-ENDLIST\n"}, want: "is missing"},
		{name: "missing media uri", files: map[string]string{"master.m3u8": "#EXTM3U\n#EXTINF:6,\n#EXT-X-ENDLIST\n"}, want: "missing URI"},
		{name: "missing end list", files: map[string]string{"master.m3u8": "#EXTM3U\n#EXTINF:6,\nsegment.ts\n", "segment.ts": "x"}, want: "missing #EXT-X-ENDLIST"},
		{name: "missing variant uri", files: map[string]string{"master.m3u8": "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\n"}, want: "missing URI"},
		{name: "duplicate entry tag", files: map[string]string{"master.m3u8": "#EXTM3U\n#EXTINF:6,\n#EXTINF:6,\nsegment.ts\n#EXT-X-ENDLIST\n", "segment.ts": "x"}, want: "missing URI"},
		{name: "mixed playlist", files: map[string]string{"master.m3u8": "#EXTM3U\n#EXTINF:6,\nsegment.ts\n#EXT-X-STREAM-INF:BANDWIDTH=1\n", "segment.ts": "x"}, want: "mixes media segments and variants"},
		{name: "mixed variant playlist", files: map[string]string{"master.m3u8": "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nchild.m3u8\n#EXTINF:6,\n", "child.m3u8": "#EXTM3U\n#EXTINF:1,\nsegment.ts\n#EXT-X-ENDLIST\n", "segment.ts": "x"}, want: "mixes media segments and variants"},
		{name: "duplicate variant tag", files: map[string]string{"master.m3u8": "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\n#EXT-X-STREAM-INF:BANDWIDTH=2\nchild.m3u8\n", "child.m3u8": "#EXTM3U\n#EXTINF:1,\nsegment.ts\n#EXT-X-ENDLIST\n", "segment.ts": "x"}, want: "missing URI"},
		{name: "multivariant end list", files: map[string]string{"master.m3u8": "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nchild.m3u8\n#EXT-X-ENDLIST\n", "child.m3u8": "#EXTM3U\n#EXTINF:1,\nsegment.ts\n#EXT-X-ENDLIST\n", "segment.ts": "x"}, want: "must not contain #EXT-X-ENDLIST"},
		{name: "cycle", files: map[string]string{"master.m3u8": "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nchild.m3u8\n", "child.m3u8": "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nmaster.m3u8\n"}, want: "playlist cycle"},
		{name: "orphan", files: map[string]string{"master.m3u8": "#EXTM3U\n#EXTINF:6,\nsegment.ts\n#EXT-X-ENDLIST\n", "segment.ts": "x", "orphan.ts": "x"}, want: "unreferenced objects"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tc.files {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
			}
			_, err := Inspect(dir)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestInspectHLSPackageRejectsDirectoryAndSymlink(t *testing.T) {
	t.Parallel()
	for _, entry := range []string{"directory", "symlink"} {
		t.Run(entry, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "master.m3u8"), []byte("#EXTM3U\n"), 0o644))
			if entry == "directory" {
				require.NoError(t, os.Mkdir(filepath.Join(dir, "nested"), 0o755))
			} else {
				require.NoError(t, os.Symlink(filepath.Join(dir, "master.m3u8"), filepath.Join(dir, "linked.m3u8")))
			}
			_, err := Inspect(dir)
			require.ErrorContains(t, err, "regular files only")
		})
	}
}

func TestInspectHLSPackageReadAndScanErrors(t *testing.T) {
	t.Parallel()
	_, err := Inspect(filepath.Join(t.TempDir(), "missing"))
	require.ErrorContains(t, err, "read HLS output directory")

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "master.m3u8"), append([]byte("#EXTM3U\n#"), make([]byte, 70*1024)...), 0o644))
	_, err = Inspect(dir)
	require.Error(t, err)
}

func TestInspectHLSPackageFilesystemErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, writeAudioHLSPackage(dir))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	withInfoError := append([]os.DirEntry(nil), entries...)
	withInfoError[0] = infoErrorDirEntry{DirEntry: withInfoError[0]}
	_, err = inspectHLSPackageWithFS(&controlledHLSFileSystem{entries: withInfoError}, dir)
	require.ErrorContains(t, err, "stat HLS object")

	_, err = inspectHLSPackageWithFS(&controlledHLSFileSystem{entries: entries, readAt: 1}, dir)
	require.ErrorContains(t, err, "read HLS playlist")

	_, err = inspectHLSPackageWithFS(&controlledHLSFileSystem{entries: entries, readAt: 2}, dir)
	require.ErrorContains(t, err, "read HLS manifest")
}

func TestUploadHLSPackageReturnsUploadErrorAndSupportsNilProgress(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, writeAudioHLSPackage(dir))
	pkg, err := Inspect(dir)
	require.NoError(t, err)

	target := &commonv1.MediaGenerationWriteTarget{GenerationId: "generation", ObjectPrefix: "prefix"}
	_, err = Upload(context.Background(), &recordingUploadStorage{uploadErr: errors.New("boom")}, target, pkg, []byte("completion"), nil)
	require.ErrorContains(t, err, "upload HLS object")
}

func TestUploadHLSPackageDoesNotPublishManifestAfterIntermediateFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, writeVideoHLSPackage(dir))
	pkg, err := Inspect(dir)
	require.NoError(t, err)

	target := &commonv1.MediaGenerationWriteTarget{GenerationId: "generation", ObjectPrefix: "prefix"}
	storage := &recordingUploadStorage{uploadErr: errors.New("boom"), uploadErrAt: 3}
	_, err = Upload(context.Background(), storage, target, pkg, []byte("completion"), nil)
	require.ErrorContains(t, err, "upload HLS object stream_360p.m3u8")
	require.Equal(t, []string{"prefix/stream_360p_000.ts", "prefix/stream_360p_001.ts"}, storage.keys)
	require.NotContains(t, storage.keys, "prefix/master.m3u8")
}

func TestHLSContentType(t *testing.T) {
	t.Parallel()
	mime, err := hlsContentType("INDEX.M3U8")
	require.NoError(t, err)
	require.Equal(t, "application/vnd.apple.mpegurl", mime)
	mime, err = hlsContentType("SEGMENT.TS")
	require.NoError(t, err)
	require.Equal(t, "video/mp2t", mime)
	_, err = hlsContentType("file.mp4")
	require.ErrorContains(t, err, "unsupported HLS object")
}
