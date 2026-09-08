package mediaauth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	testFileID       = "11111111-1111-4111-8111-111111111111"
	testGenerationID = "22222222-2222-4222-8222-222222222222"
	testToken        = "eyJ2IjoxLCJwIjoiaW5saW5lIiwic3QiOiJleGFjdCIsInN2IjoibWVkaWEvMTExMTExMTEtMTExMS00MTExLTgxMTEtMTExMTExMTExMTExLmdsYiIsImlhdCI6MTg5MzQ1NjAwMCwiZXhwIjoxODkzNDU2OTAwLCJtIjpbIkdFVCIsIkhFQUQiXX0.2uovPsSKqMoCIA-7LPIuRxMZH-JY05hiE9ckIUOANPI"
	testSecret       = "secret"
)

var fixedNow = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

func TestClaimsRoundTripForEveryPurpose(t *testing.T) {
	tests := []struct {
		name      string
		purpose   Purpose
		scopeType ScopeType
		scope     string
		path      string
		filename  string
		ttl       time.Duration
	}{
		{
			name:      "inline exact object",
			purpose:   PurposeInline,
			scopeType: ScopeExact,
			scope:     "media/" + testFileID + ".glb",
			path:      "media/" + testFileID + ".glb",
			ttl:       InlineTTL,
		},
		{
			name:      "download exact object",
			purpose:   PurposeDownload,
			scopeType: ScopeExact,
			scope:     "media/" + testFileID + ".pdf",
			path:      "media/" + testFileID + ".pdf",
			filename:  "작품 설명.pdf",
			ttl:       DownloadTTL,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims, err := NewClaims(tt.purpose, tt.scopeType, tt.scope, fixedNow, tt.filename)
			if err != nil {
				t.Fatalf("NewClaims() error = %v", err)
			}
			if claims.Version != TokenVersion || claims.Purpose != tt.purpose || claims.ScopeType != tt.scopeType || claims.ScopeValue != tt.scope {
				t.Fatalf("NewClaims() = %#v", claims)
			}
			if claims.IssuedAtUnix != fixedNow.Unix() || claims.ExpiryUnix != fixedNow.Add(tt.ttl).Unix() {
				t.Fatalf("unexpected timestamps: %#v", claims)
			}
			if !equalMethods(claims.Methods, []Method{MethodGet, MethodHead}) || claims.Filename != tt.filename {
				t.Fatalf("unexpected methods or filename: %#v", claims)
			}

			token, err := GenerateToken(claims, testSecret)
			if err != nil {
				t.Fatalf("GenerateToken() error = %v", err)
			}
			validated, err := validateTokenAt(token, testSecret, fixedNow.Add(time.Minute))
			if err != nil {
				t.Fatalf("validateTokenAt() error = %v", err)
			}
			if !reflect.DeepEqual(*validated, claims) {
				t.Fatalf("validated claims = %#v, want %#v", validated, claims)
			}
			if !validated.AllowsRequest("GET", tt.path) || !validated.AllowsRequest("HEAD", tt.path) {
				t.Fatal("GET and HEAD must both be authorized")
			}
		})
	}
}

