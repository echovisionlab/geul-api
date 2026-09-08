package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediadelivery/config"
	"github.com/echovisionlab/geul-api/internal/mediadelivery/storage"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/stretchr/testify/require"
)

const (
	testFileID       = "11111111-1111-4111-8111-111111111111"
	testAssetID      = "22222222-2222-4222-8222-222222222222"
	testGenerationID = "33333333-3333-4333-8333-333333333333"
)

func TestAssetDeliveryIsImmutableAndSupportsRange(t *testing.T) {
	router, closeServers := newDeliveryRouterForTest(t, map[string]proxyFakeS3Object{
		"media/asset/" + testAssetID + ".png": {body: "0123456789", contentType: "image/png"},
	}, nil)
	defer closeServers()

	req := httptest.NewRequest(http.MethodGet, "/asset/"+testAssetID+"/image.png", nil)
	req.Header.Set("Origin", "https://app.example")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "0123456789", rec.Body.String())
	require.Equal(t, "public, max-age=31536000, immutable", rec.Header().Get("Cache-Control"))
	require.Equal(t, "public, max-age=31536000, immutable", rec.Header().Get("Cloudflare-CDN-Cache-Control"))
	require.Equal(t, "https://app.example", rec.Header().Get("Access-Control-Allow-Origin"))

	req = httptest.NewRequest(http.MethodGet, "/asset/"+testAssetID+"/image.png", nil)
	req.Header.Set("Range", "bytes=2-5")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusPartialContent, rec.Code)
	require.Equal(t, "2345", rec.Body.String())
	require.Equal(t, "bytes 2-5/10", rec.Header().Get("Content-Range"))

	req = httptest.NewRequest(http.MethodHead, "/asset/"+testAssetID+"/image.png", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, rec.Body.String())
	require.Equal(t, "10", rec.Header().Get("Content-Length"))

	req = httptest.NewRequest(http.MethodGet, "/asset/"+testAssetID+"/image.png", nil)
	req.Header.Set("Range", "bytes=-4")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusPartialContent, rec.Code)
	require.Equal(t, "6789", rec.Body.String())
	require.Equal(t, "bytes 6-9/10", rec.Header().Get("Content-Range"))

	req = httptest.NewRequest(http.MethodGet, "/asset/"+testAssetID+"/image.png", nil)
	req.Header.Set("Range", "bytes=7-")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusPartialContent, rec.Code)
	require.Equal(t, "789", rec.Body.String())
	require.Equal(t, "bytes 7-9/10", rec.Header().Get("Content-Range"))
}

func TestSignedInlineAndDownloadDelivery(t *testing.T) {
	router, closeServers := newDeliveryRouterForTest(t, map[string]proxyFakeS3Object{
		"media/media/" + testFileID + ".glb": {body: "glb", contentType: "model/gltf-binary"},
		"media/media/" + testFileID + ".pdf": {body: "pdf", contentType: "application/pdf"},
	}, nil)
	defer closeServers()

	inline := signedToken(t, mediaauth.PurposeInline, mediaauth.ScopeExact, "media/"+testFileID+".glb", "", time.Now())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/media/"+inline+"/"+testFileID+".glb", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "glb", rec.Body.String())
	require.Equal(t, "inline", rec.Header().Get("Content-Disposition"))
	require.True(t, strings.HasPrefix(rec.Header().Get("Cache-Control"), "private, max-age="))
	require.Equal(t, "no-store", rec.Header().Get("Cloudflare-CDN-Cache-Control"))

	req := httptest.NewRequest(http.MethodHead, "/media/"+inline+"/"+testFileID+".glb", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, rec.Body.String())
	require.Equal(t, "3", rec.Header().Get("Content-Length"))
	require.Equal(t, "no-store", rec.Header().Get("Cloudflare-CDN-Cache-Control"))

	req = httptest.NewRequest(http.MethodGet, "/media/"+inline+"/"+testFileID+".glb", nil)
	req.Header.Set("Range", "bytes=1-")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusPartialContent, rec.Code)
	require.Equal(t, "lb", rec.Body.String())
	require.Equal(t, "no-store", rec.Header().Get("Cloudflare-CDN-Cache-Control"))

	download := signedToken(t, mediaauth.PurposeDownload, mediaauth.ScopeExact, "media/"+testFileID+".pdf", "작품 설명.pdf", time.Now())
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/media/"+download+"/"+testFileID+".pdf", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
	require.Equal(t, "no-store", rec.Header().Get("Cloudflare-CDN-Cache-Control"))
	require.Contains(t, rec.Header().Get("Content-Disposition"), "attachment")
	require.Contains(t, rec.Header().Get("Content-Disposition"), "filename*=UTF-8''")
}

