package aitest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/ai"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

// Harness is one metadata AI Store a contract case runs against.
type Harness struct {
	mutationtest.Harness[ai.Store]
	// SeedMember makes a Member a job can name as its requester.
	SeedMember func(t *testing.T) string
	// SeedTransport puts a job's generate command on the queue.
	SeedTransport func(t *testing.T, jobID string)
}

// FakeHarness is the Harness of a fresh in-memory Store.
func FakeHarness() Harness {
	store, _ := New()
	return Harness{
		Harness:       mutationtest.FakeHarness[ai.Store](store),
		SeedMember:    func(*testing.T) string { return uuid.NewString() },
		SeedTransport: func(_ *testing.T, jobID string) { store.SeedTransport(jobID) },
	}
}

// RunStoreContract runs every Store contract case against a fresh Harness,
// in parallel.
func RunStoreContract(t *testing.T, open func(t *testing.T) Harness) {
	t.Helper()
	for name, run := range storeContract {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			run(t, open(t))
		})
	}
}

// NewJob returns a queued Page summary job of requester.
func NewJob(requester string) ai.Job {
	return ai.Job{
		ID: uuid.NewString(), RequesterMemberID: requester, TargetType: "AI_RESOURCE_TYPE_PAGE", TargetID: uuid.NewString(),
		RequestedKeys: []string{"summary"}, Context: `{"task":{"requestedKeys":["summary"]},"source":{"title":"Page"}}`,
		Prompt: "Generate a short summary", Status: ai.JobQueued,
	}
}

func contractNow() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

func insert(t *testing.T, h Harness, job ai.Job) ai.Job {
	t.Helper()
	var stored ai.Job
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store ai.Store) (err error) {
		stored, err = store.InsertJob(ctx, job)
		return err
	}))
	return stored
}

func read(t *testing.T, h Harness, jobID string) ai.Job {
	t.Helper()
	var job ai.Job
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store ai.Store) (err error) {
		job, err = store.Job(ctx, jobID)
		return err
	}))
	return job
}

func write(t *testing.T, h Harness, work func(ctx context.Context, store ai.Store) (bool, error)) bool {
	t.Helper()
	var result bool
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store ai.Store) (err error) {
		result, err = work(ctx, store)
		return err
	}))
	return result
}

