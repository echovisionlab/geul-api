package config

import (
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfigWithDefaults(t *testing.T) {
	setRequiredConfigEnv(t)
	t.Setenv("INSTANCE_ID", "")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, 8080, cfg.Port)
	assert.Equal(t, 8001, cfg.MCPPrivatePort)
	assert.Equal(t, "X-Authenticated-Context-B64", cfg.AuthHeaderName)
	assert.Equal(t, "X-Internal-Service", cfg.InternalServiceHeaderName)
	assert.Equal(t, "postgres://example", cfg.DatabaseDSN)
	assert.Equal(t, 20, cfg.DatabaseMaxOpen)
	assert.Equal(t, 10, cfg.DatabaseMaxIdle)
	assert.Equal(t, []string{"https://preview.studio.example.com", "https://studio.example.com"}, cfg.CORSOrigins)
	assert.Equal(t, "zone-id", cfg.CloudflareZoneID)
	assert.Equal(t, "cloudflare-token", cfg.CloudflareAPIToken)
	assert.Equal(t, "https://cdn.example.com", cfg.CDNURL)
	assert.Equal(t, "https://media.example.com", cfg.MediaURL)
	assert.Equal(t, "https://studio.example.com", cfg.SiteOrigin)
	assert.Equal(t, "http://collab:3003", cfg.EditorCollabURL)
	assert.Equal(t, 60, cfg.AuthCodeResendCooldownSeconds)
	assert.Equal(t, "https://api.cloudflare.com/client/v4", cfg.CloudflareAPIURL)
	assert.Equal(t, "spicedb:50051", cfg.SpiceDBEndpoint)
	assert.Equal(t, "spicedb-test-token", cfg.SpiceDBToken)
	assert.False(t, cfg.SpiceDBAllowInsecure)
	assert.Equal(t, uint32(19456), cfg.Argon2Memory)
	assert.Equal(t, uint32(2), cfg.Argon2Iterations)
	assert.Equal(t, uint8(1), cfg.Argon2Parallelism)
	assert.Equal(t, uint32(16), cfg.Argon2SaltLength)
	assert.Equal(t, uint32(32), cfg.Argon2KeyLength)
	assert.Equal(t, "token-signing-secret-with-at-least-32-bytes", cfg.TokenSigningSecret)
	assert.Equal(t, "media-signing-secret-with-at-least-32-bytes", cfg.MediaSigningSecret)
	assert.Equal(t, 300, cfg.MediaDownloadTTLSec)
	assert.Equal(t, int64(31457280), cfg.EditorImageMaxSize)
	assert.Equal(t, 10, cfg.HTTPReadTimeoutSec)
	assert.Equal(t, 30, cfg.HTTPWriteTimeoutSec)
	assert.Equal(t, 60, cfg.HTTPIdleTimeoutSec)
	assert.NotEmpty(t, cfg.InstanceID)
	_, err = uuid.Parse(cfg.InstanceID)
	require.NoError(t, err)
}

