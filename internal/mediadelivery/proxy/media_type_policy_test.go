package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/minio/minio-go/v7"
)

type canonicalMediaTypeCase struct {
	name        string
	extension   string
	contentType string
	public      bool
}

// canonicalMediaTypeCases is an independent copy of the API's canonical
// MIME-to-extension contract. Keep this explicit so a CDN map cannot make its
// own completeness test pass by construction.
var canonicalMediaTypeCases = []canonicalMediaTypeCase{
	{name: "jpeg image", extension: "jpg", contentType: "image/jpeg", public: true},
	{name: "png image", extension: "png", contentType: "image/png", public: true},
	{name: "gif image", extension: "gif", contentType: "image/gif", public: true},
	{name: "webp image", extension: "webp", contentType: "image/webp", public: true},
	{name: "avif image", extension: "avif", contentType: "image/avif", public: true},
	{name: "svg image", extension: "svg", contentType: "image/svg+xml", public: true},
	{name: "x-icon image", extension: "ico", contentType: "image/x-icon", public: true},
	{name: "microsoft icon image", extension: "ico", contentType: "image/vnd.microsoft.icon", public: true},

	{name: "mp4 video", extension: "mp4", contentType: "video/mp4"},
	{name: "webm video", extension: "webm", contentType: "video/webm"},
	{name: "quicktime video", extension: "mov", contentType: "video/quicktime"},
	{name: "avi video", extension: "avi", contentType: "video/avi"},
	{name: "x-msvideo video", extension: "avi", contentType: "video/x-msvideo"},
	{name: "matroska video", extension: "mkv", contentType: "video/matroska"},
	{name: "mkv video", extension: "mkv", contentType: "video/mkv"},
	{name: "x-matroska video", extension: "mkv", contentType: "video/x-matroska"},
	{name: "application matroska video", extension: "mkv", contentType: "application/x-matroska"},

	{name: "mpeg audio", extension: "mp3", contentType: "audio/mpeg"},
	{name: "wav audio", extension: "wav", contentType: "audio/wav"},
	{name: "ogg audio", extension: "ogg", contentType: "audio/ogg"},
	{name: "webm audio", extension: "weba", contentType: "audio/webm"},
	{name: "flac audio", extension: "flac", contentType: "audio/flac"},
	{name: "aac audio", extension: "aac", contentType: "audio/aac"},
	{name: "mp4 audio", extension: "m4a", contentType: "audio/mp4"},
	{name: "m4a audio", extension: "m4a", contentType: "audio/m4a"},
	{name: "x-m4a audio", extension: "m4a", contentType: "audio/x-m4a"},
	{name: "mp4a-latm audio", extension: "m4a", contentType: "audio/mp4a-latm"},
	{name: "aiff audio", extension: "aiff", contentType: "audio/aiff"},
	{name: "x-aiff audio", extension: "aiff", contentType: "audio/x-aiff"},

	{name: "pdf document", extension: "pdf", contentType: "application/pdf"},
	{name: "plain text document", extension: "txt", contentType: "text/plain"},
	{name: "csv document", extension: "csv", contentType: "text/csv"},
	{name: "json presentation", extension: "json", contentType: "application/json", public: true},
	{name: "word document", extension: "doc", contentType: "application/msword"},
	{name: "openxml word document", extension: "docx", contentType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
	{name: "excel document", extension: "xls", contentType: "application/vnd.ms-excel"},
	{name: "openxml excel document", extension: "xlsx", contentType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
	{name: "powerpoint document", extension: "ppt", contentType: "application/vnd.ms-powerpoint"},
	{name: "openxml powerpoint document", extension: "pptx", contentType: "application/vnd.openxmlformats-officedocument.presentationml.presentation"},

	{name: "zip archive", extension: "zip", contentType: "application/zip"},
	{name: "x-zip archive", extension: "zip", contentType: "application/x-zip-compressed"},
	{name: "rar archive", extension: "rar", contentType: "application/x-rar-compressed"},
	{name: "7z archive", extension: "7z", contentType: "application/x-7z-compressed"},

	{name: "binary gltf presentation", extension: "glb", contentType: "model/gltf-binary", public: true},
}

var hlsMediaTypeCases = []canonicalMediaTypeCase{
	{name: "apple manifest", extension: "m3u8", contentType: "application/vnd.apple.mpegurl"},
	{name: "mpegurl manifest", extension: "m3u8", contentType: "application/x-mpegurl"},
	{name: "fragmented mp4 segment", extension: "m4s", contentType: "video/iso.segment"},
	{name: "transport stream segment", extension: "ts", contentType: "video/mp2t"},
}

func TestCanonicalMediaTypeMapsAreExact(t *testing.T) {
	expectedSigned := make(map[string]struct{}, len(canonicalMediaTypeCases))
	expectedPublic := make(map[string]struct{})
	for _, tt := range canonicalMediaTypeCases {
		key := mediaTypePolicyKey(tt.extension, tt.contentType)
		expectedSigned[key] = struct{}{}
		if tt.public {
			expectedPublic[key] = struct{}{}
		}
	}

	assertExactMediaTypePolicy(t, signedMediaObjectTypesByExtension, expectedSigned)
	assertExactMediaTypePolicy(t, publicAssetMediaTypesByExtension, expectedPublic)

	expectedHLS := make(map[string]struct{}, len(hlsMediaTypeCases))
	for _, tt := range hlsMediaTypeCases {
		expectedHLS[mediaTypePolicyKey(tt.extension, tt.contentType)] = struct{}{}
	}
	assertExactMediaTypePolicy(t, publicHLSMediaTypesByExtension, expectedHLS)
}

func TestCanonicalMediaTypeRouterPolicies(t *testing.T) {
	for _, tt := range canonicalMediaTypeCases {
		t.Run(tt.name+"/signed media", func(t *testing.T) {
			store := &fakeDeliveryStore{
				stat: minio.ObjectInfo{Size: 1, ContentType: tt.contentType},
				body: "x",
			}
			scope := "media/" + testFileID + "." + tt.extension
			claims := mustClaims(t, mediaauth.PurposeInline, mediaauth.ScopeExact, scope, time.Now())
			requestPath := "/media/" + mustToken(t, claims, "secret") + "/" + testFileID + "." + tt.extension

			rec := httptest.NewRecorder()
			newUnitDeliveryRouter(store, nil).ServeHTTP(
				rec,
				httptest.NewRequest(http.MethodGet, requestPath, nil),
			)

			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
			}
			if store.openCalls != 1 {
				t.Fatalf("openCalls=%d", store.openCalls)
			}
			if rec.Header().Get("Cloudflare-CDN-Cache-Control") != "no-store" {
				t.Fatalf("Cloudflare-CDN-Cache-Control=%q", rec.Header().Get("Cloudflare-CDN-Cache-Control"))
			}
		})

		t.Run(tt.name+"/public asset", func(t *testing.T) {
			store := &fakeDeliveryStore{
				stat: minio.ObjectInfo{Size: 1, ContentType: tt.contentType},
				body: "x",
			}
			requestPath := "/asset/" + testAssetID + "/image." + tt.extension

			rec := httptest.NewRecorder()
			newUnitDeliveryRouter(store, nil).ServeHTTP(
				rec,
				httptest.NewRequest(http.MethodGet, requestPath, nil),
			)

			wantStatus := http.StatusNotFound
			wantOpenCalls := 0
			if tt.public {
				wantStatus = http.StatusOK
				wantOpenCalls = 1
			}
			if rec.Code != wantStatus || store.openCalls != wantOpenCalls {
				t.Fatalf("status=%d openCalls=%d body=%q", rec.Code, store.openCalls, rec.Body.String())
			}
			assertPublicCacheHeaders(t, rec, tt.public)
		})

		t.Run(tt.name+"/mismatch", func(t *testing.T) {
			if pathKindAllowsContentType(
				mediaauth.PathMediaObject,
				tt.extension,
				"application/x-geul-mismatch",
			) {
				t.Fatal("signed media accepted a mismatched MIME type")
			}
		})
	}
}

func assertPublicCacheHeaders(t *testing.T, rec *httptest.ResponseRecorder, public bool) {
	t.Helper()
	if !public {
		return
	}
	const immutable = "public, max-age=31536000, immutable"
	if rec.Header().Get("Cache-Control") != immutable ||
		rec.Header().Get("Cloudflare-CDN-Cache-Control") != immutable {
		t.Fatalf(
			"Cache-Control=%q Cloudflare-CDN-Cache-Control=%q",
			rec.Header().Get("Cache-Control"),
			rec.Header().Get("Cloudflare-CDN-Cache-Control"),
		)
	}
}

func TestPublicHLSTypePolicy(t *testing.T) {
	for _, tt := range hlsMediaTypeCases {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeDeliveryStore{
				stat: minio.ObjectInfo{Size: 1, ContentType: tt.contentType},
				body: "x",
			}
			requestPath := "/media/" + testFileID + "/hls/" + testGenerationID + "/segment." + tt.extension

			rec := httptest.NewRecorder()
			newUnitDeliveryRouter(store, nil).ServeHTTP(
				rec,
				httptest.NewRequest(http.MethodGet, requestPath, nil),
			)

			if rec.Code != http.StatusOK || store.openCalls != 1 {
				t.Fatalf("status=%d openCalls=%d body=%q", rec.Code, store.openCalls, rec.Body.String())
			}
			if pathKindAllowsContentType(mediaauth.PathAsset, tt.extension, tt.contentType) {
				t.Fatal("public asset accepted an HLS-only type")
			}
			if pathKindAllowsContentType(mediaauth.PathMediaObject, tt.extension, tt.contentType) {
				t.Fatal("signed media object accepted an HLS-only type")
			}
		})
	}
}

func assertExactMediaTypePolicy(
	t *testing.T,
	actual map[string][]string,
	expected map[string]struct{},
) {
	t.Helper()

	actualPairs := make(map[string]struct{})
	for extension, contentTypes := range actual {
		for _, contentType := range contentTypes {
			actualPairs[mediaTypePolicyKey(extension, contentType)] = struct{}{}
		}
	}
	if len(actualPairs) != len(expected) {
		t.Fatalf("policy pair count=%d, want %d: actual=%v", len(actualPairs), len(expected), actualPairs)
	}
	for pair := range expected {
		if _, ok := actualPairs[pair]; !ok {
			t.Fatalf("missing policy pair %q", pair)
		}
	}
	for pair := range actualPairs {
		if _, ok := expected[pair]; !ok {
			t.Fatalf("unexpected policy pair %q", pair)
		}
	}
}

func mediaTypePolicyKey(extension, contentType string) string {
	return extension + "\x00" + contentType
}
