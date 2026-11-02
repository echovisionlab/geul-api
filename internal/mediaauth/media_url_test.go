package mediaauth

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	optional "github.com/echovisionlab/geul-api/internal/optional"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func signedClaims(t *testing.T, signedURL string) *Claims {
	t.Helper()
	parsed, err := url.Parse(signedURL)
	if err != nil {
		t.Fatalf("parse %q: %v", signedURL, err)
	}
	token, _, ok := strings.Cut(strings.TrimPrefix(parsed.Path, "/media/"), "/")
	if !ok {
		t.Fatalf("signed URL %q has no token segment", signedURL)
	}
	claims, err := ValidateToken(token, "secret")
	if err != nil {
		t.Fatalf("ValidateToken() error = %v", err)
	}
	return claims
}

func TestSignFileURLScopesTheExactObject(t *testing.T) {
	signed, expiresAt, err := SignFileURL("cdn.example.com", testFileID, " MP3 ", "secret", 0, PurposeInline, "")
	if err != nil {
		t.Fatalf("SignFileURL() error = %v", err)
	}
	if !strings.HasPrefix(signed, "https://cdn.example.com/media/") || !strings.HasSuffix(signed, "/"+testFileID+".mp3") {
		t.Fatalf("signed URL = %q", signed)
	}
	claims := signedClaims(t, signed)
	if claims.ScopeType != ScopeExact || claims.ScopeValue != "media/"+testFileID+".mp3" || claims.Purpose != PurposeInline {
		t.Fatalf("claims = %+v", claims)
	}
	if !time.Unix(claims.ExpiryUnix, 0).UTC().Equal(expiresAt) {
		t.Fatalf("expiresAt = %v, token expiry = %v", expiresAt, claims.ExpiryUnix)
	}
	if lifetime := claims.ExpiryUnix - claims.IssuedAtUnix; lifetime != int64(time.Minute/time.Second) {
		t.Fatalf("zero TTL lifetime = %ds, want the one-minute fallback", lifetime)
	}
}

func TestSignFileURLCapsTheLifetimeAndCarriesTheFilename(t *testing.T) {
	signed, _, err := SignFileURL("cdn.example.com", testFileID, "wav", "secret", 24*time.Hour, PurposeDownload, "간월재 원본.wav")
	if err != nil {
		t.Fatalf("SignFileURL() error = %v", err)
	}
	claims := signedClaims(t, signed)
	if claims.Purpose != PurposeDownload || claims.Filename != "간월재 원본.wav" {
		t.Fatalf("claims = %+v", claims)
	}
	if lifetime := claims.ExpiryUnix - claims.IssuedAtUnix; lifetime != int64(DownloadTTL/time.Second) {
		t.Fatalf("lifetime = %ds, want the download cap", lifetime)
	}
}

func TestSignFileURLRejections(t *testing.T) {
	for name, sign := range map[string]func() error{
		"missing secret": func() error {
			_, _, err := SignFileURL("", testFileID, "mp3", "", time.Minute, PurposeInline, "")
			return err
		},
		"invalid file ID": func() error {
			_, _, err := SignFileURL("", "not-a-uuid", "mp3", "secret", time.Minute, PurposeInline, "")
			return err
		},
		"invalid purpose": func() error {
			_, _, err := SignFileURL("", testFileID, "mp3", "secret", time.Minute, Purpose("invalid"), "")
			return err
		},
	} {
		if err := sign(); !errors.Is(err, ErrURLSigningFailed) {
			t.Fatalf("%s: error = %v, want ErrURLSigningFailed", name, err)
		}
	}
}

func TestPublicHLSURL(t *testing.T) {
	got, err := PublicHLSURL("cdn.example.com", testFileID, testGenerationID, "master.m3u8")
	if err != nil {
		t.Fatalf("PublicHLSURL() error = %v", err)
	}
	if want := "https://cdn.example.com/media/" + testFileID + "/hls/" + testGenerationID + "/master.m3u8"; got != want {
		t.Fatalf("PublicHLSURL() = %q, want %q", got, want)
	}
	for _, input := range [][2]string{
		{"not-a-uuid", "master.m3u8"},
		{testFileID, "../master.m3u8"},
		{testFileID, "segment_001.m4s"},
	} {
		if _, err := PublicHLSURL("", input[0], testGenerationID, input[1]); !errors.Is(err, ErrURLSigningFailed) {
			t.Fatalf("PublicHLSURL(%q, %q) error = %v", input[0], input[1], err)
		}
	}
}