func TestNewClaimsRejectsNonCanonicalContracts(t *testing.T) {
	tests := []struct {
		name      string
		purpose   Purpose
		scopeType ScopeType
		scope     string
		issuedAt  time.Time
		filename  string
	}{
		{"unknown purpose", Purpose("preview"), ScopeExact, "media/" + testFileID + ".webp", fixedNow, ""},
		{"inline prefix", PurposeInline, ScopePrefix, "media/" + testFileID + ".webp", fixedNow, ""},
		{"download prefix", PurposeDownload, ScopePrefix, "media/" + testFileID + ".pdf", fixedNow, "file.pdf"},
		{"asset scope", PurposeInline, ScopeExact, "asset/" + testFileID + ".webp", fixedNow, ""},
		{"hls object exact scope", PurposeInline, ScopeExact, "media/" + testFileID + "/hls/" + testGenerationID + "/master.m3u8", fixedNow, ""},
		{"wrong object prefix", PurposeInline, ScopeExact, "private/" + testFileID + ".webp", fixedNow, ""},
		{"invalid object id", PurposeInline, ScopeExact, "media/not-a-uuid.webp", fixedNow, ""},
		{"invalid object extension", PurposeInline, ScopeExact, "media/" + testFileID + ".WEBP", fixedNow, ""},
		{"leading slash", PurposeInline, ScopeExact, "/media/" + testFileID + ".webp", fixedNow, ""},
		{"trailing whitespace", PurposeInline, ScopeExact, "media/" + testFileID + ".webp ", fixedNow, ""},
		{"pre epoch issued at", PurposeInline, ScopeExact, "media/" + testFileID + ".webp", time.Unix(-1, 0), ""},
		{"inline filename", PurposeInline, ScopeExact, "media/" + testFileID + ".webp", fixedNow, "image.webp"},
		{"unsafe download filename", PurposeDownload, ScopeExact, "media/" + testFileID + ".pdf", fixedNow, "../file.pdf"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewClaims(tt.purpose, tt.scopeType, tt.scope, tt.issuedAt, tt.filename); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("NewClaims() error = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestGenerateTokenRejectsInvalidClaims(t *testing.T) {
	base := mustClaims(t, PurposeInline, ScopeExact, "media/"+testFileID+".webp", "")
	if _, err := GenerateToken(base, ""); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("empty secret error = %v", err)
	}
	if err := validateClaims(nil); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("validateClaims(nil) error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Claims)
	}{
		{"unknown version", func(c *Claims) { c.Version++ }},
		{"unknown purpose", func(c *Claims) { c.Purpose = Purpose("preview") }},
		{"unknown scope type", func(c *Claims) { c.ScopeType = ScopeType("wildcard") }},
		{"empty scope", func(c *Claims) { c.ScopeValue = "" }},
		{"zero issued at", func(c *Claims) { c.IssuedAtUnix = 0 }},
		{"missing expiry", func(c *Claims) { c.ExpiryUnix = 0 }},
		{"expiry equals issue", func(c *Claims) { c.ExpiryUnix = c.IssuedAtUnix }},
		{"excessive ttl", func(c *Claims) { c.ExpiryUnix = c.IssuedAtUnix + int64(InlineTTL/time.Second) + 1 }},
		{"inline filename", func(c *Claims) { c.Filename = "image.webp" }},
		{"filename too long", func(c *Claims) { c.Purpose = PurposeDownload; c.Filename = strings.Repeat("x", maxFilenameLength+1) }},
		{"filename current directory", func(c *Claims) { c.Purpose = PurposeDownload; c.Filename = "." }},
		{"filename parent directory", func(c *Claims) { c.Purpose = PurposeDownload; c.Filename = ".." }},
		{"filename invalid utf8", func(c *Claims) { c.Purpose = PurposeDownload; c.Filename = string([]byte{0xff}) }},
		{"filename unicode control", func(c *Claims) { c.Purpose = PurposeDownload; c.Filename = "bad\u0085name" }},
		{"filename slash", func(c *Claims) { c.Purpose = PurposeDownload; c.Filename = "dir/file" }},
		{"filename backslash", func(c *Claims) { c.Purpose = PurposeDownload; c.Filename = `dir\file` }},
		{"missing methods", func(c *Claims) { c.Methods = nil }},
		{"get only", func(c *Claims) { c.Methods = []Method{MethodGet} }},
		{"reversed methods", func(c *Claims) { c.Methods = []Method{MethodHead, MethodGet} }},
		{"unsupported method", func(c *Claims) { c.Methods = []Method{MethodGet, Method("POST")} }},
		{"duplicate methods", func(c *Claims) { c.Methods = []Method{MethodGet, MethodHead, MethodHead} }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := cloneClaims(base)
			tt.mutate(&claims)
			if _, err := GenerateToken(claims, testSecret); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("GenerateToken() error = %v, want ErrInvalidToken", err)
			}
		})
	}

	if err := validateScope(Purpose("unknown"), ScopeExact, "media/"+testFileID+".webp"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("unknown purpose scope error = %v", err)
	}
}

