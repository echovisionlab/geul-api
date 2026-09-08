package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/minio/minio-go/v7"
)

func TestPublicAssetDownloadMetadataPolicy(t *testing.T) {
	tests := []struct {
		name         string
		metadata     http.Header
		userMetadata minio.StringMap
		forbidden    bool
	}{
		{name: "no metadata"},
		{name: "empty standard disposition", metadata: http.Header{"Content-Disposition": []string{"", " "}}},
		{name: "mixed-case standard inline", metadata: http.Header{"cOnTeNt-dIsPoSiTiOn": []string{" InLiNe "}}},
		{name: "mixed-case S3 custom inline", metadata: http.Header{"x-AmZ-MeTa-DiSpOsItIoN": []string{"INLINE"}}},
		{name: "MinIO custom inline", metadata: http.Header{"X-Minio-Meta-Disposition": []string{"inline"}}},
		{name: "stripped user metadata inline", userMetadata: minio.StringMap{"dIsPoSiTiOn": "inline"}},
		{
			name: "empty download filename markers",
			metadata: http.Header{
				"Download-Filename":              []string{" "},
				"X-Amz-Meta-Download-Filename":   []string{""},
				"X-Minio-Meta-Download-Filename": []string{"", " "},
			},
			userMetadata: minio.StringMap{"Download-Filename": ""},
		},
		{
			name:      "standard attachment",
			metadata:  http.Header{"Content-Disposition": []string{"attachment"}},
			forbidden: true,
		},
		{
			name:      "standard attachment with parameters",
			metadata:  http.Header{"cOnTeNt-dIsPoSiTiOn": []string{`AtTaChMeNt; filename="report.png"`}},
			forbidden: true,
		},
		{
			name:      "S3 custom attachment with RFC 5987 parameter",
			metadata:  http.Header{"x-AmZ-MeTa-DiSpOsItIoN": []string{"attachment; filename*=UTF-8''report.png"}},
			forbidden: true,
		},
		{
			name:      "MinIO custom attachment",
			metadata:  http.Header{"X-Minio-Meta-Disposition": []string{"ATTACHMENT"}},
			forbidden: true,
		},
		{
			name:      "unknown disposition",
			metadata:  http.Header{"Content-Disposition": []string{"form-data"}},
			forbidden: true,
		},
		{
			name:      "multiple disposition values fail closed",
			metadata:  http.Header{"Content-Disposition": []string{"inline", "attachment"}},
			forbidden: true,
		},
		{
			name:      "standard malformed disposition",
			metadata:  http.Header{"Content-Disposition": []string{`attachment; filename="unterminated`}},
			forbidden: true,
		},
		{
			name:      "custom malformed disposition",
			metadata:  http.Header{"X-Amz-Meta-Disposition": []string{"inline; filename"}},
			forbidden: true,
		},
		{
			name:      "inline filename parameter",
			metadata:  http.Header{"Content-Disposition": []string{`inline; filename="report.png"`}},
			forbidden: true,
		},
		{
			name:      "standard download filename marker",
			metadata:  http.Header{"dOwNlOaD-FiLeNaMe": []string{"report.png"}},
			forbidden: true,
		},
		{
			name:      "S3 download filename marker",
			metadata:  http.Header{"x-AmZ-MeTa-DoWnLoAd-FiLeNaMe": []string{"report.png"}},
			forbidden: true,
		},
		{
			name:      "MinIO download filename marker",
			metadata:  http.Header{"X-Minio-Meta-Download-Filename": []string{"report.png"}},
			forbidden: true,
		},
		{
			name:         "stripped user metadata attachment",
			userMetadata: minio.StringMap{"Disposition": `attachment; filename="report.png"`},
			forbidden:    true,
		},
		{
			name:         "stripped user download filename marker",
			userMetadata: minio.StringMap{"dOwNlOaD-FiLeNaMe": "report.png"},
			forbidden:    true,
		},
		{
			name:         "prefixed user download filename marker",
			userMetadata: minio.StringMap{"x-AmZ-MeTa-DoWnLoAd-FiLeNaMe": "report.png"},
			forbidden:    true,
		},
		{
			name:         "stripped user malformed disposition",
			userMetadata: minio.StringMap{"Disposition": `inline; filename="unterminated`},
			forbidden:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stat := minio.ObjectInfo{
				Metadata:     tt.metadata,
				UserMetadata: tt.userMetadata,
			}
			if got := hasForbiddenPublicAssetDownloadMetadata(stat); got != tt.forbidden {
				t.Fatalf("forbidden=%v, want %v", got, tt.forbidden)
			}
		})
	}
}

