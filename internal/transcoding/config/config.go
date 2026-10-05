// Package config loads process configuration from environment variables.
package config

import (
	"github.com/google/uuid"
	"github.com/kelseyhightower/envconfig"
)

// Config contains the runtime settings shared by both services.
type Config struct {
	Port       int    `envconfig:"PORT" required:"true"`
	InstanceID string `envconfig:"INSTANCE_ID"`

	// S3/MinIO
	S3Bucket          string `envconfig:"S3_MEDIA_BUCKET" required:"true"`
	S3Region          string `envconfig:"S3_REGION" required:"true"`
	S3Endpoint        string `envconfig:"S3_ENDPOINT" required:"true"`
	S3AccessKeyID     string `envconfig:"S3_ACCESS_KEY_ID" required:"true"`
	S3SecretAccessKey string `envconfig:"S3_SECRET_ACCESS_KEY" required:"true"`
	S3ForcePathStyle  bool   `envconfig:"S3_FORCE_PATH_STYLE" required:"true"`

	// PGMQ and PostgreSQL signals
	DatabaseDSN string `envconfig:"DATABASE_DSN" required:"true"`

	// FFmpeg
	FFmpegPath     string `envconfig:"FFMPEG_PATH" required:"true"`
	FFprobePath    string `envconfig:"FFPROBE_PATH" required:"true"`
	FFmpegTempDir  string `envconfig:"FFMPEG_TEMP_DIR" required:"true"`
	WorkerCount    int    `envconfig:"WORKER_COUNT" required:"true"`
	JobTimeoutMins int    `envconfig:"JOB_TIMEOUT_MINUTES" required:"true"`

	// Audio defaults
	AudioHLSBitrate string `envconfig:"AUDIO_HLS_BITRATE" required:"true"`

	// Worker settings
	WaveformWorkerCount int `envconfig:"WAVEFORM_WORKER_COUNT" default:"0"`

	// Logging
	LogLevel string `envconfig:"LOG_LEVEL" required:"true"`
}

// Load reads and validates configuration from the process environment.
func Load() (*Config, error) {
	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return nil, err
	}

	if cfg.InstanceID == "" {
		cfg.InstanceID = uuid.New().String()[:8]
	}

	return &cfg, nil
}