func TestLoadConfigRespectsExplicitOverrides(t *testing.T) {
	setRequiredConfigEnv(t)
	t.Setenv("INSTANCE_ID", "backend-dev-1")
	t.Setenv("MCP_PRIVATE_PORT", "18001")
	t.Setenv("AUTH_HEADER_NAME", "x-geul-authenticated-context-b64")
	t.Setenv("INTERNAL_SERVICE_HEADER_NAME", "x-geul-internal-service")
	t.Setenv("TOKEN_SIGNING_SECRET", "token-secret")
	t.Setenv("MEDIA_SIGNING_SECRET", "media-secret")
	t.Setenv("ARGON2_MEMORY", "128")
	t.Setenv("ARGON2_ITERATIONS", "3")
	t.Setenv("ARGON2_PARALLELISM", "2")
	t.Setenv("ARGON2_SALT_LENGTH", "12")
	t.Setenv("ARGON2_KEY_LENGTH", "24")
	t.Setenv("MEDIA_DOWNLOAD_TTL_SEC", "600")
	t.Setenv("EDITOR_IMAGE_MAX_SIZE_BYTES", "1024")
	t.Setenv("HTTP_READ_TIMEOUT_SEC", "11")
	t.Setenv("HTTP_WRITE_TIMEOUT_SEC", "31")
	t.Setenv("HTTP_IDLE_TIMEOUT_SEC", "61")
	t.Setenv("DATABASE_MAX_OPEN_CONNECTIONS", "24")
	t.Setenv("DATABASE_MAX_IDLE_CONNECTIONS", "12")
	t.Setenv("AUTH_CODE_RESEND_COOLDOWN_SECONDS", "75")
	t.Setenv("SES_EVENT_SNS_TOPIC_ARN", "arn:aws:sns:ap-northeast-2:123456789012:geul-ses-events")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "backend-dev-1", cfg.InstanceID)
	assert.Equal(t, 18001, cfg.MCPPrivatePort)
	assert.Equal(t, "X-Geul-Authenticated-Context-B64", cfg.AuthHeaderName)
	assert.Equal(t, "X-Geul-Internal-Service", cfg.InternalServiceHeaderName)
	assert.Equal(t, "token-secret", cfg.TokenSigningSecret)
	assert.Equal(t, "media-secret", cfg.MediaSigningSecret)
	assert.Equal(t, uint32(128), cfg.Argon2Memory)
	assert.Equal(t, uint32(3), cfg.Argon2Iterations)
	assert.Equal(t, uint8(2), cfg.Argon2Parallelism)
	assert.Equal(t, uint32(12), cfg.Argon2SaltLength)
	assert.Equal(t, uint32(24), cfg.Argon2KeyLength)
	assert.Equal(t, 600, cfg.MediaDownloadTTLSec)
	assert.Equal(t, int64(1024), cfg.EditorImageMaxSize)
	assert.Equal(t, 11, cfg.HTTPReadTimeoutSec)
	assert.Equal(t, 31, cfg.HTTPWriteTimeoutSec)
	assert.Equal(t, 61, cfg.HTTPIdleTimeoutSec)
	assert.Equal(t, 24, cfg.DatabaseMaxOpen)
	assert.Equal(t, 12, cfg.DatabaseMaxIdle)
	assert.Equal(t, 75, cfg.AuthCodeResendCooldownSeconds)
	assert.Equal(t, "arn:aws:sns:ap-northeast-2:123456789012:geul-ses-events", cfg.SESEventSNSTopicARN)
}

func TestLoadConfigRejectsInvalidOrSharedMCPPrivatePort(t *testing.T) {
	for _, testCase := range []struct {
		name string
		port string
	}{
		{name: "zero", port: "0"},
		{name: "above TCP range", port: "65536"},
		{name: "same as public API", port: "8080"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			setRequiredConfigEnv(t)
			t.Setenv("MCP_PRIVATE_PORT", testCase.port)

			cfg, err := Load()

			require.Error(t, err)
			assert.Nil(t, cfg)
			assert.Contains(t, err.Error(), "MCP_PRIVATE_PORT")
		})
	}
}

func TestLoadConfigRejectsInvalidDatabasePoolLimits(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		maxOpen string
		maxIdle string
	}{
		{name: "zero max open", maxOpen: "0", maxIdle: "0"},
		{name: "negative max idle", maxOpen: "20", maxIdle: "-1"},
		{name: "max idle exceeds max open", maxOpen: "10", maxIdle: "11"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			setRequiredConfigEnv(t)
			t.Setenv("DATABASE_MAX_OPEN_CONNECTIONS", testCase.maxOpen)
			t.Setenv("DATABASE_MAX_IDLE_CONNECTIONS", testCase.maxIdle)

			cfg, err := Load()

			require.Error(t, err)
			assert.Nil(t, cfg)
			assert.Contains(t, err.Error(), "DATABASE_MAX_")
		})
	}
}

