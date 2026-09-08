// Package jobregistry tracks active jobs and their cancellation contexts.
package jobregistry

import (
	"context"
	"sync"
)

type entry struct {
	groupID string
	cancel  context.CancelFunc
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
	cancel   context.CancelFunc
	close    sync.Once
}

// Start registers a job unless the event is already active.
func (r *Registry) Start(parent context.Context, eventID, groupID string) (*Session, bool) {
	ctx, cancel := context.WithCancel(parent)
	_, exists := r.running.LoadOrStore(eventID, entry{groupID: groupID, cancel: cancel})
	if exists {
		cancel()
		return nil, false
	}
	return &Session{
		Context:  ctx,
		registry: r,
		eventID:  eventID,
		cancel:   cancel,
	}, true
}

// Close removes the job and cancels its context exactly once.
func (s *Session) Close() {
	s.close.Do(func() {
		s.registry.running.Delete(s.eventID)
		s.cancel()
	})
}

// CancelEvent cancels one active event.
func (r *Registry) CancelEvent(eventID string) bool {
	value, found := r.running.Load(eventID)
	if !found {
		return false
	}
	value.(entry).cancel()
	return true
}

// CancelGroup cancels every active event in a group.
func (r *Registry) CancelGroup(groupID string) bool {
	found := false
	r.running.Range(func(_, value any) bool {
		registered := value.(entry)
		if registered.groupID == groupID {
			found = true
			registered.cancel()
		}
		return true
	})
	return found
}