func TestValidateTokenRejectsMalformedTamperedAndNonCanonicalWire(t *testing.T) {
	claims := mustClaims(t, PurposeInline, ScopeExact, "media/"+testFileID+".webp", "")
	validToken, err := GenerateToken(claims, testSecret)
	if err != nil {
		t.Fatal(err)
	}

	invalid := []struct {
		name   string
		token  string
		secret string
	}{
		{"empty token", "", testSecret},
		{"empty secret", validToken, ""},
		{"wrong secret", validToken, "wrong"},
		{"missing signature", "payload", testSecret},
		{"empty payload", ".signature", testSecret},
		{"empty signature", "payload.", testSecret},
		{"extra part", "payload.signature.extra", testSecret},
		{"invalid token alphabet", "payload+.signature", testSecret},
		{"oversized token", strings.Repeat("a", maxTokenLength) + ".b", testSecret},
		{"tampered signature", validToken + "x", testSecret},
		{"invalid base64 payload", signedEncodedPayload("a", testSecret), testSecret},
		{"invalid json", signedPayload([]byte("{"), testSecret), testSecret},
		{"unknown json field", signedPayload([]byte(`{"v":1,"unknown":true}`), testSecret), testSecret},
		{"trailing json", signedPayload(append(mustJSON(t, claims), []byte(`{}`)...), testSecret), testSecret},
		{"invalid claims", signedPayload(mustJSON(t, func() Claims { c := cloneClaims(claims); c.ExpiryUnix = 0; return c }()), testSecret), testSecret},
		{"noncanonical json field order", signedPayload(reorderedClaimsJSON(claims), testSecret), testSecret},
	}

	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := validateTokenAt(tt.token, tt.secret, fixedNow); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("validateTokenAt() error = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestValidateTokenTimeBoundariesAndLiveClock(t *testing.T) {
	claims := mustClaims(t, PurposeInline, ScopeExact, "media/"+testFileID+".webp", "")
	token, err := GenerateToken(claims, testSecret)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := validateTokenAt(token, testSecret, fixedNow.Add(-ClockSkew)); err != nil {
		t.Fatalf("issue boundary error = %v", err)
	}
	if _, err := validateTokenAt(token, testSecret, fixedNow.Add(-ClockSkew-time.Second)); !errors.Is(err, ErrTokenNotYetValid) {
		t.Fatalf("future token error = %v", err)
	}
	if _, err := validateTokenAt(token, testSecret, fixedNow.Add(InlineTTL+ClockSkew)); err != nil {
		t.Fatalf("expiry boundary error = %v", err)
	}
	if _, err := validateTokenAt(token, testSecret, fixedNow.Add(InlineTTL+ClockSkew+time.Second)); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expired token error = %v", err)
	}

	liveClaims, err := NewClaims(PurposeInline, ScopeExact, "media/"+testFileID+".webp", time.Now().Add(-time.Second), "")
	if err != nil {
		t.Fatal(err)
	}
	liveToken, err := GenerateToken(liveClaims, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateToken(liveToken, testSecret); err != nil {
		t.Fatalf("ValidateToken() error = %v", err)
	}
}

func TestAllowsRequestEnforcesExactMethodsAndScopeBoundaries(t *testing.T) {
	exact := mustClaims(t, PurposeInline, ScopeExact, "media/"+testFileID+".webp", "")
	if !exact.AllowsRequest("GET", exact.ScopeValue) || !exact.AllowsRequest("HEAD", exact.ScopeValue) {
		t.Fatal("canonical methods must be allowed")
	}
	for _, method := range []string{"get", " GET", "POST", "OPTIONS"} {
		if exact.AllowsRequest(method, exact.ScopeValue) {
			t.Fatalf("method %q unexpectedly allowed", method)
		}
	}
	for _, path := range []string{"/" + exact.ScopeValue, exact.ScopeValue + "/child", "media/../secret"} {
		if exact.AllowsRequest("GET", path) {
			t.Fatalf("path %q unexpectedly allowed", path)
		}
	}

	unknown := cloneClaims(exact)
	unknown.ScopeType = ScopeType("unknown")
	if unknown.AllowsRequest("GET", unknown.ScopeValue) {
		t.Fatal("unknown scope type must be denied")
	}
	var nilClaims *Claims
	if nilClaims.AllowsRequest("GET", exact.ScopeValue) {
		t.Fatal("nil claims must deny requests")
	}
}

func TestCanonicalKeyAndDeliveryPathBuilders(t *testing.T) {
	tests := []struct {
		name string
		got  func() (string, error)
		want string
	}{
		{"asset key", func() (string, error) { return AssetObjectKey(testFileID, "webp") }, "asset/" + testFileID + ".webp"},
		{"media key", func() (string, error) { return MediaObjectKey(testFileID, "glb") }, "media/" + testFileID + ".glb"},
		{"hls prefix", func() (string, error) { return MediaHLSObjectPrefix(testFileID, testGenerationID) }, "media/" + testFileID + "/hls/" + testGenerationID},
		{"asset path", func() (string, error) { return AssetPath(testFileID, "image", "webp") }, "/asset/" + testFileID + "/image.webp"},
		{"signed media path", func() (string, error) { return SignedMediaPath(testToken, testFileID, "glb") }, "/media/" + testToken + "/" + testFileID + ".glb"},
		{"public hls master", func() (string, error) {
			return PublicMediaHLSPath(testFileID, testGenerationID, "master.m3u8")
		}, "/media/" + testFileID + "/hls/" + testGenerationID + "/master.m3u8"},
		{"public hls segment", func() (string, error) {
			return PublicMediaHLSPath(testFileID, testGenerationID, "segment_001.m4s")
		}, "/media/" + testFileID + "/hls/" + testGenerationID + "/segment_001.m4s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.got()
			if err != nil || got != tt.want {
				t.Fatalf("builder = %q, %v; want %q", got, err, tt.want)
			}
		})
	}

	invalid := []struct {
		name  string
		build func() (string, error)
	}{
		{"asset invalid id", func() (string, error) { return AssetObjectKey("not-a-uuid", "webp") }},
		{"asset invalid extension", func() (string, error) { return AssetObjectKey(testFileID, ".webp") }},
		{"media invalid id", func() (string, error) { return MediaObjectKey("not-a-uuid", "glb") }},
		{"media invalid extension", func() (string, error) { return MediaObjectKey(testFileID, "GLB") }},
		{"hls invalid file id", func() (string, error) { return MediaHLSObjectPrefix("not-a-uuid", testGenerationID) }},
		{"hls invalid generation id", func() (string, error) { return MediaHLSObjectPrefix(testFileID, "not-a-uuid") }},
		{"asset path invalid filename", func() (string, error) { return AssetPath(testFileID, "Image", "webp") }},
		{"asset path unsupported filename", func() (string, error) { return AssetPath(testFileID, "wrong", "webp") }},
		{"asset path invalid extension", func() (string, error) { return AssetPath(testFileID, "image", "WEBP") }},
		{"signed media invalid token", func() (string, error) { return SignedMediaPath("unsigned", testFileID, "glb") }},
		{"signed media oversized token", func() (string, error) {
			return SignedMediaPath(strings.Repeat("a", maxTokenLength)+".b", testFileID, "glb")
		}},
		{"signed media invalid id", func() (string, error) { return SignedMediaPath(testToken, "not-a-uuid", "glb") }},
		{"signed media invalid extension", func() (string, error) { return SignedMediaPath(testToken, testFileID, "GLB") }},
		{"public hls invalid object", func() (string, error) {
			return PublicMediaHLSPath(testFileID, testGenerationID, "../master.m3u8")
		}},
		{"public hls dot object", func() (string, error) { return PublicMediaHLSPath(testFileID, testGenerationID, ".") }},
		{"public hls parent object", func() (string, error) { return PublicMediaHLSPath(testFileID, testGenerationID, "..") }},
		{"public hls long object", func() (string, error) {
			return PublicMediaHLSPath(testFileID, testGenerationID, strings.Repeat("a", maxHLSObjectNameLength+1))
		}},
		{"public hls invalid file", func() (string, error) {
			return PublicMediaHLSPath("not-a-uuid", testGenerationID, "master.m3u8")
		}},
		{"public hls invalid generation", func() (string, error) { return PublicMediaHLSPath(testFileID, "not-a-uuid", "master.m3u8") }},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.build(); !errors.Is(err, ErrInvalidPath) {
				t.Fatalf("builder error = %v, want ErrInvalidPath", err)
			}
		})
	}
}