func TestPublicHLSUsesStableGenerationPath(t *testing.T) {
	key := "media/media/" + testFileID + "/hls/" + testGenerationID + "/master.m3u8"
	segmentKey := "media/media/" + testFileID + "/hls/" + testGenerationID + "/segment-0001.m4s"
	router, closeServers := newDeliveryRouterForTest(t, map[string]proxyFakeS3Object{
		key:        {body: "#EXTM3U", contentType: "application/vnd.apple.mpegurl"},
		segmentKey: {body: "segment", contentType: "video/iso.segment"},
	}, nil)
	defer closeServers()

	url := "/media/" + testFileID + "/hls/" + testGenerationID + "/master.m3u8"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "#EXTM3U", rec.Body.String())
	require.Equal(t, "inline", rec.Header().Get("Content-Disposition"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.Equal(t, "no-store", rec.Header().Get("Cloudflare-CDN-Cache-Control"))

	segmentURL := "/media/" + testFileID + "/hls/" + testGenerationID + "/segment-0001.m4s"
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, segmentURL, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "segment", rec.Body.String())

	other := "/media/" + testFileID + "/hls/44444444-4444-4444-8444-444444444444/master.m3u8"
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, other, nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestDeliveryRejectsInvalidRequests(t *testing.T) {
	router, closeServers := newDeliveryRouterForTest(t, map[string]proxyFakeS3Object{
		"media/asset/" + testAssetID + ".webp": {body: "image", contentType: "image/png"},
		"media/media/" + testFileID + ".glb":   {body: "glb", contentType: "model/gltf-binary"},
	}, nil)
	defer closeServers()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/asset/"+testAssetID+"/image.webp", nil))
	require.Equal(t, http.StatusNotFound, rec.Code, "extension/MIME mismatch")

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/asset/"+testAssetID+"/image.webp", nil))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/media/not-even-a-token", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestExpiredAndTamperedTokensAreForbidden(t *testing.T) {
	router, closeServers := newDeliveryRouterForTest(t, map[string]proxyFakeS3Object{
		"media/media/" + testFileID + ".glb": {body: "glb", contentType: "model/gltf-binary"},
	}, nil)
	defer closeServers()
	scope := "media/" + testFileID + ".glb"
	expired := signedToken(t, mediaauth.PurposeInline, mediaauth.ScopeExact, scope, "", time.Now().Add(-mediaauth.InlineTTL-time.Minute))
	for _, token := range []string{expired, tamperTokenSignature(signedToken(t, mediaauth.PurposeInline, mediaauth.ScopeExact, scope, "", time.Now()))} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/media/"+token+"/"+testFileID+".glb", nil))
		require.Equal(t, http.StatusForbidden, rec.Code)
	}
}

func TestImageTransformationUsesCanonicalObjectKey(t *testing.T) {
	var gotPath string
	imgproxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "image/webp")
		_, _ = w.Write([]byte("transformed"))
	}))
	defer imgproxy.Close()
	router, closeServers := newDeliveryRouterForTest(t, map[string]proxyFakeS3Object{
		"media/asset/" + testAssetID + ".png": {body: "source", contentType: "image/png"},
	}, imgproxy)
	defer closeServers()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/asset/"+testAssetID+"/image.png?w=320&q=80", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "transformed", rec.Body.String())
	require.Contains(t, gotPath, "/plain/s3://media/asset/"+testAssetID+".png")
	require.Equal(t, "public, max-age=31536000, immutable", rec.Header().Get("Cache-Control"))
}

func TestExtensionContentTypeMatrix(t *testing.T) {
	for _, tt := range []struct {
		kind        mediaauth.PathKind
		extension   string
		contentType string
		want        bool
	}{
		{mediaauth.PathAsset, "jpg", "image/jpeg", true},
		{mediaauth.PathAsset, "pdf", "application/pdf", false},
		{mediaauth.PathMediaObject, "pdf", "application/pdf", true},
		{mediaauth.PathMediaObject, "bin", "application/octet-stream", false},
		{mediaauth.PathMediaHLS, "m3u8", "application/vnd.apple.mpegurl; charset=utf-8", true},
		{mediaauth.PathMediaHLS, "m4s", "video/iso.segment", true},
		{mediaauth.PathMediaObject, "webp", "image/png", false},
		{mediaauth.PathMediaObject, "", "image/png", false},
		{mediaauth.PathKind("unknown"), "png", "image/png", false},
	} {
		require.Equal(t, tt.want, pathKindAllowsContentType(tt.kind, tt.extension, tt.contentType), tt)
	}
}

func TestContentDisposition(t *testing.T) {
	header := contentDisposition("attachment", "작품 설명.pdf")
	require.Contains(t, header, `filename="__ __.pdf"`)
	require.Contains(t, header, "filename*=UTF-8''")
}

func newDeliveryRouterForTest(t *testing.T, objects map[string]proxyFakeS3Object, imgproxy *httptest.Server) (*MediaRouter, func()) {
	t.Helper()
	s3 := newProxyFakeMinioServer(t, objects)
	if imgproxy == nil {
		imgproxy = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unexpected image transform", http.StatusInternalServerError)
		}))
	}
	cfg := &config.Config{
		S3Endpoint: strings.TrimPrefix(s3.URL, "http://"), S3AccessKeyID: "access", S3SecretAccessKey: "secret",
		S3Region: "us-east-1", S3CacheBucket: "cache", S3MediaBucket: "media", MediaSigningSecret: "secret",
		AllowedOrigins: []string{"https://app.example"}, ImgproxyURL: imgproxy.URL, ImgproxyKey: "00", ImgproxySalt: "01",
	}
	client, err := storage.NewMinioClient(cfg)
	require.NoError(t, err)
	mediaProxy := NewMediaProxy(client.Client(), cfg.S3MediaBucket)
	router := NewMediaRouter(cfg, client.Client(), cfg.S3MediaBucket, NewImageProxy(cfg), mediaProxy)
	return router, func() {
		s3.Close()
	}
}

func signedToken(t *testing.T, purpose mediaauth.Purpose, scopeType mediaauth.ScopeType, scope, filename string, issuedAt time.Time) string {
	t.Helper()
	claims, err := mediaauth.NewClaims(purpose, scopeType, scope, issuedAt, filename)
	require.NoError(t, err)
	token, err := mediaauth.GenerateToken(claims, "secret")
	require.NoError(t, err)
	return token
}
