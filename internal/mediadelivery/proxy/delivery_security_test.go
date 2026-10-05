package proxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/mediadelivery/config"
	"github.com/minio/minio-go/v7"
)

func TestSignedDeliveryRejectsInvalidClaims(t *testing.T) {
	store := &fakeDeliveryStore{
		stat: minio.ObjectInfo{Key: "media/" + testFileID + ".glb", Size: 3, ContentType: "model/gltf-binary"},
		body: "glb",
	}
	router := newUnitDeliveryRouter(store, []string{"https://app.example"})
	now := time.Now().UTC()
	valid, err := mediaauth.NewClaims(mediaauth.PurposeInline, mediaauth.ScopeExact, "media/"+testFileID+".glb", now, "")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		claims mediaauth.Claims
	}{
		{"missing expiry", mutateClaims(valid, func(c *mediaauth.Claims) { c.ExpiryUnix = 0 })},
		{"excessive ttl", mutateClaims(valid, func(c *mediaauth.Claims) {
			c.ExpiryUnix = c.IssuedAtUnix + int64((mediaauth.InlineTTL+time.Second)/time.Second)
		})},
		{"unknown purpose", mutateClaims(valid, func(c *mediaauth.Claims) { c.Purpose = "preview" })},
		{"wrong scope type", mutateClaims(valid, func(c *mediaauth.Claims) { c.ScopeType = mediaauth.ScopePrefix })},
		{"traversal scope", mutateClaims(valid, func(c *mediaauth.Claims) { c.ScopeValue = "media/../secret.glb" })},
		{"unsupported method", mutateClaims(valid, func(c *mediaauth.Claims) { c.Methods = []mediaauth.Method{"POST"} })},
		{"empty methods", mutateClaims(valid, func(c *mediaauth.Claims) { c.Methods = nil })},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := signClaimsWithoutValidation(t, tt.claims, "secret")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/media/"+token+"/"+testFileID+".glb", nil))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Cloudflare-CDN-Cache-Control") != "no-store" {
				t.Fatalf("Cloudflare-CDN-Cache-Control = %q", rec.Header().Get("Cloudflare-CDN-Cache-Control"))
			}
		})
	}
	if store.statCalls != 0 || store.openCalls != 0 {
		t.Fatalf("invalid claims reached storage: stat=%d open=%d", store.statCalls, store.openCalls)
	}
}

