package testutil

const AppIntegrationPostgresImage = "registry.dsub.io/echovisionlab/geul-postgres@sha256:41a2c6fb9e026ed327463e7662c92c5cc27e918bdaae6fa3447f45335d74494a"

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
	KratosAdminURL       string `json:"kratos_admin_url"`
	KratosPublicURL      string `json:"kratos_public_url"`
	OathkeeperAdminURL   string `json:"oathkeeper_admin_url"`
	OathkeeperProxyURL   string `json:"oathkeeper_proxy_url"`
	SpiceDBEndpoint      string `json:"spicedb_endpoint"`
	SpiceDBToken         string `json:"spicedb_token"`
	S3Region             string `json:"s3_region"`
	S3Endpoint           string `json:"s3_endpoint"`
	S3AccessKeyID        string `json:"s3_access_key_id"`
	S3SecretAccessKey    string `json:"s3_secret_access_key"`
	S3MediaBucket        string `json:"s3_media_bucket"`
	S3CacheBucket        string `json:"s3_cache_bucket"`
	S3ForcePathStyle     bool   `json:"s3_force_path_style"`
	CDNImage             string `json:"cdn_image,omitempty"`
	HookControlURL       string `json:"hook_control_url"`
	HookControlToken     string `json:"hook_control_token"`
}