var storeContract = map[string]func(t *testing.T, h Harness){
	"a job reads back as inserted": func(t *testing.T, h Harness) {
		job := insert(t, h, NewJob(h.SeedMember(t)))
		stored := read(t, h, job.ID)
		require.Equal(t, ai.JobQueued, stored.Status)
		require.Equal(t, []string{"summary"}, stored.RequestedKeys)
		require.False(t, stored.CreatedAt.IsZero())
		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store ai.Store) error {
			_, err := store.Job(ctx, uuid.NewString())
			require.ErrorIs(t, err, ai.ErrJobNotFound)
			return nil
		}))
	},
	"a queued job is claimed by one delivery": func(t *testing.T, h Harness) {
		job := insert(t, h, NewJob(h.SeedMember(t)))
		startedAt := contractNow()
		claim := func(ctx context.Context, store ai.Store) (bool, error) {
			return store.ClaimJob(ctx, job, "gemini", "flash", startedAt)
		}
		require.True(t, write(t, h, claim))
		require.False(t, write(t, h, claim), "a second delivery that read the queued job loses")
		stored := read(t, h, job.ID)
		require.Equal(t, ai.JobRunning, stored.Status)
		require.Equal(t, "gemini", *stored.Provider)
		require.True(t, startedAt.Equal(*stored.StartedAt))
	},
	"a running job is reclaimed only as last started": func(t *testing.T, h Harness) {
		job := insert(t, h, NewJob(h.SeedMember(t)))
		first := contractNow().Add(-time.Hour)
		write(t, h, func(ctx context.Context, store ai.Store) (bool, error) {
			return store.ClaimJob(ctx, job, "gemini", "flash", first)
		})
		running := read(t, h, job.ID)
		stale := running
		other := first.Add(time.Minute)
		stale.StartedAt = &other
		require.False(t, write(t, h, func(ctx context.Context, store ai.Store) (bool, error) {
			return store.ClaimJob(ctx, stale, "gemini", "flash", contractNow())
		}))
		require.True(t, write(t, h, func(ctx context.Context, store ai.Store) (bool, error) {
			return store.ClaimJob(ctx, running, "gemini", "flash", contractNow())
		}))
	},
	"an outcome is stored only under the delivery's lease": func(t *testing.T, h Harness) {
		job := insert(t, h, NewJob(h.SeedMember(t)))
		startedAt := contractNow()
		write(t, h, func(ctx context.Context, store ai.Store) (bool, error) {
			return store.ClaimJob(ctx, job, "gemini", "flash", startedAt)
		})
		response := `{"summary":"Ready"}`
		ready := ai.JobOutcome{
			Status: ai.JobReady, Suggestion: map[string]string{"summary": "Ready"}, ResponseText: &response,
			DurationMS: 42, CompletedAt: startedAt.Add(time.Second),
		}
		require.False(t, write(t, h, func(ctx context.Context, store ai.Store) (bool, error) {
			return store.FinishJob(ctx, ai.Lease{JobID: job.ID, StartedAt: startedAt.Add(time.Minute)}, ready)
		}))
		require.True(t, write(t, h, func(ctx context.Context, store ai.Store) (bool, error) {
			return store.FinishJob(ctx, ai.Lease{JobID: job.ID, StartedAt: startedAt}, ready)
		}))
		stored := read(t, h, job.ID)
		require.Equal(t, ai.JobReady, stored.Status)
		require.Equal(t, map[string]string{"summary": "Ready"}, stored.Suggestion)
		require.Equal(t, response, *stored.ResponseText)
		require.Nil(t, stored.Error)
		require.EqualValues(t, 42, *stored.DurationMS)

		message := "provider failed"
		require.False(t, write(t, h, func(ctx context.Context, store ai.Store) (bool, error) {
			return store.FinishJob(ctx, ai.Lease{JobID: job.ID, StartedAt: startedAt}, ai.JobOutcome{
				Status: ai.JobFailed, Error: &message, CompletedAt: contractNow(),
			})
		}), "a finished job is no longer running")
	},
	"a ready job resolves once": func(t *testing.T, h Harness) {
		job := insert(t, h, NewJob(h.SeedMember(t)))
		startedAt := contractNow()
		write(t, h, func(ctx context.Context, store ai.Store) (bool, error) {
			return store.ClaimJob(ctx, job, "gemini", "flash", startedAt)
		})
		write(t, h, func(ctx context.Context, store ai.Store) (bool, error) {
			return store.FinishJob(ctx, ai.Lease{JobID: job.ID, StartedAt: startedAt}, ai.JobOutcome{
				Status: ai.JobReady, Suggestion: map[string]string{"summary": "Ready"}, CompletedAt: startedAt,
			})
		})
		resolve := func(ctx context.Context, store ai.Store) (bool, error) {
			return store.ResolveJob(ctx, job.ID, ai.JobApplied, contractNow())
		}
		require.True(t, write(t, h, resolve))
		require.False(t, write(t, h, resolve))
		stored := read(t, h, job.ID)
		require.Equal(t, ai.JobApplied, stored.Status)
		require.NotNil(t, stored.ResolvedAt)
	},
	"recovery locks only expired queued jobs and stale running leases": func(t *testing.T, h Harness) {
		member := h.SeedMember(t)
		now := contractNow()
		expired := insert(t, h, NewJob(member))
		running := insert(t, h, NewJob(member))
		fresh := insert(t, h, NewJob(member))
		recentLease := insert(t, h, NewJob(member))
		old := now.Add(-time.Hour)
		for _, job := range []ai.Job{running, recentLease} {
			write(t, h, func(ctx context.Context, store ai.Store) (bool, error) {
				return store.ClaimJob(ctx, job, "gemini", "flash", old)
			})
		}
		recentStart := now.Add(-time.Minute)
		reclaimed := read(t, h, recentLease.ID)
		write(t, h, func(ctx context.Context, store ai.Store) (bool, error) {
			return store.ClaimJob(ctx, reclaimed, "gemini", "flash", recentStart)
		})
		// Age every job's last update except the fresh one's.
		for _, job := range []ai.Job{expired, running, recentLease} {
			stored := read(t, h, job.ID)
			require.True(t, write(t, h, func(ctx context.Context, store ai.Store) (bool, error) {
				return store.TouchExpiredJob(ctx, stored, old)
			}))
		}
		require.Equal(t, ai.JobQueued, read(t, h, fresh.ID).Status)

		var locked []ai.Job
		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store ai.Store) (err error) {
			locked, err = store.LockExpiredJobs(ctx, now.Add(-10*time.Minute), now.Add(-3*time.Minute), 25)
			return err
		}))
		ids := make([]string, 0, len(locked))
		for _, job := range locked {
			ids = append(ids, job.ID)
		}
		require.ElementsMatch(t, []string{expired.ID, running.ID}, ids)
	},
	"touching an expired job compares its last update": func(t *testing.T, h Harness) {
		job := insert(t, h, NewJob(h.SeedMember(t)))
		stored := read(t, h, job.ID)
		touch := func(ctx context.Context, store ai.Store) (bool, error) {
			return store.TouchExpiredJob(ctx, stored, contractNow().Add(time.Second))
		}
		require.True(t, write(t, h, touch))
		require.False(t, write(t, h, touch), "another recovery already moved the job")
	},
	"the queue answers whether a job's command is pending": func(t *testing.T, h Harness) {
		member := h.SeedMember(t)
		pending := insert(t, h, NewJob(member))
		orphan := insert(t, h, NewJob(member))
		h.SeedTransport(t, pending.ID)
		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store ai.Store) error {
			found, err := store.TransportPending(ctx, pending.ID)
			require.True(t, found)
			if err != nil {
				return err
			}
			found, err = store.TransportPending(ctx, orphan.ID)
			require.False(t, found)
			return err
		}))
	},
	"a failed transaction leaves the job queued": func(t *testing.T, h Harness) {
		job := insert(t, h, NewJob(h.SeedMember(t)))
		rejected := errors.New("rejected")
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store ai.Store) error {
			if _, err := store.ClaimJob(ctx, job, "gemini", "flash", contractNow()); err != nil {
				return err
			}
			return rejected
		})
		require.ErrorIs(t, err, rejected)
		require.Equal(t, ai.JobQueued, read(t, h, job.ID).Status)
	},
}
