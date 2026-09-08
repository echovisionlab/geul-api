// Package jobs defines transcoder-specific configuration.
// PGMQ queue, PostgreSQL signal, and content type names are in github.com/echovisionlab/geul-event-contracts/go/event
// Event message types are in github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1 (generated from proto)
package jobs

import (
	"time"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// QueueConfig defines per-queue settings (transcoder-specific)
type QueueConfig struct {
	ServiceName sharedtelemetry.ServiceName
	Name        string
	MessageType string
	Workers     int
	Timeout     time.Duration
	RetryLimit  int
}

// DefaultAudioQueueConfig returns default config for audio queue
// Uses direct PGMQ work queue consumption.
func DefaultAudioQueueConfig() QueueConfig {
	return QueueConfig{
		ServiceName: sharedtelemetry.ServiceTranscoder,
		Name:        eventpkg.QueueTranscoderAudio,
		MessageType: "api.manage.v1.TranscodeAudioEvent",
		Workers:     3,
		Timeout:     30 * time.Minute,
		RetryLimit:  3,
	}
}

// DefaultVideoQueueConfig returns default config for video queue
// Uses direct PGMQ work queue consumption.
func DefaultVideoQueueConfig() QueueConfig {
	return QueueConfig{
		ServiceName: sharedtelemetry.ServiceTranscoder,
		Name:        eventpkg.QueueTranscoderVideo,
		MessageType: "api.manage.v1.TranscodeVideoEvent",
		Workers:     2,
		Timeout:     60 * time.Minute,
		RetryLimit:  3,
	}
}

// DefaultWaveformQueueConfig returns default config for waveform generation queue.
func DefaultWaveformQueueConfig() QueueConfig {
	return QueueConfig{
		ServiceName: sharedtelemetry.ServiceWaveformProcessor,
		Name:        eventpkg.QueueWaveformGenerate,
		MessageType: "api.manage.v1.WaveformGenerateEvent",
		Workers:     2,
		Timeout:     20 * time.Minute,
		RetryLimit:  3,
	}
}
