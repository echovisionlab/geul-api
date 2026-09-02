package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/google/uuid"
	"github.com/kelseyhightower/envconfig"
)

type Config struct {
	Port           int `envconfig:"PORT" required:"true"`
	MCPPrivatePort int `envconfig:"MCP_PRIVATE_PORT" default:"8001"`

	AuthHeaderName            string `envconfig:"AUTH_HEADER_NAME" required:"true"`
	InternalServiceHeaderName string `envconfig:"INTERNAL_SERVICE_HEADER_NAME" required:"true"`

	DatabaseDSN        string   `envconfig:"DATABASE_DSN" required:"true"`
	DatabaseMaxOpen    int      `envconfig:"DATABASE_MAX_OPEN_CONNECTIONS" default:"20"`
	DatabaseMaxIdle    int      `envconfig:"DATABASE_MAX_IDLE_CONNECTIONS" default:"10"`
	CORSOrigins        []string `envconfig:"CORS_ORIGINS" required:"true"`
	S3Bucket           string   `envconfig:"S3_MEDIA_BUCKET" required:"true"`
	S3CacheBucket      string   `envconfig:"S3_CACHE_BUCKET" required:"true"`
	S3Region           string   `envconfig:"S3_REGION" required:"true"`
	S3Endpoint         string   `envconfig:"S3_ENDPOINT" required:"true"`
	S3PublicEndpoint   string   `envconfig:"S3_PUBLIC_ENDPOINT" required:"true"`
	S3AccessKeyID      string   `envconfig:"S3_ACCESS_KEY_ID" required:"true"`
	S3SecretAccessKey  string   `envconfig:"S3_SECRET_ACCESS_KEY" required:"true"`
	S3ForcePathStyle   bool     `envconfig:"S3_FORCE_PATH_STYLE" required:"true"`
	CDNURL             string   `envconfig:"CDN_URL" required:"true"`
	MediaURL           string   `envconfig:"MEDIA_URL" required:"true"`
	CloudflareZoneID   string   `envconfig:"CLOUDFLARE_ZONE_ID" required:"true"`
	CloudflareAPIToken string   `envconfig:"CLOUDFLARE_API_TOKEN" required:"true"`
	CloudflareAPIURL   string   `envconfig:"CLOUDFLARE_API_URL" default:"https://api.cloudflare.com/client/v4"`
	// SpiceDB is the authorization authority for the target runtime. The
	// preshared key name matches the deployment Secret contract.
	SpiceDBEndpoint               string `envconfig:"SPICEDB_ENDPOINT" required:"true"`
	SpiceDBToken                  string `envconfig:"SPICEDB_GRPC_PRESHARED_KEY" required:"true"`
	SpiceDBAllowInsecure          bool   `envconfig:"SPICEDB_ALLOW_INSECURE" default:"false"`
	KratosPublicURL               string `envconfig:"KRATOS_URL" required:"true"`
	KratosAdminURL                string `envconfig:"KRATOS_ADMIN_URL" required:"true"`
	EditorCollabURL               string `envconfig:"EDITOR_COLLAB_URL" required:"true"`
	SESEventSNSTopicARN           string `envconfig:"SES_EVENT_SNS_TOPIC_ARN"`
	SiteOrigin                    string `envconfig:"SITE_ORIGIN" required:"true"`
	GoogleAIAPIKey                string `envconfig:"GOOGLE_AI_API_KEY" required:"true"`
	MaxMindAccountID              string `envconfig:"MAXMIND_ACCOUNT_ID" required:"true"`
	MaxMindLicenseKey             string `envconfig:"MAXMIND_LICENSE_KEY" required:"true"`
	InstanceID                    string `envconfig:"INSTANCE_ID"`
	AuthCodeLifespanSeconds       int    `envconfig:"AUTH_CODE_LIFESPAN_SECONDS" default:"900"`
	AuthCodeResendCooldownSeconds int    `envconfig:"AUTH_CODE_RESEND_COOLDOWN_SECONDS" default:"60"`
	AuthCodeAddressHourlyLimit    int    `envconfig:"AUTH_CODE_ADDRESS_HOURLY_LIMIT" default:"6"`
	AuthCodeIPTenMinuteLimit      int    `envconfig:"AUTH_CODE_IP_TEN_MINUTE_LIMIT" default:"20"`
	AuthCodeGlobalMinuteLimit     int    `envconfig:"AUTH_CODE_GLOBAL_MINUTE_LIMIT" default:"1000"`

	// Argon2id hashing parameters for form access passwords (OWASP recommended defaults).
	Argon2Memory      uint32 `envconfig:"ARGON2_MEMORY" default:"19456"`   // KiB (19 MiB)
	Argon2Iterations  uint32 `envconfig:"ARGON2_ITERATIONS" default:"2"`   // Time cost
	Argon2Parallelism uint8  `envconfig:"ARGON2_PARALLELISM" default:"1"`  // Threads
	Argon2SaltLength  uint32 `envconfig:"ARGON2_SALT_LENGTH" default:"16"` // Bytes
	Argon2KeyLength   uint32 `envconfig:"ARGON2_KEY_LENGTH" default:"32"`  // Bytes

	// Backend trust tokens and private original-media URLs use independent secrets.
	TokenSigningSecret  string `envconfig:"TOKEN_SIGNING_SECRET" required:"true"`
	MediaSigningSecret  string `envconfig:"MEDIA_SIGNING_SECRET" required:"true"`
	MediaDownloadTTLSec int    `envconfig:"MEDIA_DOWNLOAD_TTL_SEC" default:"300"`
	EditorImageMaxSize  int64  `envconfig:"EDITOR_IMAGE_MAX_SIZE_BYTES" default:"31457280"`

	HTTPReadTimeoutSec  int `envconfig:"HTTP_READ_TIMEOUT_SEC" default:"10"`
	HTTPWriteTimeoutSec int `envconfig:"HTTP_WRITE_TIMEOUT_SEC" default:"30"`
	HTTPIdleTimeoutSec  int `envconfig:"HTTP_IDLE_TIMEOUT_SEC" default:"60"`
}