func TestParseDeliveryPathAcceptsOnlyCanonicalRoutes(t *testing.T) {
	tests := []struct {
		path string
		want DeliveryPath
	}{
		{
			path: "/asset/" + testFileID + "/image.webp",
			want: DeliveryPath{Kind: PathAsset, AssetID: testFileID, Filename: "image", Extension: "webp", ObjectKey: "asset/" + testFileID + ".webp"},
		},
		{
			path: "/media/" + testToken + "/" + testFileID + ".glb",
			want: DeliveryPath{Kind: PathMediaObject, Token: testToken, FileID: testFileID, Extension: "glb", ObjectKey: "media/" + testFileID + ".glb"},
		},
		{
			path: "/media/" + testFileID + "/hls/" + testGenerationID + "/master.m3u8",
			want: DeliveryPath{Kind: PathMediaHLS, FileID: testFileID, GenerationID: testGenerationID, ObjectName: "master.m3u8", ObjectKey: "media/" + testFileID + "/hls/" + testGenerationID + "/master.m3u8"},
		},
	}
	for _, tt := range tests {
		got, err := ParseDeliveryPath(tt.path)
		if err != nil || got != tt.want {
			t.Fatalf("ParseDeliveryPath(%q) = %#v, %v; want %#v", tt.path, got, err, tt.want)
		}
	}

	invalid := []string{
		"",
		"/assets/" + testFileID + ".webp",
		"/asset/" + testFileID + "/image.WEBP",
		"/asset/not-a-uuid/image.webp",
		"/asset/" + testFileID,
		"/asset/" + testFileID + "/.webp",
		"/asset/" + testFileID + "/Image.webp",
		"/asset/" + testFileID + "/wrong.webp",
		"/asset/" + testFileID + "/image.webp/extra",
		"/asset/" + testFileID + "/image.webp?download=1",
		"/asset/" + testFileID + ".webp",
		"/media-signed/" + testToken + "/" + testFileID + ".glb",
		"/media/" + testFileID + ".glb",
		"/media/unsigned/" + testFileID + ".glb",
		"/media/" + strings.Repeat("a", maxTokenLength) + ".b/" + testFileID + ".glb",
		"/media/" + testToken + "/not-a-uuid.glb",
		"/media/" + testToken + "/" + testFileID,
		"/media/" + testToken + "/" + testFileID + ".GLB",
		"/media/" + testToken + "/" + testFileID + "/hls/" + testGenerationID + "/master.m3u8",
		"/media/" + testToken + "/" + testFileID + "/stream/" + testGenerationID + "/master.m3u8",
		"/media/" + testFileID + "/stream/" + testGenerationID + "/master.m3u8",
		"/media/not-a-uuid/hls/" + testGenerationID + "/master.m3u8",
		"/media/" + testFileID + "/hls/not-a-uuid/master.m3u8",
		"/media/" + testFileID + "/hls/" + testGenerationID,
		"/media/" + testFileID + "/hls/" + testGenerationID + "/.",
		"/media/" + testFileID + "/hls/" + testGenerationID + "/../secret",
		"/media/" + testFileID + "/hls/" + testGenerationID + "/master%2em3u8",
	}
	for _, path := range invalid {
		if _, err := ParseDeliveryPath(path); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("ParseDeliveryPath(%q) error = %v", path, err)
		}
	}
}

