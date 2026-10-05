package config

import (
	"strings"
	"testing"
)

func TestLoadDerivesMinioEndpointAndAllowedOrigins(t *testing.T) {
	setValidConfigEnvironment(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.S3Endpoint != "minio.example" {
		t.Fatalf("S3Endpoint = %q", cfg.S3Endpoint)
	}
	if !cfg.S3UseSSL {
		t.Fatal("expected S3UseSSL")
	}
	if len(cfg.AllowedOrigins) != 2 || cfg.AllowedOrigins[0] != "https://app.example" {
		t.Fatalf("AllowedOrigins = %#v", cfg.AllowedOrigins)
	}
	if cfg.MediaSigningSecret != "secret" {
		t.Fatalf("MediaSigningSecret = %q", cfg.MediaSigningSecret)
	}
}

func TestLoadRequiresMediaSigningSecret(t *testing.T) {
	setValidConfigEnvironment(t)
	t.Setenv("MEDIA_SIGNING_SECRET", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load accepted an empty MEDIA_SIGNING_SECRET")
	}
	if !strings.Contains(err.Error(), "MEDIA_SIGNING_SECRET") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsInvalidImgproxySecrets(t *testing.T) {
	for _, tt := range []struct {
		name  string
		key   string
		value string
	}{
		{"key", "IMGPROXY_KEY", "not-hex"},
		{"salt", "IMGPROXY_SALT", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setValidConfigEnvironment(t)
			t.Setenv(tt.key, tt.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load accepted invalid %s", tt.key)
			}
		})
	}
	if err := validateHexSecret("SECRET", "00ff"); err != nil {
		t.Fatalf("validateHexSecret rejected valid value: %v", err)
	}
}

func TestLoadReturnsEnvconfigError(t *testing.T) {
	t.Setenv("CDN_PORT", "8080")

	if _, err := Load(); err == nil {
		t.Fatal("expected missing required env error")
	}
}

func setValidConfigEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("CDN_PORT", "8080")
	t.Setenv("S3_ENDPOINT", "https://minio.example")
	t.Setenv("S3_REGION", "us-east-1")
	t.Setenv("S3_ACCESS_KEY_ID", "access")
	t.Setenv("S3_SECRET_ACCESS_KEY", "secret")
	t.Setenv("S3_CACHE_BUCKET", "cache")
	t.Setenv("S3_MEDIA_BUCKET", "media")
	t.Setenv("CDN_FONT_S3_PREFIX", "fonts/")
	t.Setenv("CDN_FONT_UPSTREAM_URL", "https://fonts.gstatic.com")
	t.Setenv("CDN_FONT_CSS_UPSTREAM", "https://fonts.googleapis.com")
	t.Setenv("CDN_FONT_CACHE_MAX_AGE", "3600")
	t.Setenv("CDN_PUBLIC_URL", "https://cdn.example")
	t.Setenv("CDN_IMGPROXY_URL", "https://imgproxy.example")
	t.Setenv("IMGPROXY_KEY", "0011")
	t.Setenv("IMGPROXY_SALT", "2233")
	t.Setenv("MEDIA_SIGNING_SECRET", "secret")
	t.Setenv("CDN_ALLOWED_ORIGINS", "https://app.example,https://admin.example")
}
