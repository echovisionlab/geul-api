// Package aitest is the in-memory metadata AI Store and the Store contract
// both the fake and the PostgreSQL store keep.
package aitest

import (
	"context"
	"maps"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/ai"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

// Store is the in-memory metadata AI Store. It knows no schema and takes no
// lock; the contract pins what it shares with PostgreSQL.
type Store struct {
	mu        sync.Mutex
	jobs      map[string]ai.Job
	transport map[string]bool
	enqueued  []string
	// EnqueueErr fails every generate command enqueue.
	EnqueueErr error
	// ReadErr fails the next job read, once.
	ReadErr error
}

// New returns an empty Store and its unit of work.
func New() (*Store, *mutationtest.UnitOfWork[ai.Store]) {
	store := &Store{jobs: map[string]ai.Job{}, transport: map[string]bool{}}
	return store, mutationtest.New[ai.Store](store)
}

// Savepoint captures the persisted fields and the rollback of the Store.
func (s *Store) Savepoint() mutationtest.Checkpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs := mutationtest.ClonePersisted(s.jobs)
	transport := mutationtest.ClonePersisted(s.transport)
	enqueued := mutationtest.ClonePersisted(s.enqueued)
	return mutationtest.Checkpoint{
		State: map[string]any{
			"jobs":      jobs,
			"transport": transport,
			"enqueued":  enqueued,
		},
		Restore: func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.jobs = jobs
			s.transport = transport
			s.enqueued = enqueued
		},
	}
}

// SeedJob stores job as it is.
func (s *Store) SeedJob(job ai.Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = job
}

// StoredJob returns a stored job.
func (s *Store) StoredJob(jobID string) (ai.Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	return job, ok
}

// Jobs returns every stored job.
func (s *Store) Jobs() []ai.Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Collect(maps.Values(s.jobs))
}

// SeedTransport puts the job's generate command on the queue.
func (s *Store) SeedTransport(jobID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transport[jobID] = true
}

// Enqueued returns the job IDs whose generate command was enqueued.
func (s *Store) Enqueued() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.enqueued)
}

func (s *Store) InsertJob(_ context.Context, job ai.Job) (ai.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job.ID == "" {
		job.ID = uuid.NewString()
	}
	now := time.Now()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	if job.UpdatedAt.IsZero() {
		job.UpdatedAt = now
	}
	s.jobs[job.ID] = job
	return job, nil
}

func (s *Store) Job(_ context.Context, jobID string) (ai.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ReadErr; err != nil {
		s.ReadErr = nil
		return ai.Job{}, err
	}
	job, ok := s.jobs[jobID]
	if !ok {
		return ai.Job{}, ai.ErrJobNotFound
	}
	return job, nil
}

func (s *Store) ResolveJob(_ context.Context, jobID string, status ai.JobStatus, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok || job.Status != ai.JobReady {
		return false, nil
	}
	job.Status, job.ResolvedAt, job.UpdatedAt = status, &now, now
	s.jobs[jobID] = job
	return true, nil
}

func sameStart(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func (s *Store) ClaimJob(_ context.Context, claimed ai.Job, provider, modelName string, startedAt time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[claimed.ID]
	if !ok || job.Status != claimed.Status {
		return false, nil
	}
	switch claimed.Status {
	case ai.JobQueued:
	case ai.JobRunning:
		if !sameStart(job.StartedAt, claimed.StartedAt) {
			return false, nil
		}
	default:
		return false, nil
	}
	job.Status, job.Provider, job.Model = ai.JobRunning, &provider, &modelName
	job.StartedAt, job.UpdatedAt = &startedAt, startedAt
	s.jobs[job.ID] = job
	return true, nil
}

func (s *Store) FinishJob(_ context.Context, lease ai.Lease, outcome ai.JobOutcome) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[lease.JobID]
	if !ok || job.Status != ai.JobRunning || job.StartedAt == nil || !job.StartedAt.Equal(lease.StartedAt) {
		return false, nil
	}
	durationMS, completedAt := outcome.DurationMS, outcome.CompletedAt
	job.Status, job.DurationMS, job.CompletedAt, job.UpdatedAt = outcome.Status, &durationMS, &completedAt, completedAt
	job.Error = outcome.Error
	if outcome.Status == ai.JobReady {
		job.Suggestion, job.ResponseText, job.Error = maps.Clone(outcome.Suggestion), outcome.ResponseText, nil
	}
	s.jobs[job.ID] = job
	return true, nil
}

func (s *Store) LockExpiredJobs(_ context.Context, recoveryBefore, staleBefore time.Time, limit int) ([]ai.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var expired []ai.Job
	for _, job := range s.jobs {
		if job.UpdatedAt.After(recoveryBefore) {
			continue
		}
		stale := job.StartedAt == nil || !job.StartedAt.After(staleBefore)
		if job.Status == ai.JobQueued || (job.Status == ai.JobRunning && stale) {
			expired = append(expired, job)
		}
	}
	sort.Slice(expired, func(i, j int) bool { return expired[i].UpdatedAt.Before(expired[j].UpdatedAt) })
	return expired[:min(limit, len(expired))], nil
}

func (s *Store) TransportPending(_ context.Context, jobID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transport[jobID], nil
}

func (s *Store) TouchExpiredJob(_ context.Context, touched ai.Job, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[touched.ID]
	if !ok || job.Status != touched.Status || !job.UpdatedAt.Equal(touched.UpdatedAt) ||
		(job.Status == ai.JobRunning && !sameStart(job.StartedAt, touched.StartedAt)) {
		return false, nil
	}
	job.UpdatedAt = now
	s.jobs[job.ID] = job
	return true, nil
}

func (s *Store) EnqueueGenerate(_ context.Context, jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.EnqueueErr != nil {
		return s.EnqueueErr
	}
	s.enqueued = append(s.enqueued, jobID)
	s.transport[jobID] = true
	return nil
}

var _ ai.Store = (*Store)(nil)
