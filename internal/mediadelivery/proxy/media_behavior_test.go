package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/minio/minio-go/v7"
)

func TestParseSingleRange(t *testing.T) {
	tests := []struct {
		header string
		size   int64
		start  int64
		end    int64
		ok     bool
	}{
		{"bytes=2-5", 10, 2, 5, true},
		{"bytes=7-", 10, 7, 9, true},
		{"bytes=-4", 10, 6, 9, true},
		{"bytes=-20", 10, 0, 9, true},
		{"items=0-1", 10, 0, 0, false},
		{"bytes=", 10, 0, 0, false},
		{"bytes=-", 10, 0, 0, false},
		{"bytes=1-2-3", 10, 0, 0, false},
		{"bytes=-x", 10, 0, 0, false},
		{"bytes=x-", 10, 0, 0, false},
		{"bytes=1-x", 10, 0, 0, false},
		{"bytes=-0", 10, 0, 0, false},
		{"bytes=-1-", 10, 0, 0, false},
		{"bytes=5-4", 10, 0, 0, false},
		{"bytes=10-", 10, 0, 0, false},
		{"bytes=8-20", 10, 8, 9, true},
		{"bytes=0-10", 10, 0, 9, true},
		{"bytes=0-0", 0, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.header, func(t *testing.T) {
			start, end, ok := parseSingleRange(tt.header, tt.size)
			if start != tt.start || end != tt.end || ok != tt.ok {
				t.Fatalf("parseSingleRange(%q, %d) = (%d, %d, %v)", tt.header, tt.size, start, end, ok)
			}
		})
	}
}

func TestMediaProxyHandlesContextAndStorageFailures(t *testing.T) {
	wantErr := errors.New("storage failed")
	tests := []struct {
		name      string
		method    string
		rangeHTTP string
		store     *fakeDeliveryStore
		context   bool
		wantCode  int
		wantOpen  int
	}{
		{"missing context", http.MethodGet, "", &fakeDeliveryStore{}, false, http.StatusNotFound, 0},
		{"open failure", http.MethodGet, "", &fakeDeliveryStore{openErr: wantErr}, true, http.StatusBadGateway, 1},
		{"read failure", http.MethodGet, "", &fakeDeliveryStore{readErr: wantErr}, true, http.StatusOK, 1},
		{"range open failure", http.MethodGet, "bytes=0-1", &fakeDeliveryStore{openErr: wantErr}, true, http.StatusBadGateway, 1},
		{"range read failure", http.MethodGet, "bytes=0-1", &fakeDeliveryStore{readErr: wantErr}, true, http.StatusPartialContent, 1},
		{"head does not open", http.MethodHead, "", &fakeDeliveryStore{}, true, http.StatusOK, 0},
		{"head range does not open", http.MethodHead, "bytes=0-1", &fakeDeliveryStore{}, true, http.StatusPartialContent, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.store.body = "data"
			proxy := newMediaProxy(tt.store)
			req := httptest.NewRequest(tt.method, "/asset/"+testAssetID+"/image.bin", nil)
			req.Header.Set("Range", tt.rangeHTTP)
			if tt.context {
				req = req.WithContext(context.WithValue(req.Context(), deliveryContextKey{}, testResolvedDelivery()))
			}
			rec := httptest.NewRecorder()
			proxy.ServeHTTP(rec, req)
			if rec.Code != tt.wantCode || tt.store.openCalls != tt.wantOpen {
				t.Fatalf("status=%d openCalls=%d body=%q", rec.Code, tt.store.openCalls, rec.Body.String())
			}
		})
	}
}

func TestMediaProxyRejectsUnsatisfiableRanges(t *testing.T) {
	store := &fakeDeliveryStore{body: "data"}
	proxy := newMediaProxy(store)
	for _, rangeHeader := range []string{"bytes=4-", "bytes=2-1", "not-bytes"} {
		req := httptest.NewRequest(http.MethodGet, "/asset/"+testAssetID+"/image.bin", nil)
		req.Header.Set("Range", rangeHeader)
		req = req.WithContext(context.WithValue(req.Context(), deliveryContextKey{}, testResolvedDelivery()))
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestedRangeNotSatisfiable || rec.Header().Get("Content-Range") != "bytes */4" {
			t.Fatalf("range=%q status=%d Content-Range=%q", rangeHeader, rec.Code, rec.Header().Get("Content-Range"))
		}
	}
	if store.openCalls != 0 {
		t.Fatalf("invalid ranges opened storage %d times", store.openCalls)
	}
}