func TestForbiddenPublicAssetDownloadMetadataAlwaysReturnsNoStore(t *testing.T) {
	forbiddenMetadata := []struct {
		name         string
		metadata     http.Header
		userMetadata minio.StringMap
	}{
		{
			name:     "standard disposition parameters",
			metadata: http.Header{"cOnTeNt-dIsPoSiTiOn": []string{`attachment; filename="report.png"`}},
		},
		{
			name:     "S3 custom disposition parameters",
			metadata: http.Header{"x-AmZ-MeTa-DiSpOsItIoN": []string{`ATTACHMENT; filename="report.png"`}},
		},
		{
			name:     "lone S3 download filename",
			metadata: http.Header{"x-AmZ-MeTa-DoWnLoAd-FiLeNaMe": []string{"report.png"}},
		},
		{
			name:     "malformed standard disposition",
			metadata: http.Header{"Content-Disposition": []string{`attachment; filename="unterminated`}},
		},
		{
			name:         "stripped MinIO user metadata",
			userMetadata: minio.StringMap{"Download-Filename": "report.png"},
		},
	}
	accessPaths := []struct {
		name        string
		method      string
		extension   string
		contentType string
		query       string
	}{
		{name: "GET", method: http.MethodGet, extension: "png", contentType: "image/png"},
		{name: "HEAD", method: http.MethodHead, extension: "png", contentType: "image/png"},
		{name: "image transform", method: http.MethodGet, extension: "png", contentType: "image/png", query: "?w=320"},
		{name: "waveform GET", method: http.MethodGet, extension: "json", contentType: "application/json"},
		{name: "mesh GET", method: http.MethodGet, extension: "glb", contentType: "model/gltf-binary"},
	}

	for _, metadataCase := range forbiddenMetadata {
		for _, accessCase := range accessPaths {
			t.Run(metadataCase.name+"/"+accessCase.name, func(t *testing.T) {
				store := &fakeDeliveryStore{
					stat: minio.ObjectInfo{
						Size:         1,
						ContentType:  accessCase.contentType,
						Metadata:     metadataCase.metadata,
						UserMetadata: metadataCase.userMetadata,
					},
					body: "x",
				}
				requestPath := "/asset/" + testAssetID + "/image." + accessCase.extension + accessCase.query

				rec := httptest.NewRecorder()
				newUnitDeliveryRouter(store, nil).ServeHTTP(
					rec,
					httptest.NewRequest(accessCase.method, requestPath, nil),
				)

				if rec.Code != http.StatusForbidden || store.openCalls != 0 {
					t.Fatalf("status=%d openCalls=%d body=%q", rec.Code, store.openCalls, rec.Body.String())
				}
				for _, header := range []string{
					"Cache-Control",
					"CDN-Cache-Control",
					"Cloudflare-CDN-Cache-Control",
				} {
					if rec.Header().Get(header) != "no-store" {
						t.Fatalf("%s=%q", header, rec.Header().Get(header))
					}
				}
			})
		}
	}
}

func TestInlineOrEmptyPublicAssetMetadataRemainsAllowed(t *testing.T) {
	for _, tt := range []struct {
		name         string
		method       string
		metadata     http.Header
		userMetadata minio.StringMap
	}{
		{name: "empty GET", method: http.MethodGet},
		{
			name:     "standard inline GET",
			method:   http.MethodGet,
			metadata: http.Header{"cOnTeNt-dIsPoSiTiOn": []string{" InLiNe "}},
		},
		{
			name:     "custom inline HEAD",
			method:   http.MethodHead,
			metadata: http.Header{"x-AmZ-MeTa-DiSpOsItIoN": []string{"INLINE"}},
		},
		{
			name:         "stripped user inline GET",
			method:       http.MethodGet,
			userMetadata: minio.StringMap{"dIsPoSiTiOn": "inline"},
		},
		{
			name:   "empty download filename markers GET",
			method: http.MethodGet,
			metadata: http.Header{
				"Download-Filename":            []string{" "},
				"X-Amz-Meta-Download-Filename": []string{""},
			},
			userMetadata: minio.StringMap{"Download-Filename": ""},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeDeliveryStore{
				stat: minio.ObjectInfo{
					Size:         1,
					ContentType:  "image/png",
					Metadata:     tt.metadata,
					UserMetadata: tt.userMetadata,
				},
				body: "x",
			}

			rec := httptest.NewRecorder()
			newUnitDeliveryRouter(store, nil).ServeHTTP(
				rec,
				httptest.NewRequest(tt.method, "/asset/"+testAssetID+"/image.png", nil),
			)

			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
			}
			if tt.method == http.MethodGet && store.openCalls != 1 {
				t.Fatalf("openCalls=%d", store.openCalls)
			}
			if tt.method == http.MethodHead && store.openCalls != 0 {
				t.Fatalf("HEAD openCalls=%d", store.openCalls)
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
		})
	}
}

func TestSignedDownloadIgnoresUntrustedObjectDispositionMetadata(t *testing.T) {
	store := &fakeDeliveryStore{
		stat: minio.ObjectInfo{
			Size:        1,
			ContentType: "application/pdf",
			Metadata: http.Header{
				"X-Amz-Meta-Disposition":       []string{"attachment"},
				"X-Amz-Meta-Download-Filename": []string{"untrusted.pdf"},
			},
		},
		body: "x",
	}
	scope := "media/" + testFileID + ".pdf"
	claims, err := mediaauth.NewClaims(
		mediaauth.PurposeDownload,
		mediaauth.ScopeExact,
		scope,
		time.Now(),
		"trusted.pdf",
	)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	newUnitDeliveryRouter(store, nil).ServeHTTP(
		rec,
		httptest.NewRequest(
			http.MethodGet,
			"/media/"+mustToken(t, claims, "secret")+"/"+testFileID+".pdf",
			nil,
		),
	)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	disposition := rec.Header().Get("Content-Disposition")
	if !strings.Contains(disposition, "trusted.pdf") || strings.Contains(disposition, "untrusted.pdf") {
		t.Fatalf("Content-Disposition=%q", disposition)
	}
}
