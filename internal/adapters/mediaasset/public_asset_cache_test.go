package mediaasset

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/model"
)

func TestCloudflareCachePrefixUsesProviderHostPathGrammar(t *testing.T) {
	t.Parallel()

	asset := model.PublicAsset{ID: "11111111-1111-4111-8111-111111111111", Kind: "image", Extension: "webp"}
	for _, enabled := range []bool{true, false} {
		prefix, err := NewPublicAssetCache("https://cdn.example.com/", "", "", "", enabled, nil).Prefix(asset)
		require.NoError(t, err)
		require.Equal(t, "cdn.example.com/asset/11111111-1111-4111-8111-111111111111/image.webp", prefix)

		for _, value := range []string{
			"cdn.example.com",
			"https://cdn.example.com/base",
			"https://cdn.example.com?x=1",
		} {
			_, err := NewPublicAssetCache(value, "", "", "", enabled, nil).Prefix(asset)
			require.Error(t, err, value)
		}
		_, err = NewPublicAssetCache("https://cdn.example.com/", "", "", "", enabled, nil).Prefix(model.PublicAsset{ID: "invalid", Kind: "image", Extension: "webp"})
		require.Error(t, err)
	}
}

type forbiddenPurgeClient struct{ t *testing.T }

func (c forbiddenPurgeClient) Do(*http.Request) (*http.Response, error) {
	c.t.Error("disabled purge must not send an HTTP request")
	return nil, fmt.Errorf("unexpected purge request")
}

func TestCloudflareCacheDisabledSkipsHTTPButHonorsBatchAndCancellation(t *testing.T) {
	t.Parallel()
	cache := NewPublicAssetCache("https://cdn.example.com", "", "", "", false, forbiddenPurgeClient{t})
	prefix, err := cache.Prefix(model.PublicAsset{ID: "11111111-1111-4111-8111-111111111111", Kind: "image", Extension: "webp"})
	require.NoError(t, err)
	require.NoError(t, cache.PurgePrefixes(t.Context(), []string{prefix}))
	require.Error(t, cache.PurgePrefixes(t.Context(), nil))
	require.Error(t, cache.PurgePrefixes(t.Context(), make([]string, purgeBatchSize+1)))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, cache.PurgePrefixes(ctx, []string{prefix}), context.Canceled)
}

func TestCloudflareCacheEnabledPurgeAndRetry(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name          string
		status        int
		succeedsAfter int32
		wantAttempts  int32
		wantError     bool
	}{
		{name: "success", status: http.StatusOK, succeedsAfter: 0, wantAttempts: 1},
		{name: "rate limit retry", status: http.StatusTooManyRequests, succeedsAfter: 1, wantAttempts: 2},
		{name: "server failure retry", status: http.StatusInternalServerError, succeedsAfter: 1, wantAttempts: 2},
		{name: "retry exhausted", status: http.StatusServiceUnavailable, succeedsAfter: purgeMaxAttempts, wantAttempts: purgeMaxAttempts, wantError: true},
		{name: "forbidden stops", status: http.StatusForbidden, succeedsAfter: 1, wantAttempts: 1, wantError: true},
		{name: "provider failure stops", status: http.StatusOK, succeedsAfter: 1, wantAttempts: 1, wantError: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			prefixes := []string{"cdn.example.com/asset/11111111-1111-4111-8111-111111111111/image.webp"}
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "/zones/test-zone/purge_cache", r.URL.Path)
				assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
				assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
				var payload PurgeRequest
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
				assert.Equal(t, prefixes, payload.Prefixes)
				if attempts.Add(1) <= testCase.succeedsAfter {
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(testCase.status)
					_, _ = w.Write([]byte(`{"success":false,"errors":[{"message":"purge unavailable"}]}`))
					return
				}
				_, _ = w.Write([]byte(`{"success":true,"errors":[]}`))
			}))
			t.Cleanup(server.Close)
			cache := NewPublicAssetCache("https://cdn.example.com", server.URL, "test-zone", "test-token", true, server.Client())
			err := cache.PurgePrefixes(t.Context(), prefixes)
			if testCase.wantError {
				require.ErrorContains(t, err, "purge unavailable")
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, testCase.wantAttempts, attempts.Load())
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	require.Equal(t, 3*time.Second, parseRetryAfter("3", now))
	require.Equal(t, 2*time.Second, parseRetryAfter(now.Add(2*time.Second).Format(http.TimeFormat), now))
	require.Zero(t, parseRetryAfter("invalid", now))
}