func Load() (*Config, error) {
	if err := rejectLegacySecretEnvironment(); err != nil {
		return nil, err
	}
	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return nil, err
	}

	// InstanceID는 설정 안 하면 자동 생성
	if cfg.InstanceID == "" {
		cfg.InstanceID = uuid.NewString()
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return nil, fmt.Errorf("PORT must be between 1 and 65535")
	}
	if cfg.MCPPrivatePort < 1 || cfg.MCPPrivatePort > 65535 {
		return nil, fmt.Errorf("MCP_PRIVATE_PORT must be between 1 and 65535")
	}
	if cfg.MCPPrivatePort == cfg.Port {
		return nil, fmt.Errorf("MCP_PRIVATE_PORT must be distinct from PORT")
	}
	authHeaderName, err := auth.NormalizeHeaderName(cfg.AuthHeaderName)
	if err != nil {
		return nil, fmt.Errorf("AUTH_HEADER_NAME: %w", err)
	}
	internalServiceHeaderName, err := auth.NormalizeHeaderName(cfg.InternalServiceHeaderName)
	if err != nil {
		return nil, fmt.Errorf("INTERNAL_SERVICE_HEADER_NAME: %w", err)
	}
	if err := auth.ValidateHeaderNames(authHeaderName, internalServiceHeaderName); err != nil {
		return nil, err
	}
	cfg.AuthHeaderName = authHeaderName
	cfg.InternalServiceHeaderName = internalServiceHeaderName
	if cfg.DatabaseMaxOpen <= 0 {
		return nil, fmt.Errorf("DATABASE_MAX_OPEN_CONNECTIONS must be positive")
	}
	if cfg.DatabaseMaxIdle < 0 || cfg.DatabaseMaxIdle > cfg.DatabaseMaxOpen {
		return nil, fmt.Errorf("DATABASE_MAX_IDLE_CONNECTIONS must be between zero and DATABASE_MAX_OPEN_CONNECTIONS")
	}
	siteOrigin, err := normalizeSiteOrigin(cfg.SiteOrigin)
	if err != nil {
		return nil, err
	}
	cfg.SiteOrigin = siteOrigin
	editorCollabURL, err := normalizeAbsoluteOrigin("EDITOR_COLLAB_URL", cfg.EditorCollabURL)
	if err != nil {
		return nil, err
	}
	cfg.EditorCollabURL = editorCollabURL
	s3PublicEndpoint, err := normalizeAbsoluteOrigin("S3_PUBLIC_ENDPOINT", cfg.S3PublicEndpoint)
	if err != nil {
		return nil, err
	}
	cfg.S3PublicEndpoint = s3PublicEndpoint
	if strings.TrimSpace(cfg.TokenSigningSecret) == "" {
		return nil, fmt.Errorf("TOKEN_SIGNING_SECRET is required")
	}
	if cfg.TokenSigningSecret != strings.TrimSpace(cfg.TokenSigningSecret) {
		return nil, fmt.Errorf("TOKEN_SIGNING_SECRET must not contain leading or trailing whitespace")
	}
	if strings.TrimSpace(cfg.SpiceDBEndpoint) == "" {
		return nil, fmt.Errorf("SPICEDB_ENDPOINT is required")
	}
	if strings.TrimSpace(cfg.SpiceDBToken) == "" {
		return nil, fmt.Errorf("SPICEDB_GRPC_PRESHARED_KEY is required")
	}
	if cfg.SpiceDBEndpoint != strings.TrimSpace(cfg.SpiceDBEndpoint) {
		return nil, fmt.Errorf("SPICEDB_ENDPOINT must not contain leading or trailing whitespace")
	}
	if cfg.SpiceDBToken != strings.TrimSpace(cfg.SpiceDBToken) {
		return nil, fmt.Errorf("SPICEDB_GRPC_PRESHARED_KEY must not contain leading or trailing whitespace")
	}
	if strings.TrimSpace(cfg.MediaSigningSecret) == "" {
		return nil, fmt.Errorf("MEDIA_SIGNING_SECRET is required")
	}
	if cfg.MediaSigningSecret != strings.TrimSpace(cfg.MediaSigningSecret) {
		return nil, fmt.Errorf("MEDIA_SIGNING_SECRET must not contain leading or trailing whitespace")
	}
	for name, value := range map[string]int{
		"AUTH_CODE_LIFESPAN_SECONDS":        cfg.AuthCodeLifespanSeconds,
		"AUTH_CODE_RESEND_COOLDOWN_SECONDS": cfg.AuthCodeResendCooldownSeconds,
		"AUTH_CODE_ADDRESS_HOURLY_LIMIT":    cfg.AuthCodeAddressHourlyLimit,
		"AUTH_CODE_IP_TEN_MINUTE_LIMIT":     cfg.AuthCodeIPTenMinuteLimit,
		"AUTH_CODE_GLOBAL_MINUTE_LIMIT":     cfg.AuthCodeGlobalMinuteLimit,
	} {
		if value <= 0 {
			return nil, fmt.Errorf("%s must be positive", name)
		}
	}
	mediaTTLs := []struct {
		name    string
		seconds int
		max     time.Duration
	}{
		{name: "MEDIA_DOWNLOAD_TTL_SEC", seconds: cfg.MediaDownloadTTLSec, max: mediaauth.DownloadTTL},
	}
	for _, mediaTTL := range mediaTTLs {
		if mediaTTL.seconds <= 0 {
			return nil, fmt.Errorf("%s must be positive", mediaTTL.name)
		}
		if time.Duration(mediaTTL.seconds)*time.Second > mediaTTL.max {
			return nil, fmt.Errorf("%s must not exceed %s", mediaTTL.name, mediaTTL.max)
		}
	}

	return &cfg, nil
}

