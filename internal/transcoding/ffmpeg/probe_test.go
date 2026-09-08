package ffmpeg

import "testing"

func TestParseProbeOutputDetectsVideoOnlyMP4(t *testing.T) {
	t.Parallel()

	probe, err := parseProbeOutput([]byte(`{
		"format": {
			"duration": "60.033333",
			"bit_rate": "42505000",
			"format_name": "mov,mp4,m4a,3gp,3g2,mj2"
		},
		"streams": [
			{
				"codec_name": "h264",
				"width": 2688,
				"height": 1008
			}
		]
	}`))
	if err != nil {
		t.Fatalf("parse probe output: %v", err)
	}

	if probe.HasAudio {
		t.Fatalf("video-only mp4 must not be detected as audio: %#v", probe)
	}
	if !probe.HasVideo || probe.Width != 2688 || probe.Height != 1008 {
		t.Fatalf("expected video stream metadata, got %#v", probe)
	}
}

func TestParseProbeOutputDetectsAudioInSecondStream(t *testing.T) {
	t.Parallel()

	probe, err := parseProbeOutput([]byte(`{
		"format": {
			"duration": "5.000000",
			"format_name": "mov,mp4,m4a,3gp,3g2,mj2"
		},
		"streams": [
			{
				"codec_name": "h264",
				"width": 640,
				"height": 360
			},
			{
				"codec_name": "aac",
				"sample_rate": "44100",
				"channels": 2
			}
		]
	}`))
	if err != nil {
		t.Fatalf("parse probe output: %v", err)
	}

	if !probe.HasAudio || !probe.HasVideo {
		t.Fatalf("expected both audio and video streams, got %#v", probe)
	}
	if probe.SampleRate != 44100 || probe.Channels != 2 {
		t.Fatalf("expected audio stream metadata from second stream, got %#v", probe)
	}
	if probe.Width != 640 || probe.Height != 360 {
		t.Fatalf("expected video stream metadata from first stream, got %#v", probe)
	}
}
