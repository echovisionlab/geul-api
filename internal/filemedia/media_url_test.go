package filemedia

import (
	"net/url"
	"strings"
	"testing"
	"time"

	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/stretchr/testify/require"
)

func signedMediaClaimsFromURL(t *testing.T, signedURL string) *mediaauth.Claims {
	t.Helper()

	parsed, err := url.Parse(signedURL)
	require.NoError(t, err)

	trimmedPath := strings.TrimPrefix(parsed.Path, "/media/")
	parts := strings.Split(trimmedPath, "/")
	require.GreaterOrEqual(t, len(parts), 2)

	claims, err := mediaauth.ValidateToken(parts[0], "secret")
	require.NoError(t, err)
	return claims
}

func TestBuildSignedMediaFileURL(t *testing.T) {
	url, err := BuildSignedMediaFileURL(
		"cdn.example.com",
		"11111111-1111-4111-8111-111111111111",
		"mp3",
		"secret",
		time.Minute,
		mediaauth.PurposeDownload,
	)
	require.NoError(t, err)
	require.Contains(t, url, "https://cdn.example.com/media/")
	require.True(t, strings.HasSuffix(url, "/11111111-1111-4111-8111-111111111111.mp3"))

	claims := signedMediaClaimsFromURL(t, url)
	require.Equal(t, mediaauth.ScopeExact, claims.ScopeType)
	require.Equal(t, "media/11111111-1111-4111-8111-111111111111.mp3", claims.ScopeValue)
}

func TestBuildSignedMediaDownloadURLIncludesFilenameClaim(t *testing.T) {
	url, err := BuildSignedMediaDownloadURL(
		"cdn.example.com",
		"11111111-1111-4111-8111-111111111111",
		"wav",
		"secret",
		time.Minute,
		"간월재 원본.wav",
	)
	require.NoError(t, err)

	claims := signedMediaClaimsFromURL(t, url)
	require.Equal(t, mediaauth.PurposeDownload, claims.Purpose)
	require.Equal(t, "간월재 원본.wav", claims.Filename)
}

func TestBuildPublicMediaHLSURL(t *testing.T) {
	url, err := BuildPublicMediaHLSURL(
		"cdn.example.com",
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"master.m3u8",
	)
	require.NoError(t, err)
	require.Equal(t, "https://cdn.example.com/media/11111111-1111-4111-8111-111111111111/hls/22222222-2222-4222-8222-222222222222/master.m3u8", url)
}

func TestBuildSignedMediaURLValidation(t *testing.T) {
	fileID := "11111111-1111-4111-8111-111111111111"
	generationID := "22222222-2222-4222-8222-222222222222"

	_, err := BuildSignedMediaFileURL("", fileID, "mp3", "", time.Minute, mediaauth.PurposeInline)
	require.ErrorIs(t, err, ErrMediaURLSigningFailed)
	_, err = BuildSignedMediaFileURL("", "not-a-uuid", "mp3", "secret", time.Minute, mediaauth.PurposeInline)
	require.ErrorIs(t, err, ErrMediaURLSigningFailed)
	_, err = BuildSignedMediaFileURL("", fileID, "mp3", "secret", time.Minute, mediaauth.Purpose("invalid"))
	require.ErrorIs(t, err, ErrMediaURLSigningFailed)

	_, err = BuildPublicMediaHLSURL("", "not-a-uuid", generationID, "master.m3u8")
	require.ErrorIs(t, err, ErrMediaURLSigningFailed)
	_, err = BuildPublicMediaHLSURL("", fileID, generationID, "../master.m3u8")
	require.ErrorIs(t, err, ErrMediaURLSigningFailed)
	_, err = BuildPublicMediaHLSURL("", fileID, generationID, "segment_001.m4s")
	require.ErrorIs(t, err, ErrMediaURLSigningFailed)

	_, err = generateMediaToken(mediaauth.Purpose("invalid"), mediaauth.ScopeExact, "media/object.bin", time.Minute, "secret", "")
	require.ErrorIs(t, err, ErrMediaURLSigningFailed)
	token, err := generateMediaToken(mediaauth.PurposeInline, mediaauth.ScopeExact, "media/"+fileID+".mp3", 0, "secret", "")
	require.NoError(t, err)
	require.NotEmpty(t, token)
}

func TestMediaURLHelpers(t *testing.T) {
	for _, testCase := range []struct {
		purpose mediaauth.Purpose
		want    time.Duration
		ok      bool
	}{
		{purpose: mediaauth.PurposeInline, want: mediaauth.InlineTTL, ok: true},
		{purpose: mediaauth.PurposeDownload, want: mediaauth.DownloadTTL, ok: true},
		{purpose: mediaauth.Purpose("invalid")},
	} {
		got, ok := mediaTokenMaxTTL(testCase.purpose)
		require.Equal(t, testCase.ok, ok)
		require.Equal(t, testCase.want, got)
	}

	require.Equal(t, "bin", mediaExtension(nil))
	empty := ""
	require.Equal(t, "bin", mediaExtension(&empty))
	mp3 := "audio/mpeg"
	require.Equal(t, "mp3", mediaExtension(&mp3))

	require.Equal(t, "/asset/id/image.webp", joinOriginPath("", "/asset/id/image.webp"))
	require.Equal(t, "http://cdn.example.com/asset/id/image.webp", joinOriginPath("http://cdn.example.com/", "/asset/id/image.webp"))
	require.Equal(t, "https://cdn.example.com/asset/id/image.webp", joinOriginPath("https://cdn.example.com/", "/asset/id/image.webp"))
	require.Equal(t, "https://cdn.example.com/asset/id/image.webp", joinOriginPath("cdn.example.com/", "/asset/id/image.webp"))
}
