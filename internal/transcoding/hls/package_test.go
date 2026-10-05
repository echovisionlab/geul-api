package hls

import (
	"os"
	"path/filepath"
)

func writeAudioHLSPackage(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "segment_000.ts"), []byte("segment"), 0o644); err != nil {
		return err
	}
	manifest := "#EXTM3U\n#EXTINF:6,\nsegment_000.ts\n#EXT-X-ENDLIST\n"
	return os.WriteFile(filepath.Join(dir, MasterManifestName), []byte(manifest), 0o644)
}

func writeVideoHLSPackage(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, name := range []string{"stream_360p_000.ts", "stream_360p_001.ts"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("segment"), 0o644); err != nil {
			return err
		}
	}
	media := "#EXTM3U\n#EXTINF:1,\nstream_360p_000.ts\n#EXTINF:1,\nstream_360p_001.ts\n#EXT-X-ENDLIST\n"
	if err := os.WriteFile(filepath.Join(dir, "stream_360p.m3u8"), []byte(media), 0o644); err != nil {
		return err
	}
	master := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000\nstream_360p.m3u8\n"
	return os.WriteFile(filepath.Join(dir, MasterManifestName), []byte(master), 0o644)
}
