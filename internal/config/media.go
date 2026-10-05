package config

import (
	"encoding/hex"
	"fmt"
)

// MediaConfig controls API-owned delivery and durable background work. Queue
// names and payloads remain those of the existing event contracts.
type MediaConfig struct {
	OGNodePath            string `envconfig:"OG_NODE_BINARY_PATH"`
	OGTelemetryAttributes string `envconfig:"OG_OTEL_RESOURCE_ATTRIBUTES"`
	WaveformTempDir       string `envconfig:"WAVEFORM_TEMP_DIR" default:"/tmp/geul-media/waveform"`
	WaveformFFmpegPath    string `envconfig:"WAVEFORM_FFMPEG_PATH"`
	OGWorkers             int    `envconfig:"OG_GENERATE_WORKERS" default:"12"`
	OGScriptPath          string `envconfig:"OG_WORKER_SCRIPT" default:"/app/media/og/dist/index.js"`
	OGPort                int    `envconfig:"OG_PORT" default:"3010"`
	OGShutdownTimeoutMS   int    `envconfig:"OG_SHUTDOWN_TIMEOUT_MS" default:"120000"`
	ImgproxyURL           string `envconfig:"CDN_IMGPROXY_URL" default:"http://127.0.0.1:8080"`
	ImgproxyKey           string `envconfig:"IMGPROXY_KEY" required:"true"`
	ImgproxySalt          string `envconfig:"IMGPROXY_SALT" required:"true"`

	DeliveryPort           int    `envconfig:"MEDIA_DELIVERY_PORT" default:"8002"`
	AudioWorkers           int    `envconfig:"MEDIA_AUDIO_WORKERS" default:"3"`
	VideoWorkers           int    `envconfig:"MEDIA_VIDEO_WORKERS" default:"3"`
	WaveformWorkers        int    `envconfig:"MEDIA_WAVEFORM_WORKERS" default:"2"`
	JobTimeoutMinutes      int    `envconfig:"MEDIA_JOB_TIMEOUT_MINUTES" default:"30"`
	MeshTimeoutMinutes     int    `envconfig:"MEDIA_MESH_TIMEOUT_MINUTES" default:"20"`
	MeshMaxInputBytes      int64  `envconfig:"MEDIA_MESH_MAX_INPUT_BYTES" default:"52428800"`
	FFmpegPath             string `envconfig:"FFMPEG_PATH" default:"ffmpeg"`
	FFprobePath            string `envconfig:"FFPROBE_PATH" default:"ffprobe"`
	FFmpegTempDir          string `envconfig:"FFMPEG_TEMP_DIR" default:"/tmp/geul-media/transcode"`
	MeshTempDir            string `envconfig:"ASSET_OPTIMIZER_TEMP_DIR" default:"/tmp/geul-media/mesh"`
	AudioHLSBitrate        string `envconfig:"AUDIO_HLS_BITRATE" default:"128k"`
	NodePath               string `envconfig:"NODE_BINARY_PATH" default:"node"`
	GLTFTransformPath      string `envconfig:"GLTF_TRANSFORM_PATH" default:"/app/media/asset-optimizer/node_modules/.bin/gltf-transform"`
	ParticleMeshScriptPath string `envconfig:"PARTICLE_MESH_SCRIPT_PATH" default:"/app/media/asset-optimizer/scripts/optimize-particle-mesh.mjs"`
	FontS3Prefix           string `envconfig:"CDN_FONT_S3_PREFIX" default:"fonts/"`
	FontUpstreamURL        string `envconfig:"CDN_FONT_UPSTREAM_URL" default:"https://fonts.gstatic.com"`
	FontCSSUpstream        string `envconfig:"CDN_FONT_CSS_UPSTREAM" default:"https://fonts.googleapis.com"`
	FontCacheMaxAge        int    `envconfig:"CDN_FONT_CACHE_MAX_AGE" default:"31536000"`
}

func (m MediaConfig) validate(apiPort, privatePort int) error {
	if m.DeliveryPort < 1 || m.DeliveryPort > 65535 || m.DeliveryPort == apiPort || m.DeliveryPort == privatePort {
		return fmt.Errorf("MEDIA_DELIVERY_PORT must be a distinct port in 1..65535")
	}
	if m.OGPort < 1 || m.OGPort > 65535 || m.OGPort == m.DeliveryPort || m.OGPort == apiPort || m.OGPort == privatePort {
		return fmt.Errorf("OG_PORT must be a distinct port in 1..65535")
	}
	if m.OGShutdownTimeoutMS < 1 {
		return fmt.Errorf("OG_SHUTDOWN_TIMEOUT_MS must be positive")
	}
	for name, value := range map[string]string{"IMGPROXY_KEY": m.ImgproxyKey, "IMGPROXY_SALT": m.ImgproxySalt} {
		if b, err := hex.DecodeString(value); err != nil || len(b) == 0 {
			return fmt.Errorf("%s must be non-empty, even-length hexadecimal", name)
		}
	}
	if m.OGWorkers < 1 || m.AudioWorkers < 1 || m.VideoWorkers < 1 || m.WaveformWorkers < 1 {
		return fmt.Errorf("media worker counts must be positive")
	}
	if m.JobTimeoutMinutes < 1 || m.MeshTimeoutMinutes < 1 {
		return fmt.Errorf("media job timeouts must be positive")
	}
	if m.MeshMaxInputBytes < 1 || m.MeshMaxInputBytes > 50*1024*1024 {
		return fmt.Errorf("MEDIA_MESH_MAX_INPUT_BYTES must be in 1..52428800")
	}
	return nil
}
