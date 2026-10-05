package hls

import (
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ClientObject describes a bounded browser upload; only playlists need bytes.
type ClientObject struct {
	Size     int64
	Playlist []byte
}
type clientPackageFS map[string]ClientObject

func (f clientPackageFS) ReadDir(string) ([]os.DirEntry, error) {
	return nil, fmt.Errorf("directory access is unsupported")
}
func (f clientPackageFS) ReadFile(name string) ([]byte, error) {
	v, ok := f[name]
	if !ok {
		return nil, fmt.Errorf("missing playlist %s", name)
	}
	return v.Playlist, nil
}

// ValidateClientPackage applies the same closed, local MPEG-TS graph policy as worker packages.
func ValidateClientPackage(objects map[string]ClientObject) error {
	files := make(map[string]hlsFile, len(objects))
	for name, obj := range objects {
		if name == "" || name != filepath.Base(name) || url.PathEscape(name) != name || strings.HasSuffix(strings.ToLower(name), ".tmp") || obj.Size <= 0 {
			return fmt.Errorf("invalid HLS object %q", name)
		}
		mime, err := hlsContentType(name)
		if err != nil {
			return err
		}
		files[name] = hlsFile{name: name, path: name, size: obj.Size, contentType: mime}
	}
	if _, ok := files[MasterManifestName]; !ok {
		return fmt.Errorf("missing HLS master manifest")
	}
	return validateHLSReferences(clientPackageFS(objects), files)
}

// ValidateClientDurations checks every media rendition against the declared source duration.
func ValidateClientDurations(objects map[string]ClientObject, expected float64) error {
	if expected <= 0 || math.IsNaN(expected) || math.IsInf(expected, 0) {
		return fmt.Errorf("invalid declared duration")
	}
	for name, obj := range objects {
		if filepath.Ext(name) != ".m3u8" {
			continue
		}
		timing, err := clientPlaylistTiming(name, obj.Playlist)
		if err != nil {
			return err
		}
		if timing.media && (timing.target < math.Ceil(timing.maxSegment) || math.Abs(timing.total-expected) > math.Max(0.5, expected*0.001)) {
			return fmt.Errorf("media rendition duration differs from plan: %s", name)
		}
	}
	return nil
}

type playlistTiming struct {
	total      float64
	maxSegment float64
	target     float64
	media      bool
}

func clientPlaylistTiming(name string, data []byte) (playlistTiming, error) {
	var timing playlistTiming
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#EXT-X-TARGETDURATION:") {
			target, err := strconv.Atoi(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"))
			if err != nil || target <= 0 {
				return timing, fmt.Errorf("invalid target duration in %s", name)
			}
			timing.target = float64(target)
		}
		if strings.HasPrefix(line, "#EXTINF:") {
			timing.media = true
			value := strings.SplitN(strings.TrimPrefix(line, "#EXTINF:"), ",", 2)[0]
			duration, err := strconv.ParseFloat(value, 64)
			if err != nil || duration <= 0 || math.IsNaN(duration) || math.IsInf(duration, 0) {
				return timing, fmt.Errorf("invalid segment duration in %s", name)
			}
			timing.total += duration
			timing.maxSegment = math.Max(timing.maxSegment, duration)
		}
	}
	return timing, nil
}
