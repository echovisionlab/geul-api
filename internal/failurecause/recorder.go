package failurecause

import (
	"context"
	"log/slog"
	"sync"
)

// Recorder carries a request's sanitized failure cause to the owner of its
// terminal request record, so one record holds the status and the real cause.
// The record owner installs it with WithRecorder; an inner failure boundary
// records into it instead of logging a second record.
type Recorder struct {
	mu       sync.Mutex
	failure  Failure
	recorded bool
}

// Failure is a sanitized cause for a terminal record: the cause text, a
// panic's stack, and the identifier attributes the error carried.
type Failure struct {
	Cause string
	Stack string
	Attrs []slog.Attr
}

// Of returns err's sanitized cause and the identifier attributes it carries.
func Of(err error, private ...string) Failure {
	return Failure{Cause: Message(err, private...), Attrs: Attrs(err)}
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

// Record stores a sanitized failure. The last record wins: it is the one
// closest to the terminal outcome. A nil Recorder, outside a request, drops
// it.
func (r *Recorder) Record(failure Failure) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failure, r.recorded = failure, true
}

// Recorded returns the stored failure, and whether anything was recorded.
func (r *Recorder) Recorded() (Failure, bool) {
	if r == nil {
		return Failure{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failure, r.recorded
}
