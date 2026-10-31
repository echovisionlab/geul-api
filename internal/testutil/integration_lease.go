package testutil

import (
	"os"
	"strings"
)

const AppIntegrationPostgresImage = "registry.rawsector.com/echovisionlab/geul-postgres@sha256:73020bae7decd43b5a00291488740ef307dbd12f405674f0526098df71257138"

// The upstream minio/minio images are no longer pullable from Docker Hub;
// Chainguard publishes the same server, pinned here by its multi-arch index.
const AppIntegrationMinIOImage = "cgr.dev/chainguard/minio@sha256:3b6e40c30ecd5cea627e382ff0b82ca841f58c52294578787043ee80c8ef7c82"

const AppIntegrationMinIOImageEnv = "GEUL_INTEGRATION_MINIO_IMAGE"

// MinIOIntegrationImage returns the configured MinIO image for integration
// tests, defaulting to the pinned upstream release used by local test runs.
func MinIOIntegrationImage() string {
	if image := strings.TrimSpace(os.Getenv(AppIntegrationMinIOImageEnv)); image != "" {
		return image
	}
	return AppIntegrationMinIOImage
}

const AppIntegrationLeaseVersion = 1

type AppIntegrationLeaseDescriptor struct {
	Version              int                         `json:"version"`
	PostgresAdminDSN     string                      `json:"postgres_admin_dsn"`
	PostgresContainerID  string                      `json:"postgres_container_id"`
	PostgresTemplateName string                      `json:"postgres_template_name"`
	Backend              *AppIntegrationBackendLease `json:"backend,omitempty"`
}

type AppIntegrationBackendLease struct {
	PostgresDSN          string `json:"postgres_dsn"`
	PostgresDatabaseName string `json:"postgres_database_name"`
	// PostgresIsolationTemplate is cloned once per test; its kratos schema is a
	// postgres_fdw view of PostgresDatabaseName.
	PostgresIsolationTemplate string `json:"postgres_isolation_template"`
	// PostgresLocalKratosTemplate carries its own copy of the kratos schema
	// (structure and network row) for tests that need READ COMMITTED locking
	// on kratos rows.
	PostgresLocalKratosTemplate string `json:"postgres_local_kratos_template"`
	KratosAdminURL              string `json:"kratos_admin_url"`
	KratosPublicURL             string `json:"kratos_public_url"`
	OathkeeperAdminURL          string `json:"oathkeeper_admin_url"`
	OathkeeperProxyURL          string `json:"oathkeeper_proxy_url"`
	SpiceDBEndpoint             string `json:"spicedb_endpoint"`
	SpiceDBToken                string `json:"spicedb_token"`
	S3Region                    string `json:"s3_region"`
	S3Endpoint                  string `json:"s3_endpoint"`
	S3AccessKeyID               string `json:"s3_access_key_id"`
	S3SecretAccessKey           string `json:"s3_secret_access_key"`
	S3MediaBucket               string `json:"s3_media_bucket"`
	S3CacheBucket               string `json:"s3_cache_bucket"`
	S3ForcePathStyle            bool   `json:"s3_force_path_style"`
	HookControlURL              string `json:"hook_control_url"`
	HookControlToken            string `json:"hook_control_token"`
}
