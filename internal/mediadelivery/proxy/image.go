package proxy

import (
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/echovisionlab/geul-api/internal/mediadelivery/config"
)

type ImageProxy struct {
	cfg    *config.Config
	client *http.Client
}

func NewImageProxy(cfg *config.Config) *ImageProxy {
	return &ImageProxy{cfg: cfg, client: newUpstreamHTTPClient()}
}

func (p *ImageProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	resolved, err := resolvedDeliveryFromRequest(r)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	options, err := parseImageOptions(r.URL.Query(), resolved.claims == nil)
	if err != nil {
		http.Error(w, "Invalid image options", http.StatusBadRequest)
		return
	}
	imgproxyURL, err := p.buildImgproxyURL(resolved.path.ObjectKey, options)
	if err != nil {
		slog.Error("failed to sign imgproxy request", "error", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, imgproxyURL, nil)
	if err != nil {
		slog.Error("failed to create imgproxy request", "error", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if accept := r.Header.Get("Accept"); accept != "" {
		req.Header.Set("Accept", accept)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		slog.Error("failed to call imgproxy", "error", err)
		http.Error(w, "Bad gateway", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	p.writeImgproxyResponse(w, r, resolved, resp)
}

func (p *ImageProxy) writeImgproxyResponse(
	w http.ResponseWriter,
	r *http.Request,
	resolved *resolvedDelivery,
	resp *http.Response,
) {
	if resp.StatusCode != http.StatusOK {
		errorBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		slog.Error("imgproxy returned error",
			"status", resp.StatusCode,
			"path", resolved.path.ObjectKey,
			"contentType", resp.Header.Get("Content-Type"),
			"body", strings.TrimSpace(string(errorBody)),
		)
		http.Error(w, "Image not found", resp.StatusCode)
		return
	}

	if contentType := resp.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	setDeliveryCacheHeaders(w, resolved.claims)
	if contentLength := resp.Header.Get("Content-Length"); contentLength != "" {
		w.Header().Set("Content-Length", contentLength)
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(resp.StatusCode)
		return
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		slog.Error("failed to stream imgproxy response", "error", err, "assetId", resolved.path.AssetID, "fileId", resolved.path.FileID)
	}
}
