package proxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/echovisionlab/geul-api/internal/mediadelivery/config"
	"github.com/echovisionlab/geul-api/internal/mediadelivery/storage"
)

type FontProxy struct {
	cfg           *config.Config
	storage       cacheStore
	client        *http.Client
	runBackground func(func())
}

func NewFontProxy(cfg *config.Config, storage *storage.MinioClient) *FontProxy {
	return &FontProxy{
		cfg:           cfg,
		storage:       storage,
		client:        newUpstreamHTTPClient(),
		runBackground: runInBackground,
	}
}

func (p *FontProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/fonts/")
	if path == "" {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	cacheKey := p.cfg.FontS3Prefix + path
	ctx := r.Context()

	data, contentType, err := p.storage.Get(ctx, cacheKey)
	if err == nil {
		slog.Debug("font cache hit", "path", path)
		p.respond(w, r, data, contentType)
		return
	}

	slog.Debug("font cache miss, fetching from upstream", "path", path)
	data, contentType, err = p.fetchFromUpstream(ctx, path)
	if err != nil {
		slog.Error("failed to fetch from upstream", "path", path, "error", err)
		http.Error(w, "Failed to fetch font", http.StatusBadGateway)
		return
	}

	cacheResponseInBackground(
		p.runBackground,
		p.storage,
		cacheKey,
		data,
		contentType,
		"font",
		"path", path,
	)

	p.respond(w, r, data, contentType)
}

func (p *FontProxy) fetchFromUpstream(ctx context.Context, path string) ([]byte, string, error) {
	url := fmt.Sprintf("%s/%s", p.cfg.FontUpstreamURL, path)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("upstream returned %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "font/woff2"
	}

	return data, contentType, nil
}

func (p *FontProxy) respond(w http.ResponseWriter, r *http.Request, data []byte, contentType string) {
	writeCacheableResponse(
		w,
		r,
		data,
		contentType,
		fmt.Sprintf("public, max-age=%d, immutable", p.cfg.FontCacheMaxAge),
		p.cfg.AllowedOrigins,
	)
}
