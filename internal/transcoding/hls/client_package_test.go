package hls

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClientPackageRejectsExternalOrphanCycleAndInvalidDurations(t *testing.T) {
	segment := ClientObject{Size: 188}
	master := ClientObject{Size: 100, Playlist: []byte("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6,\nsegment.ts\n#EXT-X-ENDLIST\n")}
	objects := map[string]ClientObject{"master.m3u8": master, "segment.ts": segment}
	require.NoError(t, ValidateClientPackage(objects))
	require.NoError(t, ValidateClientDurations(objects, 6))
	require.Error(t, ValidateClientDurations(objects, 60))
	for _, test := range []struct{ name, playlist string }{
		{"external URI", "#EXTM3U\n#EXTINF:6,\nhttps://x/segment.ts\n#EXT-X-ENDLIST\n"},
		{"encryption key", "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"key\"\n#EXTINF:6,\nsegment.ts\n#EXT-X-ENDLIST\n"},
		{"playlist cycle", "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nmaster.m3u8\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			next := map[string]ClientObject{"master.m3u8": {Size: 100, Playlist: []byte(test.playlist)}, "segment.ts": segment}
			require.Error(t, ValidateClientPackage(next))
		})
	}
	objects["orphan.ts"] = segment
	require.Error(t, ValidateClientPackage(objects))
	delete(objects, "orphan.ts")
	for _, duration := range []string{"NaN", "Inf", "-6", "0"} {
		t.Run("duration "+duration, func(t *testing.T) {
			next := map[string]ClientObject{"master.m3u8": {Size: 100, Playlist: []byte("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:" + duration + ",\nsegment.ts\n#EXT-X-ENDLIST\n")}, "segment.ts": segment}
			require.Error(t, ValidateClientDurations(next, 6))
		})
	}
}