func TestPrivateCanonicalValidators(t *testing.T) {
	if got, err := canonicalObjectPath("media/object"); err != nil || got != "media/object" {
		t.Fatalf("canonicalObjectPath() = %q, %v", got, err)
	}
	for _, value := range []string{
		"", " media/object", "media/object ", "/media/object", "media/object/",
		`media\object`, "media/object?x", "media/object#x", "media/object%x", "media//object",
		"media/./object", "media/../object",
	} {
		if _, err := canonicalObjectPath(value); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("canonicalObjectPath(%q) error = %v", value, err)
		}
	}

	for _, tc := range []struct {
		value string
		valid bool
	}{
		{"", true},
		{"document.pdf", true},
		{"작품 설명.pdf", true},
		{".", false},
		{"..", false},
		{"line\nbreak", false},
		{"dir/file", false},
		{`dir\file`, false},
		{strings.Repeat("x", maxFilenameLength+1), false},
		{string([]byte{0xff}), false},
	} {
		if got := validFilename(tc.value); got != tc.valid {
			t.Fatalf("validFilename(%q) = %v, want %v", tc.value, got, tc.valid)
		}
	}

	for _, tc := range []struct {
		purpose Purpose
		want    time.Duration
		ok      bool
	}{
		{PurposeInline, InlineTTL, true},
		{PurposeDownload, DownloadTTL, true},
		{Purpose("unknown"), 0, false},
	} {
		got, ok := maxTTL(tc.purpose)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("maxTTL(%q) = %v, %v; want %v, %v", tc.purpose, got, ok, tc.want, tc.ok)
		}
	}

	if !validTokenWire(testToken) || validTokenWire("unsigned") || validTokenWire("payload.signature") || validTokenWire(strings.Repeat("a", maxTokenLength)+".b") {
		t.Fatal("validTokenWire did not enforce canonical bounded token shape")
	}
}

