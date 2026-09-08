package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

type proxyFakeS3Object struct {
	body        string
	contentType string
	metadata    map[string]string
	onRequest   func(*http.Request)
}

func newProxyFakeMinioServer(t *testing.T, objects map[string]proxyFakeS3Object) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/")
		obj, ok := objects[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if obj.onRequest != nil {
			obj.onRequest(r)
		}

		w.Header().Set("Content-Type", obj.contentType)
		w.Header().Set("Content-Length", strconv.Itoa(len(obj.body)))
		w.Header().Set("ETag", `"proxy-test-etag"`)
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		for key, value := range obj.metadata {
			w.Header().Set("X-Amz-Meta-"+key, value)
		}

		if r.Method == http.MethodHead {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
			writeProxyFakeRange(w, rangeHeader, obj.body)
			return
		}
		_, _ = io.WriteString(w, obj.body)
	}))
}

func writeProxyFakeRange(w http.ResponseWriter, rangeHeader, body string) {
	start, end, ok := parseProxyFakeRange(rangeHeader, len(body))
	if !ok {
		http.Error(w, "unsupported range", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
	w.Header().Set("Content-Range", "bytes "+strconv.Itoa(start)+"-"+strconv.Itoa(end)+"/"+strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = io.WriteString(w, body[start:end+1])
}

func parseProxyFakeRange(rangeHeader string, size int) (int, int, bool) {
	if !strings.HasPrefix(rangeHeader, "bytes=") {
		return 0, 0, false
	}
	parts := strings.Split(strings.TrimPrefix(rangeHeader, "bytes="), "-")
	if len(parts) != 2 {
		return 0, 0, false
	}
	start, ok := parseProxyFakeRangeStart(parts[0], parts[1], size)
	if !ok {
		return 0, 0, false
	}
	end, ok := parseProxyFakeRangeEnd(parts[0], parts[1], size)
	if !ok {
		return 0, 0, false
	}
	if start < 0 || start > end || end >= size {
		return 0, 0, false
	}
	return start, end, true
}

func parseProxyFakeRangeStart(rawStart, rawEnd string, size int) (int, bool) {
	if rawStart != "" {
		start, err := strconv.Atoi(rawStart)
		return start, err == nil
	}
	suffixLength, err := strconv.Atoi(rawEnd)
	if err != nil || suffixLength <= 0 {
		return 0, false
	}
	if suffixLength >= size {
		return 0, true
	}
	return size - suffixLength, true
}

func parseProxyFakeRangeEnd(rawStart, rawEnd string, size int) (int, bool) {
	if rawStart == "" || rawEnd == "" {
		return size - 1, true
	}
	end, err := strconv.Atoi(rawEnd)
	return end, err == nil
}
