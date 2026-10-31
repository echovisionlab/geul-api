package failurecause

import (
	"context"
	"sync"
)

// Recorder carries a request's sanitized failure cause to the owner of its
// terminal request record, so one record holds the status and the real cause.
// The record owner installs it with WithRecorder; an inner failure boundary
// records into it instead of logging a second record.
type Recorder struct {
	mu       sync.Mutex
	cause    string
	stack    string
	recorded bool
}

type recorderKey struct{}

// WithRecorder returns ctx carrying a new Recorder.
func WithRecorder(ctx context.Context) (context.Context, *Recorder) {
	recorder := &Recorder{}
	return context.WithValue(ctx, recorderKey{}, recorder), recorder
}

// RecorderFrom returns the Recorder installed in ctx, or nil.
func RecorderFrom(ctx context.Context) *Recorder {
	recorder, _ := ctx.Value(recorderKey{}).(*Recorder)
	return recorder
}

// Record stores a sanitized cause and, for a panic, its stack. The last
// record wins: it is the one closest to the terminal outcome.
func (r *Recorder) Record(cause, stack string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cause, r.stack, r.recorded = cause, stack, true
}

// Recorded returns the stored cause and stack, and whether anything was
// recorded.
func (r *Recorder) Recorded() (cause, stack string, ok bool) {
	if r == nil {
		return "", "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cause, r.stack, r.recorded
}
