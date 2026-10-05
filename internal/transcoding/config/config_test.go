package config

import "testing"

func TestLoadReadsRequiredEnvironmentAndGeneratesInstanceID(t *testing.T) {
	t.Setenv("PORT", "8080")
	t.Setenv("S3_MEDIA_BUCKET", "media")
	t.Setenv("S3_REGION", "us-east-1")
	t.Setenv("S3_ENDPOINT", "http://minio:9000")
	t.Setenv("S3_ACCESS_KEY_ID", "access")
	t.Setenv("S3_SECRET_ACCESS_KEY", "secret")
	t.Setenv("S3_FORCE_PATH_STYLE", "true")
	t.Setenv("DATABASE_DSN", "postgres://transcoder@postgres/geul")
	t.Setenv("FFMPEG_PATH", "ffmpeg")
	t.Setenv("FFPROBE_PATH", "ffprobe")
	t.Setenv("FFMPEG_TEMP_DIR", "/tmp/transcoder")
	t.Setenv("WORKER_COUNT", "3")
	t.Setenv("JOB_TIMEOUT_MINUTES", "10")
	t.Setenv("AUDIO_HLS_BITRATE", "128k")
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.InstanceID == "" || len(cfg.InstanceID) != 8 {
		t.Fatalf("expected generated 8-char instance id, got %q", cfg.InstanceID)
	}
	if cfg.Port != 8080 || !cfg.S3ForcePathStyle {
		t.Fatalf("unexpected loaded config: %#v", cfg)
	}
}

func TestLoadPreservesConfiguredInstanceIDAndReturnsErrors(t *testing.T) {
	t.Setenv("PORT", "8080")
	t.Setenv("INSTANCE_ID", "worker-a")
	t.Setenv("S3_MEDIA_BUCKET", "media")
	t.Setenv("S3_REGION", "us-east-1")
	t.Setenv("S3_ENDPOINT", "http://minio:9000")
	t.Setenv("S3_ACCESS_KEY_ID", "access")
	t.Setenv("S3_SECRET_ACCESS_KEY", "secret")
	t.Setenv("S3_FORCE_PATH_STYLE", "true")
	t.Setenv("DATABASE_DSN", "postgres://transcoder@postgres/geul")
	t.Setenv("FFMPEG_PATH", "ffmpeg")
	t.Setenv("FFPROBE_PATH", "ffprobe")
	t.Setenv("FFMPEG_TEMP_DIR", "/tmp/transcoder")
	t.Setenv("WORKER_COUNT", "3")
	t.Setenv("JOB_TIMEOUT_MINUTES", "10")
	t.Setenv("AUDIO_HLS_BITRATE", "128k")
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.InstanceID != "worker-a" {
		t.Fatalf("InstanceID = %q", cfg.InstanceID)
	}

	t.Setenv("PORT", "not-int")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid port error")
	}
}
