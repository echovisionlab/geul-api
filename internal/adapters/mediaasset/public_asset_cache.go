package mediaasset

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	mediaauth "github.com/echovisionlab/geul-mediaauth"

	mediaassetdomain "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
)

const (
	purgeBatchSize      = 30
	purgeMaxAttempts    = 3
	purgeRequestTimeout = 10 * time.Second
	purgeRetryDelay     = 250 * time.Millisecond
)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type PublicAssetCache struct {
	cdnURL     string
	apiURL     string
	zoneID     string
	apiToken   string
	httpClient HTTPDoer
}

var _ mediaassetdomain.PublicAssetCache = (*PublicAssetCache)(nil)

func NewPublicAssetCache(cdnURL, apiURL, zoneID, apiToken string, client HTTPDoer) *PublicAssetCache {
	if client == nil {
		client = http.DefaultClient
	}
	return &PublicAssetCache{
		cdnURL: strings.TrimSpace(cdnURL), apiURL: strings.TrimSpace(apiURL),
		zoneID: strings.TrimSpace(zoneID), apiToken: strings.TrimSpace(apiToken), httpClient: client,
	}
}

func (c *PublicAssetCache) Prefix(asset model.PublicAsset) (string, error) {
	assetPath, err := mediaauth.AssetPath(asset.ID, asset.Kind, asset.Extension)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(c.cdnURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("CDN_URL must be an absolute HTTP origin")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("CDN_URL must not include a path")
	}
	if !strings.HasPrefix(assetPath, "/asset/") {
		return "", fmt.Errorf("asset path must start with /asset/")
	}
	return parsed.Host + assetPath, nil
}

type PurgeRequest struct {
	Prefixes []string `json:"prefixes"`
}

type purgeResponse struct {
	Success bool `json:"success"`
	Errors  []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (c *PublicAssetCache) PurgePrefixes(ctx context.Context, prefixes []string) error {
	if len(prefixes) == 0 || len(prefixes) > purgeBatchSize {
		return fmt.Errorf("cloudflare purge requires 1 to %d prefixes", purgeBatchSize)
	}
	payload, err := json.Marshal(PurgeRequest{Prefixes: prefixes})
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 1; attempt <= purgeMaxAttempts; attempt++ {
		retryAfter, retry, err := c.purgeOnce(ctx, payload)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retry || attempt == purgeMaxAttempts {
			break
		}
		delay := purgeRetryDelay << (attempt - 1)
		if retryAfter > delay {
			delay = retryAfter
		}
		if delay > 5*time.Second {
			delay = 5 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func (c *PublicAssetCache) purgeOnce(ctx context.Context, payload []byte) (time.Duration, bool, error) {
	endpoint := strings.TrimRight(c.apiURL, "/") + "/zones/" + url.PathEscape(c.zoneID) + "/purge_cache"
	requestCtx, cancel := context.WithTimeout(ctx, purgeRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, true, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError, err
	}
	var result purgeResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError,
			fmt.Errorf("decode Cloudflare response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices || !result.Success {
		message := resp.Status
		if len(result.Errors) > 0 && strings.TrimSpace(result.Errors[0].Message) != "" {
			message = result.Errors[0].Message
		}
		return parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
			resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError,
			fmt.Errorf("cloudflare purge failed: %s", message)
	}
	return 0, false, nil
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := time.ParseDuration(value + "s"); err == nil && seconds >= 0 {
		return seconds
	}
	if retryAt, err := http.ParseTime(value); err == nil && retryAt.After(now) {
		return retryAt.Sub(now)
	}
	return 0
}
