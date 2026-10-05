package mediadelivery

import (
	"net/http"

	"github.com/echovisionlab/geul-api/internal/mediadelivery/config"
	"github.com/echovisionlab/geul-api/internal/mediadelivery/proxy"
	"github.com/echovisionlab/geul-api/internal/mediadelivery/storage"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func NewHandler(
	cfg *config.Config,
	minioClient *storage.MinioClient,
) http.Handler {
	fontProxy := proxy.NewFontProxy(cfg, minioClient)
	fontCSSProxy := proxy.NewFontCSSProxy(cfg, minioClient)
	imageProxy := proxy.NewImageProxy(cfg)
	mediaProxy := proxy.NewMediaProxy(minioClient.Client(), cfg.S3MediaBucket)
	mediaRouter := proxy.NewMediaRouter(cfg, minioClient.Client(), cfg.S3MediaBucket, imageProxy, mediaProxy)

	mux := http.NewServeMux()
	mux.Handle("/fonts/css2", fontCSSProxy)
	mux.Handle("/fonts/", fontProxy)
	mux.Handle("/asset/", mediaRouter)
	mux.Handle("/media/", mediaRouter)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Not found", http.StatusNotFound)
	})
	return redactDeliveryTokenForTelemetry(otelhttp.NewHandler(restoreDeliveryPath(mux), "geul-cdn-http"))
}
