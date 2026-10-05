package config

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/kelseyhightower/envconfig"
)

const DefaultMaxInputBytes int64 = 50 * 1024 * 1024

type Config struct {
	Port       int    `envconfig:"PORT" default:"3030"`
	InstanceID string `envconfig:"INSTANCE_ID"`

	S3Bucket          string `envconfig:"S3_MEDIA_BUCKET" required:"true"`
	S3Region          string `envconfig:"S3_REGION" required:"true"`
	S3Endpoint        string `envconfig:"S3_ENDPOINT" required:"true"`
	S3AccessKeyID     string `envconfig:"S3_ACCESS_KEY_ID" required:"true"`
	S3SecretAccessKey string `envconfig:"S3_SECRET_ACCESS_KEY" required:"true"`
	S3ForcePathStyle  bool   `envconfig:"S3_FORCE_PATH_STYLE" default:"true"`

	DatabaseDSN string `envconfig:"DATABASE_DSN" required:"true"`

	GLTFTransformPath      string `envconfig:"GLTF_TRANSFORM_PATH" default:"gltf-transform"`
	NodeBinaryPath         string `envconfig:"NODE_BINARY_PATH" default:"node"`
	ParticleMeshScriptPath string `envconfig:"PARTICLE_MESH_SCRIPT_PATH" default:"scripts/optimize-particle-mesh.mjs"`
	TempDir                string `envconfig:"ASSET_OPTIMIZER_TEMP_DIR" default:"/tmp/asset-optimizer"`
	MaxInputBytes          int64  `envconfig:"MAX_INPUT_BYTES" default:"52428800"`

	WorkerCount    int `envconfig:"WORKER_COUNT" default:"1"`
	JobTimeoutMins int `envconfig:"JOB_TIMEOUT_MINUTES" default:"20"`

	LogLevel string `envconfig:"LOG_LEVEL" default:"info"`
}

func Load() (*Config, error) {
	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return nil, err
	}
	if cfg.InstanceID == "" {
		cfg.InstanceID = uuid.New().String()[:8]
	}
	if cfg.WorkerCount != 1 {
		return nil, fmt.Errorf("WORKER_COUNT must be 1 for the MVP worker")
	}
	if cfg.JobTimeoutMins <= 0 {
		return nil, fmt.Errorf("JOB_TIMEOUT_MINUTES must be positive")
	}
	if cfg.MaxInputBytes <= 0 || cfg.MaxInputBytes > DefaultMaxInputBytes {
		return nil, fmt.Errorf("MAX_INPUT_BYTES must be in range 1..%d", DefaultMaxInputBytes)
	}
	return &cfg, nil
}