func TestSignedDeliveryAuthorizationMatrix(t *testing.T) {
	now := time.Now().UTC()
	objectScope := "media/" + testFileID + ".glb"
	otherScope := "media/44444444-4444-4444-8444-444444444444.glb"
	inline := mustClaims(t, mediaauth.PurposeInline, mediaauth.ScopeExact, objectScope, now)
	headOnly := inline
	headOnly.Methods = []mediaauth.Method{mediaauth.MethodHead}
	wrongScope := mustClaims(t, mediaauth.PurposeInline, mediaauth.ScopeExact, otherScope, now)
	future := mustClaims(t, mediaauth.PurposeInline, mediaauth.ScopeExact, objectScope, now.Add(2*time.Minute))

	tests := []struct {
		name   string
		method string
		path   func(string) string
		token  string
		want   int
	}{
		{"head-only rejects get", http.MethodGet, mediaObjectRequestPath, signClaimsWithoutValidation(t, headOnly, "secret"), http.StatusForbidden},
		{"head-only rejects head", http.MethodHead, mediaObjectRequestPath, signClaimsWithoutValidation(t, headOnly, "secret"), http.StatusForbidden},
		{"exact scope mismatch", http.MethodGet, mediaObjectRequestPath, mustToken(t, wrongScope, "secret"), http.StatusForbidden},
		{"not yet valid", http.MethodGet, mediaObjectRequestPath, mustToken(t, future, "secret"), http.StatusForbidden},
		{"wrong secret", http.MethodGet, mediaObjectRequestPath, mustToken(t, inline, "other"), http.StatusForbidden},
		{"tampered signature", http.MethodGet, mediaObjectRequestPath, tamperTokenSignature(mustToken(t, inline, "secret")), http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeDeliveryStore{
				stat: minio.ObjectInfo{Size: 3, ContentType: "model/gltf-binary"},
				body: "glb",
			}
			router := newUnitDeliveryRouter(store, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path(tt.token), nil))
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d, body = %q", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

func TestDeliveryPathsRejectTraversalAndNonCanonicalForms(t *testing.T) {
	router := newUnitDeliveryRouter(&fakeDeliveryStore{}, nil)
	for _, requestPath := range []string{
		"/asset/" + testAssetID,
		"/asset/" + testAssetID + ".webp",
		"/asset/" + testAssetID + "/image.WEBP",
		"/asset/" + testAssetID + "/Image.webp",
		"/asset/" + testAssetID + "/image.webp/extra",
		"/asset/../" + testAssetID + ".webp",
		"/media/payload.signature/" + testFileID + ".glb/extra",
		"/media/payload.signature/" + testFileID + "/hls/" + testGenerationID + "/..",
		"/media/payload.signature/" + testFileID + "/hls/" + testGenerationID + "/segment%2Fsecret.ts",
		"/media/payload.signature/" + testFileID + "/not-hls/" + testGenerationID + "/segment.ts",
	} {
		t.Run(requestPath, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, requestPath, nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestPreflightAndCORSDoNotReadObjects(t *testing.T) {
	store := &fakeDeliveryStore{}
	router := newUnitDeliveryRouter(store, []string{"https://app.example", "https://*.example.test"})
	claims := mustClaims(t, mediaauth.PurposeInline, mediaauth.ScopeExact, "media/"+testFileID+".glb", time.Now())
	requestPath := mediaObjectRequestPath(mustToken(t, claims, "secret"))
	for _, tt := range []struct {
		origin string
		want   string
	}{
		{"https://app.example", "https://app.example"},
		{"https://admin.example.test", "https://admin.example.test"},
		{"https://example.test", ""},
		{"http://admin.example.test", ""},
		{"", ""},
	} {
		req := httptest.NewRequest(http.MethodOptions, requestPath, nil)
		req.Header.Set("Origin", tt.origin)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != tt.want {
			t.Fatalf("origin=%q status=%d allow-origin=%q", tt.origin, rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
		}
		if rec.Header().Get("Cloudflare-CDN-Cache-Control") != "no-store" {
			t.Fatalf("preflight cache header = %q", rec.Header().Get("Cloudflare-CDN-Cache-Control"))
		}
	}
	assetPreflight := httptest.NewRequest(
		http.MethodOptions,
		"/asset/"+testAssetID+"/image.png",
		nil,
	)
	assetPreflight.Header.Set("Origin", "https://app.example")
	assetRecorder := httptest.NewRecorder()
	router.ServeHTTP(assetRecorder, assetPreflight)
	if assetRecorder.Code != http.StatusNoContent ||
		assetRecorder.Header().Get("Cloudflare-CDN-Cache-Control") != "no-store" {
		t.Fatalf(
			"asset preflight status=%d cache=%q",
			assetRecorder.Code,
			assetRecorder.Header().Get("Cloudflare-CDN-Cache-Control"),
		)
	}
	if store.statCalls != 0 || store.openCalls != 0 {
		t.Fatalf("preflight reached storage: stat=%d open=%d", store.statCalls, store.openCalls)
	}
}

func TestDeliveryRejectsMissingObjectsAndMetadataMismatch(t *testing.T) {
	tests := []struct {
		name    string
		stat    minio.ObjectInfo
		statErr error
		path    string
	}{
		{"missing", minio.ObjectInfo{}, minio.ErrorResponse{Code: "NoSuchKey"}, "/asset/" + testAssetID + "/image.webp"},
		{"invalid content type", minio.ObjectInfo{ContentType: "not a mime"}, nil, "/asset/" + testAssetID + "/image.webp"},
		{"extension mismatch", minio.ObjectInfo{ContentType: "image/png"}, nil, "/asset/" + testAssetID + "/image.webp"},
		{"unsupported extension", minio.ObjectInfo{ContentType: "text/plain"}, nil, "/asset/" + testAssetID + "/image.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeDeliveryStore{stat: tt.stat, statErr: tt.statErr}
			rec := httptest.NewRecorder()
			newUnitDeliveryRouter(store, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != http.StatusNotFound || store.openCalls != 0 {
				t.Fatalf("status=%d openCalls=%d", rec.Code, store.openCalls)
			}
			for _, header := range []string{"Cache-Control", "CDN-Cache-Control", "Cloudflare-CDN-Cache-Control"} {
				if rec.Header().Get(header) != "no-store" {
					t.Fatalf("%s=%q", header, rec.Header().Get(header))
				}
			}
		})
	}
}

func TestUnsupportedMethodIsRejectedBeforeParsing(t *testing.T) {
	store := &fakeDeliveryStore{}
	rec := httptest.NewRecorder()
	newUnitDeliveryRouter(store, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/media/not-a-token", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD, OPTIONS" {
		t.Fatalf("status=%d Allow=%q", rec.Code, rec.Header().Get("Allow"))
	}
	if store.statCalls != 0 {
		t.Fatalf("method rejection reached storage")
	}
}

func newUnitDeliveryRouter(store deliveryStore, allowedOrigins []string) *MediaRouter {
	cfg := &config.Config{MediaSigningSecret: "secret", AllowedOrigins: allowedOrigins}
	mediaProxy := newMediaProxy(store)
	return newMediaRouter(cfg, store, NewImageProxy(cfg), mediaProxy)
}

func mutateClaims(claims mediaauth.Claims, mutate func(*mediaauth.Claims)) mediaauth.Claims {
	mutate(&claims)
	return claims
}

func signClaimsWithoutValidation(t *testing.T, claims mediaauth.Claims, secret string) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func mustClaims(t *testing.T, purpose mediaauth.Purpose, scopeType mediaauth.ScopeType, scope string, issuedAt time.Time) mediaauth.Claims {
	t.Helper()
	claims, err := mediaauth.NewClaims(purpose, scopeType, scope, issuedAt, "")
	if err != nil {
		t.Fatal(err)
	}
	return claims
}

func mustToken(t *testing.T, claims mediaauth.Claims, secret string) string {
	t.Helper()
	token, err := mediaauth.GenerateToken(claims, secret)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func tamperTokenSignature(token string) string {
	if strings.HasSuffix(token, "A") {
		return strings.TrimSuffix(token, "A") + "B"
	}
	return token[:len(token)-1] + "A"
}

func mediaObjectRequestPath(token string) string {
	return "/media/" + token + "/" + testFileID + ".glb"
}

func TestAllowedOriginPatterns(t *testing.T) {
	for _, tt := range []struct {
		origin  string
		allowed []string
		want    bool
	}{
		{"https://app.example", []string{"https://app.example"}, true},
		{"https://admin.example.test", []string{"https://*.example.test"}, true},
		{"http://admin.example.test", []string{"https://*.example.test"}, false},
		{"https://example.test", []string{"https://*.example.test"}, false},
		{"https://.example.test", []string{"https://*.example.test"}, false},
		{"https://admin.example.test/", []string{"https://*.example.test"}, false},
		{"https://user@admin.example.test", []string{"https://*.example.test"}, false},
		{"https://admin.example.test?x=1", []string{"https://*.example.test"}, false},
		{"https://admin.example.test#x", []string{"https://*.example.test"}, false},
		{"https://admin.example.test:8443", []string{"https://*.example.test"}, false},
		{"https://admin.example.test", []string{"*.example.test"}, false},
		{"https://admin.example.test", []string{"https://*."}, false},
		{"https://example.com", []string{"https://*.example.test"}, false},
		{"https://example.com", nil, false},
	} {
		if got := isAllowedOrigin(tt.origin, tt.allowed); got != tt.want {
			t.Fatalf("isAllowedOrigin(%q, %v) = %v", tt.origin, tt.allowed, got)
		}
	}
}

func TestCORSResponseHeaders(t *testing.T) {
	store := &fakeDeliveryStore{
		stat: minio.ObjectInfo{Size: 3, ContentType: "model/gltf-binary"},
		body: "glb",
	}
	claims := mustClaims(t, mediaauth.PurposeInline, mediaauth.ScopeExact, "media/"+testFileID+".glb", time.Now())
	req := httptest.NewRequest(http.MethodGet, mediaObjectRequestPath(mustToken(t, claims, "secret")), nil)
	req.Header.Set("Origin", "https://app.example")
	rec := httptest.NewRecorder()
	newUnitDeliveryRouter(store, []string{"https://app.example"}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	for name, want := range map[string]string{
		"Access-Control-Allow-Origin":   "https://app.example",
		"Access-Control-Allow-Methods":  "GET, HEAD, OPTIONS",
		"Access-Control-Allow-Headers":  "Range",
		"Access-Control-Expose-Headers": "Content-Range, Content-Length, Accept-Ranges, Content-Disposition",
	} {
		if got := rec.Header().Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Origin") {
		t.Fatalf("Vary = %q", rec.Header().Get("Vary"))
	}
}
