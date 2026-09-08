package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediadelivery/config"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/minio/minio-go/v7"
)

func TestImageProxySuccessAndHead(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			var gotAccept string
			var gotURL string
			proxy := NewImageProxy(imageProxyConfig("https://imgproxy.example"))
			proxy.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				gotAccept = r.Header.Get("Accept")
				gotURL = r.URL.String()
				return &http.Response{
					StatusCode: http.StatusOK,
					Header: http.Header{
						"Content-Type":   []string{"image/webp"},
						"Content-Length": []string{"11"},
					},
					Body: io.NopCloser(strings.NewReader("transformed")),
				}, nil
			})}
			req := imageRequest(method, "/asset/"+testAssetID+"/image.png?w=320&q=37", testResolvedImage(nil))
			req.Header.Set("Accept", "image/avif,image/webp")
			req.Header.Set("Origin", "https://app.example")
			rec := httptest.NewRecorder()
			proxy.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || gotAccept != "image/avif,image/webp" {
				t.Fatalf("status=%d Accept=%q", rec.Code, gotAccept)
			}
			if !strings.Contains(gotURL, "/q:37/") {
				t.Fatalf("imgproxy URL = %q", gotURL)
			}
			if method == http.MethodGet && rec.Body.String() != "transformed" {
				t.Fatalf("GET body = %q", rec.Body.String())
			}
			if method == http.MethodHead && rec.Body.Len() != 0 {
				t.Fatalf("HEAD body = %q", rec.Body.String())
			}
			if rec.Header().Get("Content-Length") != "11" {
				t.Fatalf("headers = %#v", rec.Header())
			}
		})
	}
}

func TestImageProxyHandlesResponseReadError(t *testing.T) {
	proxy := imageProxyWithTransport(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/webp"}},
			Body:       &errorReadCloser{err: io.ErrUnexpectedEOF},
		}, nil
	}))
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, imageRequest(http.MethodGet, "/asset/"+testAssetID+"/image.png?w=64", testResolvedImage(nil)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestImageProxyFailureModes(t *testing.T) {
	tests := []struct {
		name      string
		proxy     *ImageProxy
		withState bool
		target    string
		want      int
	}{
		{"missing delivery context", NewImageProxy(imageProxyConfig("https://imgproxy.example")), false, "?w=64", http.StatusNotFound},
		{"invalid public options", NewImageProxy(imageProxyConfig("https://imgproxy.example")), true, "?w=100", http.StatusBadRequest},
		{"invalid signing config", NewImageProxy(&config.Config{ImgproxyURL: "https://imgproxy.example", S3MediaBucket: "media"}), true, "?w=64", http.StatusInternalServerError},
		{"invalid imgproxy URL", NewImageProxy(imageProxyConfig("http://[::1")), true, "?w=64", http.StatusInternalServerError},
		{"transport failure", imageProxyWithTransport(roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed")
		})), true, "?w=64", http.StatusBadGateway},
		{"upstream failure", imageProxyWithTransport(roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusUnprocessableEntity, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader("bad image"))}, nil
		})), true, "?w=64", http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/asset/"+testAssetID+"/image.png"+tt.target, nil)
			if tt.withState {
				req = imageRequest(http.MethodGet, req.URL.String(), testResolvedImage(nil))
			}
			rec := httptest.NewRecorder()
			tt.proxy.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status=%d want=%d body=%q", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

