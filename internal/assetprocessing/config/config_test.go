package config

import (
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	t.Run("required environment", func(t *testing.T) {
		setValidEnvironment(t)
		t.Setenv("PORT", "not-a-number")
		if _, err := Load(); err == nil {
			t.Fatal("expected environment parsing error")
		}
	})

	t.Run("defaults and generated instance id", func(t *testing.T) {
		setValidEnvironment(t)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if len(cfg.InstanceID) != 8 {
			t.Fatalf("expected generated 8-character instance id, got %q", cfg.InstanceID)
		}
		if cfg.MaxInputBytes != DefaultMaxInputBytes || cfg.WorkerCount != 1 {
			t.Fatalf("unexpected defaults: %#v", cfg)
		}
		if cfg.GLTFTransformPath != "gltf-transform" || cfg.NodeBinaryPath != "node" || cfg.ParticleMeshScriptPath != "scripts/optimize-particle-mesh.mjs" {
			t.Fatalf("unexpected optimizer paths: %#v", cfg)
		}
	})

	t.Run("explicit instance id", func(t *testing.T) {
		setValidEnvironment(t)
		t.Setenv("INSTANCE_ID", "worker-a")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.InstanceID != "worker-a" {
			t.Fatalf("unexpected instance id: %q", cfg.InstanceID)
		}
	})

	tests := []struct {
		name    string
		key     string
		value   string
		message string
	}{
		{name: "worker count", key: "WORKER_COUNT", value: "2", message: "WORKER_COUNT"},
		{name: "job timeout", key: "JOB_TIMEOUT_MINUTES", value: "0", message: "JOB_TIMEOUT_MINUTES"},
		{name: "zero max input", key: "MAX_INPUT_BYTES", value: "0", message: "MAX_INPUT_BYTES"},
		{name: "oversized max input", key: "MAX_INPUT_BYTES", value: "52428801", message: "MAX_INPUT_BYTES"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv(test.key, test.value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("expected %s error, got %v", test.message, err)
			}
		})
	}
}

func setValidEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("S3_MEDIA_BUCKET", "media")
	t.Setenv("S3_REGION", "us-east-1")
	t.Setenv("S3_ENDPOINT", "http://minio.test")
	t.Setenv("S3_ACCESS_KEY_ID", "access")
	t.Setenv("S3_SECRET_ACCESS_KEY", "secret")
	t.Setenv("DATABASE_DSN", "postgres://geul_asset_optimizer@postgres/geul")
	t.Setenv("INSTANCE_ID", "")
}
