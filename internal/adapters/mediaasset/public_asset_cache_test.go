package mediaasset

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/model"
)

func TestCloudflareCachePrefixUsesProviderHostPathGrammar(t *testing.T) {
	t.Parallel()

	asset := model.PublicAsset{ID: "11111111-1111-4111-8111-111111111111", Kind: "image", Extension: "webp"}
	prefix, err := NewPublicAssetCache("https://cdn.example.com/", "", "", "", nil).Prefix(asset)
	require.NoError(t, err)
	require.Equal(t, "cdn.example.com/asset/11111111-1111-4111-8111-111111111111/image.webp", prefix)

	for _, value := range []string{
		"cdn.example.com",
		"https://cdn.example.com/base",
		"https://cdn.example.com?x=1",
	} {
		_, err := NewPublicAssetCache(value, "", "", "", nil).Prefix(asset)
		require.Error(t, err, value)
	}
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	require.Equal(t, 3*time.Second, parseRetryAfter("3", now))
	require.Equal(t, 2*time.Second, parseRetryAfter(now.Add(2*time.Second).Format(http.TimeFormat), now))
	require.Zero(t, parseRetryAfter("invalid", now))
}
