// Package transcodetest holds the in-memory transcode Store and its Store
// contract.
package transcodetest

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/transcode"
)

// Store is an in-memory transcode.Store.
type Store struct {
	mu        sync.Mutex
	jobs      map[string]transcode.Job
	waveforms map[string]transcode.WaveformJob
}

// New returns an empty fake Store and its unit of work.
func New() (*Store, *mutationtest.UnitOfWork[transcode.Store]) {
	store := NewStore()
	return store, mutationtest.New[transcode.Store](store)
}

// NewStore returns an empty fake Store.
func NewStore() *Store {
	return &Store{jobs: map[string]transcode.Job{}, waveforms: map[string]transcode.WaveformJob{}}
}

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() mutationtest.Checkpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs := mutationtest.ClonePersisted(s.jobs)
	waveforms := mutationtest.ClonePersisted(s.waveforms)
	return mutationtest.Checkpoint{
		State: map[string]any{
			"jobs":      jobs,
			"waveforms": waveforms,
		},
		Restore: func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.jobs = jobs
			s.waveforms = waveforms
		},
	}
}

// SeedJob stores a transcode job.
func (s *Store) SeedJob(job transcode.Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.EventID] = job
}

// SeedWaveformJob stores a waveform job.
func (s *Store) SeedWaveformJob(job transcode.WaveformJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waveforms[job.EventID] = job
}

// Job returns a stored transcode job.
func (s *Store) Job(eventID string) (transcode.Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[eventID]
	return job, ok
}

// WaveformJob returns a stored waveform job.
func (s *Store) WaveformJob(eventID string) (transcode.WaveformJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.waveforms[eventID]
	return job, ok
}

func (s *Store) InsertJob(_ context.Context, job transcode.Job) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[job.EventID]; exists {
		return false, nil
	}
	job.Payload = slices.Clone(job.Payload)
	s.jobs[job.EventID] = job
	return true, nil
}

func (s *Store) CancelOtherActiveJobs(_ context.Context, eventID, queueName, fileID, reason string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, job := range s.jobs {
		if id != eventID && job.QueueName == queueName && job.FileID == fileID && slices.Contains(transcode.ActiveStatuses(), job.Status) {
			s.jobs[id] = cancelled(job, reason, now)
		}
	}
	return nil
}

func (s *Store) LockJob(_ context.Context, eventID string) (transcode.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[eventID]
	if !ok {
		return transcode.Job{}, transcode.ErrJobNotFound
	}
	return job, nil
}

func (s *Store) LockUnsettledJob(_ context.Context, eventID string) (transcode.Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[eventID]
	if !ok || job.Status == transcode.StatusCompleted || job.Status == transcode.StatusCancelled || job.Status == transcode.StatusFailedTerminal {
		return transcode.Job{}, false, nil
	}
	return job, true, nil
}

func (s *Store) RecordProgress(_ context.Context, eventID string, progress transcode.Progress, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[eventID]
	if !ok {
		return nil
	}
	job.Status, job.Progress, job.HLSProgress, job.SpectrogramProgress, job.UpdatedAt =
		transcode.StatusProcessing, progress.Progress, progress.HLSProgress, progress.SpectrogramProgress, now
	if progress.LastSequence != nil {
		job.LastSequence = new(*progress.LastSequence)
	}
	if progress.LastStage != nil {
		job.LastStage = new(*progress.LastStage)
	}
	s.jobs[eventID] = job
	return nil
}

func (s *Store) CompletionCandidates(_ context.Context, identity transcode.CompletionIdentity) ([]transcode.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var jobs []transcode.Job
	for _, job := range s.jobs {
		if job.EventID == identity.EventID && job.QueueName == identity.QueueName && job.EntityType == identity.EntityType &&
			job.EntityID == identity.EntityID && job.FileID == identity.FileID {
			jobs = append(jobs, job)
		}
	}
	slices.SortFunc(jobs, func(a, b transcode.Job) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return jobs, nil
}

func (s *Store) DeleteJob(_ context.Context, eventID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, eventID)
	return nil
}

func (s *Store) FailJob(_ context.Context, eventID, reason string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job, ok := s.jobs[eventID]; ok {
		job.Status, job.LastError, job.CompletedAt, job.UpdatedAt = transcode.StatusFailedTerminal, new(reason), new(now), now
		s.jobs[eventID] = job
	}
	return nil
}

func (s *Store) CancelFileJobs(_ context.Context, fileID, reason string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, job := range s.jobs {
		if job.FileID == fileID && slices.Contains(transcode.ActiveStatuses(), job.Status) {
			s.jobs[id] = cancelled(job, reason, now)
		}
	}
	return nil
}

func (s *Store) QueuedTrackWaveformJobs(_ context.Context, trackIDs []string) ([]transcode.WaveformJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var jobs []transcode.WaveformJob
	for _, job := range s.waveforms {
		if job.EntityType == "TRANSCODE_ENTITY_TYPE_TRACK" && slices.Contains(trackIDs, job.EntityID) && job.Status == transcode.WaveformJobStatusQueued {
			jobs = append(jobs, job)
		}
	}
	slices.SortFunc(jobs, func(a, b transcode.WaveformJob) int { return cmp.Compare(a.EventID, b.EventID) })
	return jobs, nil
}

func (s *Store) CancelWaveformJobs(_ context.Context, eventIDs []string, reason string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range eventIDs {
		job, ok := s.waveforms[id]
		if !ok {
			continue
		}
		job.Status, job.CancelRequested, job.LastError, job.CompletedAt, job.UpdatedAt =
			transcode.WaveformJobStatusCancelled, true, new(reason), new(now), now
		s.waveforms[id] = job
	}
	return nil
}

func cancelled(job transcode.Job, reason string, now time.Time) transcode.Job {
	job.Status, job.LastError, job.CompletedAt, job.UpdatedAt = transcode.StatusCancelled, new(reason), new(now), now
	return job
}

var _ transcode.Store = (*Store)(nil)
