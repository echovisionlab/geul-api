package application

import (
	"context"

	"gorm.io/gorm"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Publisher is the Transcode application-owned output boundary.
type Publisher interface {
	PublishMediaProcessingLifecycle(context.Context, *managev1.MediaProcessingLifecycleEvent) error
	PublishWaveformGenerate(context.Context, *managev1.WaveformGenerateEvent) error
}

// JobTracker owns queryable Transcode job state while PGMQ owns delivery.
type JobTracker interface {
	HandleTranscodeProgress(context.Context, *managev1.TranscodeProgressEvent) error
	HandleTranscodeComplete(context.Context, *managev1.TranscodeCompleteEvent) error
	MarkCancelled(context.Context, string, managev1.TranscodeCancelReason) error
}

// Application applies Transcode and Waveform results to their authoritative
// FileMedia state. Transport decoding belongs to the runtime adapter.
type Application struct {
	db            *gorm.DB
	publisher     Publisher
	transcodeJobs JobTracker
}

func New(db *gorm.DB, publisher Publisher, jobs JobTracker) *Application {
	if db == nil {
		panic("transcode application: db is required")
	}
	return &Application{db: db, publisher: publisher, transcodeJobs: jobs}
}

// Handlers is kept internal to the package so the extracted implementation can
// retain cohesive receiver names without exposing a second public abstraction.
type Handlers = Application
