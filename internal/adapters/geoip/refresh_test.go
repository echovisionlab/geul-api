package geoipadapter

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type geoIPHTTPDoerFunc func(*http.Request) (*http.Response, error)

func (fn geoIPHTTPDoerFunc) Do(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestGeoIPDownloadURLPreservesCredentialValues(t *testing.T) {
	downloadURL := geoIPDownloadURL("account + value", "license&value")
	parsed, err := url.Parse(downloadURL)
	require.NoError(t, err)
	require.Equal(t, "https", parsed.Scheme)
	require.Equal(t, "download.maxmind.com", parsed.Host)
	require.Equal(t, "account + value", parsed.Query().Get("account_id"))
	require.Equal(t, "license&value", parsed.Query().Get("license_key"))
	require.Equal(t, "zip", parsed.Query().Get("suffix"))
}

func TestDownloadGeoIPArchiveStreamsToRemovableTemporaryFile(t *testing.T) {
	doer := geoIPHTTPDoerFunc(func(req *http.Request) (*http.Response, error) {
		require.NoError(t, req.Context().Err())
		return &http.Response{
			StatusCode:    http.StatusOK,
			ContentLength: 7,
			Body:          io.NopCloser(strings.NewReader("archive")),
			Header:        make(http.Header),
		}, nil
	})

	archive, err := downloadGeoIPArchive(context.Background(), doer, "https://download.example.test/archive.zip")
	require.NoError(t, err)
	require.Equal(t, int64(7), archive.size)
	path := archive.path
	_, err = archive.file.Seek(0, io.SeekStart)
	require.NoError(t, err)
	body, err := io.ReadAll(archive.file)
	require.NoError(t, err)
	require.Equal(t, "archive", string(body))
	require.NoError(t, archive.Close())
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestDownloadGeoIPArchiveRejectsDeclaredOversizeWithoutReadingBody(t *testing.T) {
	read := false
	doer := geoIPHTTPDoerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			ContentLength: geoIPMaxArchiveBytes + 1,
			Body: io.NopCloser(readerFunc(func([]byte) (int, error) {
				read = true
				return 0, io.EOF
			})),
			Header: make(http.Header),
		}, nil
	})

	archive, err := downloadGeoIPArchive(context.Background(), doer, "https://download.example.test/archive.zip")
	require.Nil(t, archive)
	require.ErrorContains(t, err, "size limit")
	require.False(t, read)
}

func TestDownloadGeoIPArchiveRejectsRedirectResponse(t *testing.T) {
	doer := geoIPHTTPDoerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusFound,
			Body:       io.NopCloser(strings.NewReader("redirect")),
			Header:     http.Header{"Location": []string{"https://other.example.test/archive.zip"}},
		}, nil
	})

	archive, err := downloadGeoIPArchive(context.Background(), doer, "https://download.example.test/archive.zip")
	require.Nil(t, archive)
	require.ErrorContains(t, err, "status 302")
}

func TestGeoIPHTTPClientAllowsOnlyOfficialHTTPSRedirect(t *testing.T) {
	client := newGeoIPHTTPClient()
	via := []*http.Request{{URL: mustParseURL(t, "https://download.maxmind.com/geoip/databases/GeoLite2-City-CSV/download?license_key=secret")}}
	allowed := &http.Request{
		URL: mustParseURL(t, "https://"+geoIPRedirectHost+"/signed/archive.zip?token=provider-token"),
		Header: http.Header{
			"Referer":       []string{via[0].URL.String()},
			"Authorization": []string{"Basic secret"},
		},
	}
	require.NoError(t, client.CheckRedirect(allowed, via))
	require.Empty(t, allowed.Header.Get("Referer"))
	require.Empty(t, allowed.Header.Get("Authorization"))

	for _, target := range []string{
		"http://" + geoIPRedirectHost + "/archive.zip",
		"https://attacker.example.test/archive.zip",
		"https://" + geoIPRedirectHost + ":8443/archive.zip",
		"https://user@" + geoIPRedirectHost + "/archive.zip",
	} {
		err := client.CheckRedirect(&http.Request{URL: mustParseURL(t, target), Header: make(http.Header)}, via)
		require.ErrorContains(t, err, "not allowed", target)
	}

	tooMany := []*http.Request{via[0], via[0], via[0]}
	err := client.CheckRedirect(allowed, tooMany)
	require.ErrorContains(t, err, "exceeded")
}

func TestGeoIPCSVValueParsersFailClosed(t *testing.T) {
	_, err := optionalCSVInt("not-an-int")
	require.Error(t, err)
	_, err = requiredCSVBool("sometimes")
	require.Error(t, err)
	_, err = optionalLocationWKT("37.1", "")
	require.Error(t, err)
	_, err = optionalLocationWKT("91", "127")
	require.Error(t, err)
	_, err = requiredPositiveInt("0")
	require.Error(t, err)
}

type readerFunc func([]byte) (int, error)

func (fn readerFunc) Read(buffer []byte) (int, error) {
	return fn(buffer)
}

func mustParseURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	require.NoError(t, err)
	return parsed
}
