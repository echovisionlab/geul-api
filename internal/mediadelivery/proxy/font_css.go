package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/echovisionlab/geul-api/internal/mediadelivery/config"
	"github.com/echovisionlab/geul-api/internal/mediadelivery/storage"
)

const (
	// fontCSSCacheVersion isolates deterministic WOFF2 CSS from legacy cache
	// entries whose format depended on the first client's User-Agent.
	fontCSSCacheVersion = "v2"

	// googleFontsWOFF2UserAgent is a fixed capability token, not a client
	// identity. Google Fonts varies CSS by User-Agent; this evergreen browser
	// profile requests WOFF2 CSS that is shared by current Chrome, Safari, and
	// Firefox instead of letting the first client poison the shared cache.
	googleFontsWOFF2UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

type FontCSSProxy struct {
	cfg           *config.Config
	storage       cacheStore
	client        *http.Client
	logger        *slog.Logger
	runBackground func(func())
}

func NewFontCSSProxy(cfg *config.Config, storage *storage.MinioClient) *FontCSSProxy {
	return &FontCSSProxy{
		cfg:           cfg,
		storage:       storage,
		client:        newUpstreamHTTPClient(),
		logger:        slog.Default(),
		runBackground: runInBackground,
	}
}

func (p *FontCSSProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	query := r.URL.RawQuery
	if query == "" {
		http.Error(w, "Missing query parameters", http.StatusBadRequest)
		return
	}

	// Cache key based on schema version and query hash (includes family,
	// display, etc.). The version deliberately bypasses User-Agent-dependent
	// CSS cached before responses became deterministic.
	queryHash := p.hashQuery(query)
	cacheKey := "font-css/" + fontCSSCacheVersion + "/" + queryHash + ".css"

	data, _, err := p.storage.Get(r.Context(), cacheKey)
	if err == nil {
		p.logger.Debug("font CSS cache hit", "query_hash", queryHash)
		p.respond(w, r, data)
		return
	}

	p.logger.Debug("font CSS cache miss, fetching from upstream", "query_hash", queryHash)

	upstreamURL := p.cfg.FontCSSUpstream + "/css2?" + query

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstreamURL, nil)
	if err != nil {
		p.logger.Error("failed to create request", "error", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Always request one modern, cross-browser WOFF2 representation. Cache
	// entries must never depend on the requesting client's User-Agent.
	req.Header.Set("User-Agent", googleFontsWOFF2UserAgent)

	resp, err := p.client.Do(req)
	if err != nil {
		p.logger.Error("failed to fetch from upstream", "error", err, "query_hash", queryHash)
		http.Error(w, "Failed to fetch font CSS", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		p.logUpstreamFailure(resp.StatusCode, queryHash)
		http.Error(w, "Upstream error", resp.StatusCode)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		p.logger.Error("failed to read response", "error", err, "query_hash", queryHash)
		http.Error(w, "Failed to read response", http.StatusInternalServerError)
		return
	}

	css := strings.ReplaceAll(string(body), "https://fonts.gstatic.com", p.cfg.CDNPublicURL+"/fonts")
	data = []byte(css)

	cacheResponseInBackground(
		p.runBackground,
		p.storage,
		cacheKey,
		data,
		"text/css; charset=utf-8",
		"font CSS",
		"query", query,
	)

	p.respond(w, r, data)
}

func (p *FontCSSProxy) logUpstreamFailure(status int, queryHash string) {
	log := p.logger.Error
	if status >= http.StatusBadRequest && status < http.StatusInternalServerError {
		log = p.logger.Warn
	}
	log("upstream returned error", "status", status, "query_hash", queryHash)
}

func (p *FontCSSProxy) hashQuery(query string) string {
	h := sha256.Sum256([]byte(query))
	return hex.EncodeToString(h[:16]) // 32 chars
}

func (p *FontCSSProxy) respond(w http.ResponseWriter, r *http.Request, data []byte) {
	writeCacheableResponse(
		w,
		r,
		data,
		"text/css; charset=utf-8",
		fmt.Sprintf("public, max-age=%d", p.cfg.FontCacheMaxAge),
		p.cfg.AllowedOrigins,
	)
}