func TestLoadConfigRejectsMediaTTLOutsideSigningContract(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "non-positive download", key: "MEDIA_DOWNLOAD_TTL_SEC", value: "0"},
		{name: "excessive download", key: "MEDIA_DOWNLOAD_TTL_SEC", value: "901"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			setRequiredConfigEnv(t)
			t.Setenv(testCase.key, testCase.value)

			cfg, err := Load()

			require.Error(t, err)
			assert.Nil(t, cfg)
			assert.Contains(t, err.Error(), testCase.key)
		})
	}
}

func TestLoadConfigFailsWhenRequiredEnvMissing(t *testing.T) {
	setRequiredConfigEnv(t)
	require.NoError(t, os.Unsetenv("DATABASE_DSN"))

	cfg, err := Load()
	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "DATABASE_DSN")
}

func TestLoadConfigRequiresAndValidatesTrustBoundaryHeaderNames(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		authHeader    string
		internal      string
		wantSubstring string
	}{
		{name: "missing auth header", authHeader: "", internal: "X-Internal-Service", wantSubstring: "AUTH_HEADER_NAME"},
		{name: "missing internal header", authHeader: "X-Authenticated-Context-B64", internal: "", wantSubstring: "INTERNAL_SERVICE_HEADER_NAME"},
		{name: "whitespace auth header", authHeader: " X-Auth ", internal: "X-Internal-Service", wantSubstring: "AUTH_HEADER_NAME"},
		{name: "invalid auth header", authHeader: "X Auth", internal: "X-Internal-Service", wantSubstring: "AUTH_HEADER_NAME"},
		{name: "reserved auth header", authHeader: "Authorization", internal: "X-Internal-Service", wantSubstring: "AUTH_HEADER_NAME"},
		{name: "same headers", authHeader: "X-Trust", internal: "x-trust", wantSubstring: "distinct"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			setRequiredConfigEnv(t)
			t.Setenv("AUTH_HEADER_NAME", testCase.authHeader)
			t.Setenv("INTERNAL_SERVICE_HEADER_NAME", testCase.internal)

			cfg, err := Load()

			require.Error(t, err)
			assert.Nil(t, cfg)
			assert.Contains(t, err.Error(), testCase.wantSubstring)
		})
	}
}

func TestLoadConfigRequiresCanonicalTokenSigningSecret(t *testing.T) {
	for _, value := range []string{"", "   "} {
		t.Run("value="+value, func(t *testing.T) {
			setRequiredConfigEnv(t)
			t.Setenv("TOKEN_SIGNING_SECRET", value)

			cfg, err := Load()

			require.Error(t, err)
			assert.Nil(t, cfg)
			assert.Contains(t, err.Error(), "TOKEN_SIGNING_SECRET")
			assert.NotContains(t, err.Error(), "token-signing-secret-with-at-least-32-bytes")
		})
	}
}

func TestLoadConfigRejectsInvalidSpiceDBCredentials(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "blank endpoint", key: "SPICEDB_ENDPOINT", value: "   "},
		{name: "spaced endpoint", key: "SPICEDB_ENDPOINT", value: " spicedb:50051"},
		{name: "blank preshared key", key: "SPICEDB_GRPC_PRESHARED_KEY", value: "   "},
		{name: "spaced preshared key", key: "SPICEDB_GRPC_PRESHARED_KEY", value: " token"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			setRequiredConfigEnv(t)
			t.Setenv(testCase.key, testCase.value)

			cfg, err := Load()

			require.Error(t, err)
			assert.Nil(t, cfg)
		})
	}
}

func TestLoadConfigRejectsOuterWhitespaceInCanonicalTokenSigningSecret(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		value string
	}{
		{name: "leading", value: " token-signing-secret-with-at-least-32-bytes"},
		{name: "trailing", value: "token-signing-secret-with-at-least-32-bytes "},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			setRequiredConfigEnv(t)
			t.Setenv("TOKEN_SIGNING_SECRET", testCase.value)

			cfg, err := Load()

			require.Error(t, err)
			assert.Nil(t, cfg)
			assert.Equal(t, "TOKEN_SIGNING_SECRET must not contain leading or trailing whitespace", err.Error())
			assert.NotContains(t, err.Error(), testCase.value)
		})
	}
}

