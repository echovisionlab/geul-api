package proxy

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/echovisionlab/geul-api/internal/mediadelivery/config"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/minio/minio-go/v7"
)

type deliveryContextKey struct{}

type resolvedDelivery struct {
	path   mediaauth.DeliveryPath
	claims *mediaauth.Claims
	stat   minio.ObjectInfo
}

// MediaRouter is the only parser and authorization boundary for asset and media delivery.
type MediaRouter struct {
	cfg        *config.Config
	store      deliveryStore
	imageProxy *ImageProxy
	mediaProxy *MediaProxy
}

func NewMediaRouter(cfg *config.Config, client *minio.Client, bucket string, imageProxy *ImageProxy, mediaProxy *MediaProxy) *MediaRouter {
	return newMediaRouter(cfg, newMinioDeliveryStore(client, bucket), imageProxy, mediaProxy)
}

func newMediaRouter(cfg *config.Config, store deliveryStore, imageProxy *ImageProxy, mediaProxy *MediaProxy) *MediaRouter {
	return &MediaRouter{cfg: cfg, store: store, imageProxy: imageProxy, mediaProxy: mediaProxy}
}

func (m *MediaRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	setNoStoreCacheHeaders(w)
	m.setCORSHeaders(w, r)
	if handleDeliveryPreflight(w, r) {
		return
	}
	if !isDeliveryMethod(r.Method) {
		w.Header().Set("Allow", "GET, HEAD, OPTIONS")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resolved, ok := m.resolveDelivery(w, r)
	if !ok {
		return
	}
	m.serveResolvedDelivery(w, r, resolved)
}

func handleDeliveryPreflight(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodOptions {
		return false
	}
	if _, err := mediaauth.ParseDeliveryPath(r.URL.Path); err != nil {
		http.NotFound(w, r)
		return true
	}
	setNoStoreCacheHeaders(w)
	w.WriteHeader(http.StatusNoContent)
	return true
}

func isDeliveryMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func (m *MediaRouter) resolveDelivery(w http.ResponseWriter, r *http.Request) (*resolvedDelivery, bool) {
	deliveryPath, err := mediaauth.ParseDeliveryPath(r.URL.Path)
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	claims, err := m.authorize(r, deliveryPath)
	if err != nil {
		slog.Warn("media authorization rejected", "kind", deliveryPath.Kind, "fileId", deliveryPath.FileID, "error", err)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return nil, false
	}

	stat, err := m.store.Stat(r.Context(), deliveryPath.ObjectKey)
	if err != nil {
		slog.Info("delivery object not found", "kind", deliveryPath.Kind, "fileId", deliveryPath.FileID, "generationId", deliveryPath.GenerationID)
		http.NotFound(w, r)
		return nil, false
	}
	if deliveryPath.Kind == mediaauth.PathAsset && hasForbiddenPublicAssetDownloadMetadata(stat) {
		slog.Warn("public asset has forbidden download metadata", "assetId", deliveryPath.AssetID)
		setNoStoreCacheHeaders(w)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return nil, false
	}
	if !pathKindAllowsContentType(deliveryPath.Kind, deliveryExtension(deliveryPath), stat.ContentType) {
		slog.Error("delivery object metadata mismatch", "kind", deliveryPath.Kind, "fileId", deliveryPath.FileID, "assetId", deliveryPath.AssetID, "contentType", stat.ContentType)
		http.NotFound(w, r)
		return nil, false
	}
	return &resolvedDelivery{path: deliveryPath, claims: claims, stat: stat}, true
}

func (m *MediaRouter) serveResolvedDelivery(w http.ResponseWriter, r *http.Request, resolved *resolvedDelivery) {
	r = r.WithContext(context.WithValue(r.Context(), deliveryContextKey{}, resolved))
	hasAssetOptions := resolved.path.Kind == mediaauth.PathAsset && len(r.URL.Query()) != 0
	hasTransform := hasImageTransformQuery(r)
	isImage := isImageContentType(resolved.stat.ContentType)
	if hasAssetOptions && !hasTransform {
		http.Error(w, "Invalid asset options", http.StatusBadRequest)
		return
	}
	if hasAssetOptions && !isImage {
		http.Error(w, "Asset options are supported only for images", http.StatusBadRequest)
		return
	}
	if !isImage || !hasTransform {
		m.mediaProxy.ServeHTTP(w, r)
		return
	}
	if resolved.claims != nil && resolved.claims.Purpose != mediaauth.PurposeInline {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	m.imageProxy.ServeHTTP(w, r)
}

func (m *MediaRouter) authorize(r *http.Request, deliveryPath mediaauth.DeliveryPath) (*mediaauth.Claims, error) {
	if deliveryPath.Kind == mediaauth.PathAsset || deliveryPath.Kind == mediaauth.PathMediaHLS {
		return nil, nil
	}
	claims, err := mediaauth.ValidateToken(deliveryPath.Token, m.cfg.MediaSigningSecret)
	if err != nil {
		return nil, err
	}
	if !claims.AllowsRequest(r.Method, deliveryPath.ObjectKey) {
		return nil, mediaauth.ErrInvalidToken
	}
	return claims, nil
}

func (m *MediaRouter) setCORSHeaders(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Vary", "Origin")
	origin := r.Header.Get("Origin")
	if origin == "" || !isAllowedOrigin(origin, m.cfg.AllowedOrigins) {
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Range")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Range, Content-Length, Accept-Ranges, Content-Disposition")
}

func resolvedDeliveryFromRequest(r *http.Request) (*resolvedDelivery, error) {
	resolved, ok := r.Context().Value(deliveryContextKey{}).(*resolvedDelivery)
	if !ok || resolved == nil {
		return nil, errors.New("delivery context is missing")
	}
	return resolved, nil
}