func normalizeSiteOrigin(value string) (string, error) {
	return normalizeAbsoluteOrigin("SITE_ORIGIN", value)
}

func normalizeAbsoluteOrigin(name string, value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery {
		return "", fmt.Errorf("%s must be an absolute origin in scheme://host[:port] form", name)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("%s scheme must be http or https", name)
	}
	return value, nil
}

func rejectLegacySecretEnvironment() error {
	if _, exists := os.LookupEnv("MEDIA_DOWNLOAD_TOKEN_SECRET"); exists {
		return fmt.Errorf("legacy MEDIA_DOWNLOAD_TOKEN_SECRET is not supported; use MEDIA_SIGNING_SECRET")
	}
	for _, name := range []string{
		"INTERNAL_SERVICE_SECRET",
		"IDENTITY_INTERNAL_SERVICE_SECRET",
		"COLLAB_API_SERVICE_SECRET",
		"OG_API_SERVICE_SECRET",
		"GATEWAY_ASSERTION_SECRET",
		"AUTH_CODE_ISSUANCE_SECRET",
		"SUBSCRIBER_TOKEN_SECRET",
		"NEWSLETTER_TOKEN_SECRET",
	} {
		if _, exists := os.LookupEnv(name); exists {
			return fmt.Errorf("legacy %s is not supported; use TOKEN_SIGNING_SECRET", name)
		}
	}
	return nil
}
