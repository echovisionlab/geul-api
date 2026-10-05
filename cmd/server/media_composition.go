package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/config"
	"github.com/echovisionlab/geul-api/internal/mediadelivery"
	deliveryconfig "github.com/echovisionlab/geul-api/internal/mediadelivery/config"
	"github.com/echovisionlab/geul-api/internal/mediadelivery/storage"
)

// Delivery uses its own listener within the API process: signed media paths
// have different CORS, telemetry redaction and streaming deadlines from RPCs.
func newMediaHTTPServer(cfg *config.Config) (*http.Server, error) {
	delivery := &deliveryconfig.Config{
		S3Endpoint: strings.TrimPrefix(strings.TrimPrefix(cfg.S3Endpoint, "https://"), "http://"),
		S3UseSSL:   strings.HasPrefix(cfg.S3Endpoint, "https://"),
		S3Region:   cfg.S3Region, S3AccessKeyID: cfg.S3AccessKeyID, S3SecretAccessKey: cfg.S3SecretAccessKey,
		S3CacheBucket: cfg.S3CacheBucket, S3MediaBucket: cfg.S3Bucket,
		FontS3Prefix: cfg.Media.FontS3Prefix, FontUpstreamURL: cfg.Media.FontUpstreamURL, FontCSSUpstream: cfg.Media.FontCSSUpstream, FontCacheMaxAge: cfg.Media.FontCacheMaxAge,
		ImgproxyURL: cfg.Media.ImgproxyURL, ImgproxyKey: cfg.Media.ImgproxyKey, ImgproxySalt: cfg.Media.ImgproxySalt,
		CDNPublicURL: cfg.CDNURL, MediaSigningSecret: cfg.MediaSigningSecret, AllowedOrigins: cfg.CORSOrigins,
	}
	store, err := storage.NewMinioClient(delivery)
	if err != nil {
		return nil, err
	}
	return &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Media.DeliveryPort),
		Handler:           mediadelivery.NewHandler(delivery, store),
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
	}, nil
}