func TestMediaResponseMetadata(t *testing.T) {
	resolved := testResolvedDelivery()
	resolved.stat.ETag = `"etag-value"`
	resolved.stat.LastModified = time.Date(2026, 7, 10, 1, 2, 3, 0, time.FixedZone("KST", 9*60*60))
	store := &fakeDeliveryStore{body: "data"}
	req := httptest.NewRequest(http.MethodGet, "/asset/"+testAssetID+"/image.bin", nil)
	req = req.WithContext(context.WithValue(req.Context(), deliveryContextKey{}, resolved))
	rec := httptest.NewRecorder()
	newMediaProxy(store).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "data" {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	for name, want := range map[string]string{
		"Content-Type":        "application/octet-stream",
		"Content-Length":      "4",
		"Accept-Ranges":       "bytes",
		"ETag":                `"etag-value"`,
		"Last-Modified":       "Thu, 09 Jul 2026 16:02:03 GMT",
		"Content-Disposition": "inline",
	} {
		if got := rec.Header().Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestContentDispositionPolicies(t *testing.T) {
	tests := []struct {
		name     string
		resolved *resolvedDelivery
		want     string
	}{
		{"asset inline", testResolvedDelivery(), "inline"},
		{"signed inline", resolvedWithClaims(mediaauth.PurposeInline, ""), "inline"},
		{"signed download", resolvedWithClaims(mediaauth.PurposeDownload, "report.pdf"), `attachment; filename="report.pdf"; filename*=UTF-8''report.pdf`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			setDisposition(rec, tt.resolved)
			if got := rec.Header().Get("Content-Disposition"); got != tt.want {
				t.Fatalf("Content-Disposition = %q, want %q", got, tt.want)
			}
		})
	}

	if got := contentDisposition("inline", ""); got != "inline" {
		t.Fatalf("empty filename disposition = %q", got)
	}
	header := contentDisposition("attachment", "bad\r\n/name\\\".pdf")
	if strings.ContainsAny(header, "\r\n\\") || !strings.Contains(header, `filename="bad___name__.pdf"`) || strings.Contains(header, "%0D") {
		t.Fatalf("unsafe Content-Disposition = %q", header)
	}
}

func TestCachePolicies(t *testing.T) {
	for _, tt := range []struct {
		name   string
		claims *mediaauth.Claims
		want   string
	}{
		{"asset", nil, "public, max-age=31536000, immutable"},
		{"download", &mediaauth.Claims{Purpose: mediaauth.PurposeDownload}, "private, no-store"},
		{"expired inline", &mediaauth.Claims{Purpose: mediaauth.PurposeInline, ExpiryUnix: time.Now().Add(-time.Minute).Unix()}, "private, max-age=0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := signedCacheControl(tt.claims)
			assertEqualCacheControl(t, got, tt.want)
		})
	}
}

func assertEqualCacheControl(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("signedCacheControl() = %q, want %q", got, want)
	}
}

func TestDeliveryHelpers(t *testing.T) {
	if _, err := resolvedDeliveryFromRequest(httptest.NewRequest(http.MethodGet, "/", nil)); err == nil {
		t.Fatal("expected missing context error")
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), deliveryContextKey{}, (*resolvedDelivery)(nil)))
	if _, err := resolvedDeliveryFromRequest(req); err == nil {
		t.Fatal("expected nil context error")
	}
	if got := deliveryExtension(mediaauth.DeliveryPath{ObjectName: "segment.M4S"}); got != "m4s" {
		t.Fatalf("deliveryExtension = %q", got)
	}
	for _, contentType := range []string{"image/webp", "IMAGE/PNG; charset=utf-8"} {
		if !isImageContentType(contentType) {
			t.Fatalf("isImageContentType(%q) = false", contentType)
		}
	}
	if isImageContentType("not a mime") || isImageContentType("video/mp4") {
		t.Fatal("non-image type accepted")
	}
	for _, key := range imageTransformQueryKeys {
		req := httptest.NewRequest(http.MethodGet, "/?"+key+"=1", nil)
		if !hasImageTransformQuery(req) {
			t.Fatalf("transform key %q not detected", key)
		}
	}
	if hasImageTransformQuery(httptest.NewRequest(http.MethodGet, "/?other=1", nil)) {
		t.Fatal("unrelated query detected as transform")
	}
}

func TestExplicitExtensionContentTypeContract(t *testing.T) {
	for kind, mediaTypes := range map[mediaauth.PathKind]map[string][]string{
		mediaauth.PathAsset:       publicAssetMediaTypesByExtension,
		mediaauth.PathMediaObject: signedMediaObjectTypesByExtension,
		mediaauth.PathMediaHLS:    publicHLSMediaTypesByExtension,
	} {
		for extension, contentTypes := range mediaTypes {
			for _, contentType := range contentTypes {
				if !pathKindAllowsContentType(kind, strings.ToUpper(extension), contentType+"; charset=utf-8") {
					t.Fatalf("kind=%q extension=%q contentType=%q rejected", kind, extension, contentType)
				}
			}
		}
	}
	for _, tt := range []struct {
		kind                   mediaauth.PathKind
		extension, contentType string
	}{
		{mediaauth.PathAsset, "txt", "text/plain"},
		{mediaauth.PathAsset, "webp", "image/png"},
		{mediaauth.PathMediaObject, "", "image/png"},
		{mediaauth.PathMediaObject, "png", "invalid"},
		{mediaauth.PathMediaHLS, "png", "image/png"},
	} {
		if pathKindAllowsContentType(tt.kind, tt.extension, tt.contentType) {
			t.Fatalf("kind=%q extension=%q contentType=%q accepted", tt.kind, tt.extension, tt.contentType)
		}
	}
}

func testResolvedDelivery() *resolvedDelivery {
	return &resolvedDelivery{
		path: mediaauth.DeliveryPath{Kind: mediaauth.PathAsset, AssetID: testAssetID, Filename: "image", Extension: "bin", ObjectKey: "asset/" + testAssetID + ".bin"},
		stat: minio.ObjectInfo{Size: 4, ContentType: "application/octet-stream"},
	}
}

func resolvedWithClaims(purpose mediaauth.Purpose, filename string) *resolvedDelivery {
	resolved := testResolvedDelivery()
	resolved.claims = &mediaauth.Claims{Purpose: purpose, Filename: filename}
	return resolved
}