func TestLoadConfigRequiresSiteOrigin(t *testing.T) {
	setRequiredConfigEnv(t)
	require.NoError(t, os.Unsetenv("SITE_ORIGIN"))

	cfg, err := Load()
	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "SITE_ORIGIN")
}

func TestLoadConfigRejectsSiteOriginWithPath(t *testing.T) {
	setRequiredConfigEnv(t)
	t.Setenv("SITE_ORIGIN", "https://studio.example.com/app")

	cfg, err := Load()
	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "scheme://host[:port]")
}

func TestLoadConfigRejectsS3PublicEndpointWithPath(t *testing.T) {
	setRequiredConfigEnv(t)
	t.Setenv("S3_PUBLIC_ENDPOINT", "https://s3.example.com/minio")

	cfg, err := Load()
	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "S3_PUBLIC_ENDPOINT")
}

func TestLoadConfigRejectsEditorCollabURLWithPath(t *testing.T) {
	setRequiredConfigEnv(t)
	t.Setenv("EDITOR_COLLAB_URL", "http://collab:3003/internal")

	cfg, err := Load()
	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "EDITOR_COLLAB_URL")
}

func TestLoadConfigRejectsNonPositiveAuthCodeResendCooldown(t *testing.T) {
	setRequiredConfigEnv(t)
	t.Setenv("AUTH_CODE_RESEND_COOLDOWN_SECONDS", "0")

	cfg, err := Load()
	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "AUTH_CODE_RESEND_COOLDOWN_SECONDS")
}

func setRequiredConfigEnv(t *testing.T) {
	t.Helper()

	values := map[string]string{
		"PORT":                         "8080",
		"AUTH_HEADER_NAME":             "X-Authenticated-Context-B64",
		"INTERNAL_SERVICE_HEADER_NAME": "X-Internal-Service",
		"DATABASE_DSN":                 "postgres://example",
		"CORS_ORIGINS":                 "https://preview.studio.example.com,https://studio.example.com",
		"S3_MEDIA_BUCKET":              "media",
		"S3_CACHE_BUCKET":              "cache",
		"S3_REGION":                    "ap-northeast-2",
		"S3_ENDPOINT":                  "https://s3.example.com",
		"S3_PUBLIC_ENDPOINT":           "https://s3-public.example.com",
		"S3_ACCESS_KEY_ID":             "access-key",
		"S3_SECRET_ACCESS_KEY":         "secret-key",
		"S3_FORCE_PATH_STYLE":          "true",
		"CDN_URL":                      "https://cdn.example.com",
		"MEDIA_URL":                    "https://media.example.com",
		"CLOUDFLARE_ZONE_ID":           "zone-id",
		"CLOUDFLARE_API_TOKEN":         "cloudflare-token",
		"SPICEDB_ENDPOINT":             "spicedb:50051",
		"SPICEDB_GRPC_PRESHARED_KEY":   "spicedb-test-token",
		"KRATOS_URL":                   "https://identity.example.com",
		"KRATOS_ADMIN_URL":             "http://kratos:4434",
		"EDITOR_COLLAB_URL":            "http://collab:3003",
		"SITE_ORIGIN":                  "https://studio.example.com",
		"GOOGLE_AI_API_KEY":            "google-key",
		"MAXMIND_ACCOUNT_ID":           "maxmind-account",
		"MAXMIND_LICENSE_KEY":          "maxmind-license",
		"TOKEN_SIGNING_SECRET":         "token-signing-secret-with-at-least-32-bytes",
		"MEDIA_SIGNING_SECRET":         "media-signing-secret-with-at-least-32-bytes",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
}
