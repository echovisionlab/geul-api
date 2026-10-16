package proxy

import (
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/echovisionlab/geul-api/internal/failurecause"
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
		slog.ErrorContext(r.Context(), "Missing image delivery context", failurecause.Attr(err))
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	options, err := parseImageOptions(r.URL.Query(), resolved.claims == nil)
	if err != nil {
		slog.WarnContext(r.Context(), "Invalid image options", failurecause.Attr(err))
		http.Error(w, "Invalid image options", http.StatusBadRequest)
		return
	}
	imgproxyURL, err := p.buildImgproxyURL(resolved.path.ObjectKey, options)
	if err != nil {
		slog.ErrorContext(r.Context(), "failed to sign imgproxy request", failurecause.Attr(err), "assetId", resolved.path.AssetID, "fileId", resolved.path.FileID)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	signature, _, _ := strings.Cut(strings.TrimPrefix(imgproxyURL, p.cfg.ImgproxyURL+"/"), "/")
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, imgproxyURL, nil)
	if err != nil {
		slog.ErrorContext(r.Context(), "failed to create imgproxy request", failurecause.Attr(err, signature), "assetId", resolved.path.AssetID, "fileId", resolved.path.FileID)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if accept := r.Header.Get("Accept"); accept != "" {
		req.Header.Set("Accept", accept)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		slog.ErrorContext(r.Context(), "failed to call imgproxy", failurecause.Attr(err, signature), "assetId", resolved.path.AssetID, "fileId", resolved.path.FileID)
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
		_, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		if readErr != nil {
			slog.ErrorContext(r.Context(), "Failed to read imgproxy error response", failurecause.Attr(readErr), "assetId", resolved.path.AssetID, "fileId", resolved.path.FileID)
		}
		level := slog.LevelError
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			level = slog.LevelWarn
		}
		slog.Log(r.Context(), level, "imgproxy returned error", "status", resp.StatusCode, "assetId", resolved.path.AssetID, "fileId", resolved.path.FileID)
		status, message := resp.StatusCode, "Image service rejected the request"
		if status == http.StatusNotFound {
			message = "Image not found"
		} else if status >= 500 {
			status, message = http.StatusBadGateway, "Image service unavailable"
		}
		http.Error(w, message, status)
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
		slog.ErrorContext(r.Context(), "failed to stream imgproxy response", failurecause.Attr(err), "assetId", resolved.path.AssetID, "fileId", resolved.path.FileID)
	}
}
