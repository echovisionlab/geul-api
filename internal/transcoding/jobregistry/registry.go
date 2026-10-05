// Package jobregistry tracks active jobs and their cancellation contexts.
package jobregistry

import (
	"context"
	"errors"
	"sync"
)

// ErrExplicitCancellation identifies a user or file-lifecycle cancellation.
// Parent-context shutdowns keep their own cancellation cause so queue delivery
// can remain redeliverable.
var ErrExplicitCancellation = errors.New("transcode job explicitly cancelled")

type entry struct {
	groupID string
	cancel  context.CancelCauseFunc
}

// Registry owns the active job sessions.
type Registry struct {
	running sync.Map
}

// Session represents one registered job and its cancellation context.
type Session struct {
	Context context.Context

	registry *Registry
	eventID  string
	cancel   context.CancelCauseFunc
	close    sync.Once
}

// Start registers a job unless the event is already active.
func (r *Registry) Start(parent context.Context, eventID, groupID string) (*Session, bool) {
	ctx, cancel := context.WithCancelCause(parent)
	_, exists := r.running.LoadOrStore(eventID, entry{groupID: groupID, cancel: cancel})
	if exists {
		cancel(nil)
		return nil, false
	}
	return &Session{
		Context:  ctx,
		registry: r,
		eventID:  eventID,
		cancel:   cancel,
	}, true
}

// IsExplicitCancellation reports whether ctx was stopped by a registry
// cancellation request rather than by its parent delivery context.
func IsExplicitCancellation(ctx context.Context) bool {
	return ctx != nil && errors.Is(context.Cause(ctx), ErrExplicitCancellation)
}

// Close removes the job and cancels its context exactly once.
func (s *Session) Close() {
	s.close.Do(func() {
		s.registry.running.Delete(s.eventID)
		s.cancel(nil)
	})
}

// CancelEvent cancels one active event.
func (r *Registry) CancelEvent(eventID string) bool {
	value, found := r.running.Load(eventID)
	if !found {
		return false
	}
	value.(entry).cancel(ErrExplicitCancellation)
	return true
}

// CancelGroup cancels every active event in a group.
func (r *Registry) CancelGroup(groupID string) bool {
	found := false
	r.running.Range(func(_, value any) bool {
		registered := value.(entry)
		if registered.groupID == groupID {
			found = true
			registered.cancel(ErrExplicitCancellation)
		}
		return true
	})
	return found
}
