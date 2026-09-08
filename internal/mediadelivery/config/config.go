package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/kelseyhightower/envconfig"
)

type Config struct {
	Port string `envconfig:"CDN_PORT" required:"true"`

	// S3/MinIO
	S3Endpoint        string `envconfig:"S3_ENDPOINT" required:"true"`
	S3Region          string `envconfig:"S3_REGION" required:"true"`
	S3AccessKeyID     string `envconfig:"S3_ACCESS_KEY_ID" required:"true"`
	S3SecretAccessKey string `envconfig:"S3_SECRET_ACCESS_KEY" required:"true"`
	S3CacheBucket     string `envconfig:"S3_CACHE_BUCKET" required:"true"`
	S3MediaBucket     string `envconfig:"S3_MEDIA_BUCKET" required:"true"`
	S3UseSSL          bool   `ignored:"true"` // Derived from S3Endpoint

	// Font Proxy
	FontS3Prefix    string `envconfig:"CDN_FONT_S3_PREFIX" required:"true"`
	FontUpstreamURL string `envconfig:"CDN_FONT_UPSTREAM_URL" required:"true"`
	FontCSSUpstream string `envconfig:"CDN_FONT_CSS_UPSTREAM" required:"true"`
	FontCacheMaxAge int    `envconfig:"CDN_FONT_CACHE_MAX_AGE" required:"true"`
	CDNPublicURL    string `envconfig:"CDN_PUBLIC_URL" required:"true"`

	// Image Proxy
	ImgproxyURL  string `envconfig:"CDN_IMGPROXY_URL" required:"true"`
	ImgproxyKey  string `envconfig:"IMGPROXY_KEY" required:"true"`
	ImgproxySalt string `envconfig:"IMGPROXY_SALT" required:"true"`

	// Signed media delivery
	MediaSigningSecret string `envconfig:"MEDIA_SIGNING_SECRET" required:"true"`

	// CORS
	AllowedOrigins []string `envconfig:"CDN_ALLOWED_ORIGINS" required:"true"`
}

func Load() (*Config, error) {
	if _, ok := os.LookupEnv("TOKEN_SIGNING_SECRET"); ok {
		return nil, fmt.Errorf("TOKEN_SIGNING_SECRET is no longer supported; use MEDIA_SIGNING_SECRET")
	}

	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.MediaSigningSecret) == "" {
		return nil, fmt.Errorf("MEDIA_SIGNING_SECRET is required")
	}

	// Derive S3UseSSL from endpoint
	cfg.S3UseSSL = strings.HasPrefix(cfg.S3Endpoint, "https://")

	// Strip protocol from endpoint for MinIO client
	cfg.S3Endpoint = strings.TrimPrefix(cfg.S3Endpoint, "https://")
	cfg.S3Endpoint = strings.TrimPrefix(cfg.S3Endpoint, "http://")
	if err := validateHexSecret("IMGPROXY_KEY", cfg.ImgproxyKey); err != nil {
		return nil, err
	}
	if err := validateHexSecret("IMGPROXY_SALT", cfg.ImgproxySalt); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func validateHexSecret(name, value string) error {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) == 0 {
		return fmt.Errorf("%s must be non-empty, even-length hexadecimal", name)
	}
	return nil
}
