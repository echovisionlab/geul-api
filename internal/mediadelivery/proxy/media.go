package proxy

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/minio/minio-go/v7"
)

type MediaProxy struct {
	store deliveryStore
}

func NewMediaProxy(client *minio.Client, bucket string) *MediaProxy {
	return newMediaProxy(newMinioDeliveryStore(client, bucket))
}

func newMediaProxy(store deliveryStore) *MediaProxy {
	return &MediaProxy{store: store}
}

func (p *MediaProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	resolved, err := resolvedDeliveryFromRequest(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
		p.serveRange(w, r, resolved, rangeHeader)
		return
	}
	if r.Method == http.MethodHead {
		p.writeFullResponseHeaders(w, resolved)
		return
	}
	body, err := p.store.Open(r.Context(), resolved.path.ObjectKey, nil)
	if err != nil {
		slog.Error("failed to open delivery object", "error", err, "kind", resolved.path.Kind, "fileId", resolved.path.FileID, "assetId", resolved.path.AssetID)
		http.Error(w, "Bad gateway", http.StatusBadGateway)
		return
	}
	defer func() { _ = body.Close() }()
	p.writeFullResponseHeaders(w, resolved)
	if _, err := io.Copy(w, body); err != nil {
		slog.Error("failed to stream delivery object", "error", err, "kind", resolved.path.Kind, "fileId", resolved.path.FileID, "assetId", resolved.path.AssetID)
	}
}

func (p *MediaProxy) writeFullResponseHeaders(w http.ResponseWriter, resolved *resolvedDelivery) {
	p.setResponseHeaders(w, resolved)
	w.Header().Set("Content-Length", strconv.FormatInt(resolved.stat.Size, 10))
	w.WriteHeader(http.StatusOK)
}

func (p *MediaProxy) serveRange(w http.ResponseWriter, r *http.Request, resolved *resolvedDelivery, rangeHeader string) {
	objectKey := resolved.path.ObjectKey
	totalSize := resolved.stat.Size
	start, end, ok := parseSingleRange(rangeHeader, totalSize)
	if !ok {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", totalSize))
		http.Error(w, "Range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	if r.Method == http.MethodHead {
		p.writeRangeResponseHeaders(w, resolved, start, end)
		return
	}
	body, err := p.store.Open(r.Context(), objectKey, &byteRange{start: start, end: end})
	if err != nil {
		slog.Error("failed to open delivery range", "error", err, "kind", resolved.path.Kind, "fileId", resolved.path.FileID, "assetId", resolved.path.AssetID)
		http.Error(w, "Bad gateway", http.StatusBadGateway)
		return
	}
	defer func() { _ = body.Close() }()
	p.writeRangeResponseHeaders(w, resolved, start, end)
	if _, err := io.Copy(w, body); err != nil {
		slog.Error("failed to stream delivery range", "error", err, "kind", resolved.path.Kind, "fileId", resolved.path.FileID, "assetId", resolved.path.AssetID)
	}
}

func (p *MediaProxy) writeRangeResponseHeaders(
	w http.ResponseWriter,
	resolved *resolvedDelivery,
	start int64,
	end int64,
) {
	p.setResponseHeaders(w, resolved)
	w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, resolved.stat.Size))
	w.WriteHeader(http.StatusPartialContent)
}

func (p *MediaProxy) setResponseHeaders(w http.ResponseWriter, resolved *resolvedDelivery) {
	stat := resolved.stat
	w.Header().Set("Content-Type", stat.ContentType)
	w.Header().Set("Accept-Ranges", "bytes")
	if resolved.path.Kind == mediaauth.PathMediaHLS {
		setNoStoreCacheHeaders(w)
	} else {
		setDeliveryCacheHeaders(w, resolved.claims)
	}
	w.Header().Add("Vary", "Range")
	setDisposition(w, resolved)
	if stat.ETag != "" {
		w.Header().Set("ETag", fmt.Sprintf(`"%s"`, strings.Trim(stat.ETag, `"`)))
	}
	if !stat.LastModified.IsZero() {
		w.Header().Set("Last-Modified", stat.LastModified.UTC().Format(http.TimeFormat))
	}
}

func parseSingleRange(rangeHeader string, totalSize int64) (int64, int64, bool) {
	parts, ok := splitSingleRange(rangeHeader, totalSize)
	if !ok {
		return 0, 0, false
	}
	if parts[0] == "" {
		return parseSuffixRange(parts[1], totalSize)
	}
	return parseBoundedRange(parts, totalSize)
}

func splitSingleRange(rangeHeader string, totalSize int64) ([]string, bool) {
	if totalSize <= 0 || !strings.HasPrefix(rangeHeader, "bytes=") {
		return nil, false
	}
	parts := strings.Split(strings.TrimPrefix(rangeHeader, "bytes="), "-")
	return parts, len(parts) == 2 && (parts[0] != "" || parts[1] != "")
}

func parseSuffixRange(rawLength string, totalSize int64) (int64, int64, bool) {
	suffixLength, err := strconv.ParseInt(rawLength, 10, 64)
	if err != nil || suffixLength <= 0 {
		return 0, 0, false
	}
	start := int64(0)
	if suffixLength < totalSize {
		start = totalSize - suffixLength
	}
	return start, totalSize - 1, true
}

func parseBoundedRange(parts []string, totalSize int64) (int64, int64, bool) {
	parsedStart, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	end, ok := parseRangeEnd(parts[1], totalSize)
	if !ok {
		return 0, 0, false
	}
	if parsedStart < 0 || parsedStart >= totalSize || parsedStart > end {
		return 0, 0, false
	}
	if end >= totalSize {
		end = totalSize - 1
	}
	return parsedStart, end, true
}

func parseRangeEnd(rawEnd string, totalSize int64) (int64, bool) {
	if rawEnd == "" {
		return totalSize - 1, true
	}
	end, err := strconv.ParseInt(rawEnd, 10, 64)
	return end, err == nil
}

func setDisposition(w http.ResponseWriter, resolved *resolvedDelivery) {
	if resolved.claims == nil || resolved.claims.Purpose != mediaauth.PurposeDownload {
		w.Header().Set("Content-Disposition", "inline")
		return
	}
	w.Header().Set("Content-Disposition", contentDisposition("attachment", resolved.claims.Filename))
}

func contentDisposition(disposition, filename string) string {
	if filename == "" {
		return disposition
	}
	safeFilename := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' || r == '/' {
			return '_'
		}
		return r
	}, filename)
	fallback := strings.Map(func(r rune) rune {
		if r > 0x7e {
			return '_'
		}
		return r
	}, safeFilename)
	return fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`, disposition, fallback, url.PathEscape(safeFilename))
}