func TestExpiringReference(t *testing.T) {
	name := "source"
	inline, err := ExpiringReference("cdn.example.com", "secret", testFileID, "wav", "audio/wav", &name, PurposeInline, time.Minute)
	if err != nil {
		t.Fatalf("inline ExpiringReference() error = %v", err)
	}
	if optional.Value(inline).Purpose != PurposeInline || optional.Value(optional.Value(inline).FileName) != "source" || optional.Value(inline).FileID != testFileID || optional.Value(inline).MimeType != "audio/wav" || optional.Value(inline).Extension != "wav" {
		t.Fatalf("inline ref = %v", inline)
	}
	if signedClaims(t, optional.Value(inline).URL).Filename != "" {
		t.Fatal("inline token carries a download filename")
	}

	download, err := ExpiringReference("cdn.example.com", "secret", testFileID, "wav", "audio/wav", &name, PurposeDownload, time.Minute)
	if err != nil {
		t.Fatalf("download ExpiringReference() error = %v", err)
	}
	claims := signedClaims(t, optional.Value(download).URL)
	if optional.Value(optional.Value(download).FileName) != "source.wav" || claims.Filename != "source.wav" {
		t.Fatalf("download ref file name = %q, token filename = %q", optional.Value(optional.Value(download).FileName), claims.Filename)
	}
	if !optional.Value(download).ExpiresAt.Equal(time.Unix(claims.ExpiryUnix, 0)) {
		t.Fatal("advertised expiry differs from the token expiry")
	}

	if _, err := ExpiringReference("", "", testFileID, "wav", "audio/wav", nil, PurposeInline, time.Minute); !errors.Is(err, ErrURLSigningFailed) {
		t.Fatalf("unsigned ExpiringReference() error = %v", err)
	}
}

func TestDeliveryPurpose(t *testing.T) {
	for purpose, want := range map[Purpose]commonv1.MediaDeliveryPurpose{
		PurposeInline:      commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_INLINE,
		PurposeDownload:    commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_DOWNLOAD,
		Purpose("unknown"): commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_UNSPECIFIED,
	} {
		if got := DeliveryPurpose(purpose); got != want {
			t.Fatalf("DeliveryPurpose(%q) = %v, want %v", purpose, got, want)
		}
	}
}

func TestCanonicalDownloadFilename(t *testing.T) {
	invalid, valid, bare, upper := "../source.wav", " source.wav ", "source", "SOURCE.WAV"
	for _, tc := range []struct {
		name      string
		fileName  *string
		extension string
		want      string
	}{
		{"historical invalid value", &invalid, "wav", "download-" + testFileID + ".wav"},
		{"valid value keeps its extension", &valid, "wav", "source.wav"},
		{"extension compared without case", &upper, "wav", "SOURCE.WAV"},
		{"missing extension appended", &bare, " WAV ", "source.wav"},
		{"no extension expected", &bare, "", "source"},
		{"missing name", nil, "wav", "download-" + testFileID + ".wav"},
		{"missing name and extension", nil, "", "download-" + testFileID},
	} {
		if got := CanonicalDownloadFilename(tc.fileName, testFileID, tc.extension); got != tc.want {
			t.Fatalf("%s: CanonicalDownloadFilename() = %q, want %q", tc.name, got, tc.want)
		}
	}
	if ValidDownloadFilename("folder/file.wav") || !ValidDownloadFilename("file.wav") {
		t.Fatal("ValidDownloadFilename() disagrees with the token filename rule")
	}
}

func TestFilenameExtension(t *testing.T) {
	for value, want := range map[string]string{"a.wav": "wav", "a": "", "a.": "", "a.b.c": "c"} {
		if got := FilenameExtension(value); got != want {
			t.Fatalf("FilenameExtension(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestJoinOrigin(t *testing.T) {
	for origin, want := range map[string]string{
		"":                         "/asset/id/image.webp",
		"http://cdn.example.com/":  "http://cdn.example.com/asset/id/image.webp",
		"https://cdn.example.com/": "https://cdn.example.com/asset/id/image.webp",
		"cdn.example.com/":         "https://cdn.example.com/asset/id/image.webp",
	} {
		if got := JoinOrigin(origin, "/asset/id/image.webp"); got != want {
			t.Fatalf("JoinOrigin(%q) = %q, want %q", origin, got, want)
		}
	}
}
