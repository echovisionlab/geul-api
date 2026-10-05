package jobs

import (
	"testing"
	"time"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

func TestDefaultQueueConfigs(t *testing.T) {
	t.Parallel()
	assertQueueConfig(t, "audio", DefaultAudioQueueConfig(), eventpkg.QueueTranscoderAudio, 3, 30*time.Minute)
	assertQueueConfig(t, "video", DefaultVideoQueueConfig(), eventpkg.QueueTranscoderVideo, 2, time.Hour)
	assertQueueConfig(t, "waveform", DefaultWaveformQueueConfig(), eventpkg.QueueWaveformGenerate, 2, 20*time.Minute)
}

func assertQueueConfig(t *testing.T, label string, config QueueConfig, name string, workers int, timeout time.Duration) {
	t.Helper()
	if config.Name != name {
		t.Fatalf("unexpected %s queue name: %#v", label, config)
	}
	if config.Workers != workers {
		t.Fatalf("unexpected %s worker count: %#v", label, config)
	}
	if config.Timeout != timeout {
		t.Fatalf("unexpected %s timeout: %#v", label, config)
	}
	if config.RetryLimit != 3 {
		t.Fatalf("unexpected %s retry policy: %#v", label, config)
	}
}
