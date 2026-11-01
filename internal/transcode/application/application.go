package application

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/mutation"
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
	units         mutation.UnitOfWork[Store]
	publisher     Publisher
	transcodeJobs JobTracker
}

func New(units mutation.UnitOfWork[Store], publisher Publisher, jobs JobTracker) *Application {
	if units == nil {
		panic("transcode application: units are required")
	}
	return &Application{units: units, publisher: publisher, transcodeJobs: jobs}
}

// transaction runs work in one transaction of the application's store.
func (h *Handlers) transaction(ctx context.Context, work func(Store) error) error {
	return h.units.Transaction(ctx, false, func(tx mutation.Tx[Store]) error { return work(tx.Store()) })
}

// read runs read in one read snapshot of the application's store.
func (h *Handlers) read(ctx context.Context, read func(Store) error) error {
	return h.units.Read(ctx, read)
}

// Handlers is kept internal to the package so the extracted implementation can
// retain cohesive receiver names without exposing a second public abstraction.
type Handlers = Application