func TestGoldenTokenV1(t *testing.T) {
	claims := mustClaims(t, PurposeInline, ScopeExact, "media/"+testFileID+".glb", "")
	token, err := GenerateToken(claims, "golden-secret")
	if err != nil {
		t.Fatal(err)
	}
	if token != testToken {
		t.Fatalf("GenerateToken() = %q, want %q", token, testToken)
	}
}

func mustClaims(t *testing.T, purpose Purpose, scopeType ScopeType, scope, filename string) Claims {
	t.Helper()
	claims, err := NewClaims(purpose, scopeType, scope, fixedNow, filename)
	if err != nil {
		t.Fatalf("NewClaims() error = %v", err)
	}
	return claims
}

func cloneClaims(claims Claims) Claims {
	claims.Methods = append([]Method(nil), claims.Methods...)
	return claims
}

func equalMethods(left, right []Method) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return payload
}

func signedPayload(payload []byte, secret string) string {
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return signedEncodedPayload(encoded, secret)
}

func signedEncodedPayload(encoded, secret string) string {
	return encoded + "." + computeSignature(encoded, secret)
}

func reorderedClaimsJSON(claims Claims) []byte {
	return []byte(`{"p":"` + string(claims.Purpose) + `","v":1,"st":"` + string(claims.ScopeType) + `","sv":"` + claims.ScopeValue + `","iat":` +
		strconv.FormatInt(claims.IssuedAtUnix, 10) + `,"exp":` + strconv.FormatInt(claims.ExpiryUnix, 10) + `,"m":["GET","HEAD"]}`)
}