func TestRouterRejectsInvalidPublicAssetOptions(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		contentType string
		wantBody    string
	}{
		{"unknown option", "/asset/" + testAssetID + "/image.png?dpr=2", "image/png", "Invalid asset options"},
		{"transform on non-image", "/asset/" + testAssetID + "/image.glb?w=320", "model/gltf-binary", "Asset options are supported only for images"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeDeliveryStore{stat: minio.ObjectInfo{ContentType: tt.contentType}}
			recorder := httptest.NewRecorder()
			newUnitDeliveryRouter(store, nil).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), tt.wantBody) {
				t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestParseImageOptions(t *testing.T) {
	opts, err := parseImageOptions(map[string][]string{
		"w":      {"4096"},
		"h":      {"200"},
		"q":      {"100"},
		"fit":    {"fill"},
		"format": {"avif"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if opts != (imageOptions{Width: 4096, Height: 200, Quality: 100, Fit: "fill", Format: "avif"}) {
		t.Fatalf("options = %#v", opts)
	}
	for name, query := range map[string]map[string][]string{
		"oversized width":  {"w": {"4097"}},
		"invalid height":   {"h": {"invalid"}},
		"zero quality":     {"q": {"0"}},
		"quality over max": {"q": {"101"}},
		"invalid fit":      {"fit": {"crop"}},
		"invalid format":   {"format": {"bmp"}},
	} {
		if _, err := parseImageOptions(query, false); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	for _, format := range []string{"png", "jpg", "jpeg", "webp", "avif"} {
		got, err := parseImageOptions(map[string][]string{"format": {format}}, false)
		if err != nil || got.Format != format {
			t.Fatalf("format %q parsed as %q, err=%v", format, got.Format, err)
		}
	}
	if _, err := getQueryInt(map[string][]string{"v": {"x"}}, "v"); err == nil {
		t.Fatal("invalid integer query accepted")
	}
	if getQueryString(map[string][]string{"v": {}}, "v") != "" || getQueryString(nil, "v") != "" {
		t.Fatal("invalid string query accepted")
	}
}

func TestParsePublicImageOptions(t *testing.T) {
	for name, query := range map[string]map[string][]string{
		"unmanaged width": {"w": {"100"}},
		"explicit format": {"format": {"webp"}},
		"unknown option":  {"dpr": {"2"}},
		"duplicate width": {"w": {"320", "640"}},
	} {
		if _, err := parseImageOptions(query, true); err == nil {
			t.Fatalf("public %s accepted", name)
		}
	}
	for _, quality := range []int{1, 37, 80, 100} {
		got, err := parseImageOptions(map[string][]string{"w": {"320"}, "q": {strconv.Itoa(quality)}, "fit": {"fill"}}, true)
		if err != nil {
			t.Fatalf("public quality %d rejected: %v", quality, err)
		}
		if got.Quality != quality {
			t.Fatalf("public quality %d parsed as %d", quality, got.Quality)
		}
	}
}

func TestImageProxyBuildsSignedURLs(t *testing.T) {
	proxy := NewImageProxy(imageProxyConfig("https://imgproxy.example"))
	signedURL, err := proxy.buildImgproxyURL("asset/"+testAssetID+".png", imageOptions{Quality: 80})
	if err != nil || strings.Contains(signedURL, "/insecure/") || !strings.Contains(signedURL, "/q:80/plain/s3://media/asset/") {
		t.Fatalf("signed URL = %q, err=%v", signedURL, err)
	}
	resized, err := proxy.buildImgproxyURL("asset/"+testAssetID+".png", imageOptions{Width: 100, Height: 50, Quality: 90, Fit: "fill", Format: "webp"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resized, "/rs:fill:100:50/q:90/f:webp/plain/") {
		t.Fatalf("resized URL = %q", resized)
	}
}

func TestImageProxyRejectsInvalidSigningSecrets(t *testing.T) {
	proxy := NewImageProxy(imageProxyConfig("https://imgproxy.example"))
	proxy.cfg.ImgproxyKey = "not-hex"
	proxy.cfg.ImgproxySalt = "00"
	if _, err := proxy.sign("/path"); err == nil {
		t.Fatal("bad key was accepted")
	}
	proxy.cfg.ImgproxyKey = "00"
	proxy.cfg.ImgproxySalt = "not-hex"
	if _, err := proxy.sign("/path"); err == nil {
		t.Fatal("bad salt was accepted")
	}
	proxy.cfg.ImgproxySalt = "01"
	if got, err := proxy.sign("/path"); err != nil || got == "" {
		t.Fatalf("valid signature = %q, err=%v", got, err)
	}
	proxy.cfg.ImgproxyKey = ""
	if _, err := proxy.buildImgproxyURL("asset/"+testAssetID+".png", imageOptions{Quality: 80}); err == nil {
		t.Fatal("buildImgproxyURL accepted missing key")
	}
}

func TestRouterRestrictsImageTransformsByDisposition(t *testing.T) {
	now := time.Now()
	download, err := mediaauth.NewClaims(mediaauth.PurposeDownload, mediaauth.ScopeExact, "media/"+testFileID+".png", now, "image.png")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		path  string
		store *fakeDeliveryStore
	}{
		{
			name:  "signed download",
			path:  "/media/" + mustToken(t, download, "secret") + "/" + testFileID + ".png?w=100",
			store: &fakeDeliveryStore{stat: minio.ObjectInfo{ContentType: "image/png"}},
		},
		{
			name:  "asset attachment",
			path:  "/asset/" + testAssetID + "/image.png?w=320",
			store: &fakeDeliveryStore{stat: minio.ObjectInfo{ContentType: "image/png", Metadata: http.Header{"X-Amz-Meta-Disposition": []string{"attachment"}}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			newUnitDeliveryRouter(tt.store, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
			}
		})
	}
}

func imageProxyWithTransport(transport http.RoundTripper) *ImageProxy {
	proxy := NewImageProxy(imageProxyConfig("https://imgproxy.example"))
	proxy.client = &http.Client{Transport: transport}
	return proxy
}

func imageProxyConfig(imgproxyURL string) *config.Config {
	return &config.Config{
		ImgproxyURL:    imgproxyURL,
		S3MediaBucket:  "media",
		AllowedOrigins: []string{"https://app.example"},
		ImgproxyKey:    "00",
		ImgproxySalt:   "01",
	}
}

func imageRequest(method, target string, resolved *resolvedDelivery) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	return req.WithContext(context.WithValue(req.Context(), deliveryContextKey{}, resolved))
}

func testResolvedImage(claims *mediaauth.Claims) *resolvedDelivery {
	return &resolvedDelivery{
		path:   mediaauth.DeliveryPath{Kind: mediaauth.PathAsset, ObjectKey: "asset/" + testAssetID + ".png"},
		claims: claims,
		stat:   minio.ObjectInfo{Size: 10, ContentType: "image/png"},
	}
}
