package transcodetest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/transcode"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

// Harness is one Store implementation the contract runs against.
type Harness struct {
	mutationtest.Harness[transcode.Store]
	// SeedWaveformJob stores a waveform job, which the transcode
	// application creates outside this Store.
	SeedWaveformJob func(t *testing.T, job transcode.WaveformJob)
}

// FakeHarness is the Harness of a fresh in-memory Store.
func FakeHarness() Harness {
	store := NewStore()
	return Harness{
		Harness:         mutationtest.FakeHarness[transcode.Store](store),
		SeedWaveformJob: func(_ *testing.T, job transcode.WaveformJob) { store.SeedWaveformJob(job) },
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

func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// QueuedJob is a queued audio job of fileID created at at.
func QueuedJob(fileID string, at time.Time) transcode.Job {
	return transcode.Job{
		EventID: uuid.NewString(), QueueName: eventpkg.QueueTranscoderAudio, EntityType: "TRANSCODE_ENTITY_TYPE_POST",
		EntityID: uuid.NewString(), FileID: fileID, Payload: []byte{1, 2, 3}, Status: transcode.StatusQueued,
		CreatedAt: at, UpdatedAt: at,
	}
}

func transact(t *testing.T, h Harness, work func(ctx context.Context, store transcode.Store) error) error {
	t.Helper()
	return mutationtest.Transact(t, h.Harness, work)
}

func insert(t *testing.T, h Harness, jobs ...transcode.Job) {
	t.Helper()
	require.NoError(t, transact(t, h, func(ctx context.Context, store transcode.Store) error {
		for _, job := range jobs {
			inserted, err := store.InsertJob(ctx, job)
			if err != nil {
				return err
			}
			require.True(t, inserted)
		}
		return nil
	}))
}

func read(t *testing.T, h Harness, eventID string) (transcode.Job, error) {
	t.Helper()
	var job transcode.Job
	err := transact(t, h, func(ctx context.Context, store transcode.Store) (err error) {
		job, err = store.LockJob(ctx, eventID)
		return err
	})
	return job, err
}

func mustRead(t *testing.T, h Harness, eventID string) transcode.Job {
	t.Helper()
	job, err := read(t, h, eventID)
	require.NoError(t, err)
	return job
}

var storeContract = map[string]func(t *testing.T, h Harness){
	"a job is stored once and reads back with its payload": func(t *testing.T, h Harness) {
		at := now()
		job := QueuedJob(uuid.NewString(), at)
		insert(t, h, job)
		require.NoError(t, transact(t, h, func(ctx context.Context, store transcode.Store) error {
			again := job
			again.Payload = []byte{9}
			inserted, err := store.InsertJob(ctx, again)
			require.False(t, inserted)
			return err
		}))
		stored := mustRead(t, h, job.EventID)
		require.Equal(t, job.Payload, stored.Payload)
		require.Equal(t, transcode.StatusQueued, stored.Status)
		require.Equal(t, job.QueueName, stored.QueueName)
		require.Equal(t, job.EntityID, stored.EntityID)
		require.True(t, stored.CreatedAt.Equal(at))
		_, err := read(t, h, uuid.NewString())
		require.ErrorIs(t, err, transcode.ErrJobNotFound)
	},
	"a newer job cancels the other active jobs of its File and queue": func(t *testing.T, h Harness) {
		at, fileID := now(), uuid.NewString()
		active, processing, current := QueuedJob(fileID, at), QueuedJob(fileID, at), QueuedJob(fileID, at)
		processing.Status = transcode.StatusProcessing
		failed, otherQueue, otherFile := QueuedJob(fileID, at), QueuedJob(fileID, at), QueuedJob(uuid.NewString(), at)
		failed.Status = transcode.StatusFailedTerminal
		otherQueue.QueueName = eventpkg.QueueTranscoderVideo
		insert(t, h, active, processing, current, failed, otherQueue, otherFile)

		require.NoError(t, transact(t, h, func(ctx context.Context, store transcode.Store) error {
			return store.CancelOtherActiveJobs(ctx, current.EventID, current.QueueName, fileID, "superseded", at)
		}))
		for _, cancelled := range []string{active.EventID, processing.EventID} {
			job := mustRead(t, h, cancelled)
			require.Equal(t, transcode.StatusCancelled, job.Status)
			require.Equal(t, "superseded", *job.LastError)
			require.True(t, job.CompletedAt.Equal(at))
		}
		require.Equal(t, transcode.StatusQueued, mustRead(t, h, current.EventID).Status)
		require.Equal(t, transcode.StatusFailedTerminal, mustRead(t, h, failed.EventID).Status)
		require.Equal(t, transcode.StatusQueued, mustRead(t, h, otherQueue.EventID).Status)
		require.Equal(t, transcode.StatusQueued, mustRead(t, h, otherFile.EventID).Status)
	},
	"progress applies to unsettled jobs and keeps an absent sequence": func(t *testing.T, h Harness) {
		at := now()
		job, settled := QueuedJob(uuid.NewString(), at), QueuedJob(uuid.NewString(), at)
		settled.Status = transcode.StatusCancelled
		insert(t, h, job, settled)
		sequence, stage := int64(4), "TRANSCODE_STAGE_DOWNLOADING"
		require.NoError(t, transact(t, h, func(ctx context.Context, store transcode.Store) error {
			_, found, err := store.LockUnsettledJob(ctx, settled.EventID)
			require.False(t, found)
			if err != nil {
				return err
			}
			locked, found, err := store.LockUnsettledJob(ctx, job.EventID)
			require.True(t, found)
			require.Equal(t, job.EventID, locked.EventID)
			if err != nil {
				return err
			}
			return store.RecordProgress(ctx, job.EventID, transcode.Progress{
				Progress: 30, HLSProgress: 20, SpectrogramProgress: 40, LastSequence: &sequence, LastStage: &stage,
			}, at)
		}))
		require.NoError(t, transact(t, h, func(ctx context.Context, store transcode.Store) error {
			return store.RecordProgress(ctx, job.EventID, transcode.Progress{Progress: 50, HLSProgress: 50, SpectrogramProgress: 60}, at)
		}))
		stored := mustRead(t, h, job.EventID)
		require.Equal(t, transcode.StatusProcessing, stored.Status)
		require.Equal(t, []int{50, 50, 60}, []int{stored.Progress, stored.HLSProgress, stored.SpectrogramProgress})
		require.Equal(t, sequence, *stored.LastSequence)
		require.Equal(t, stage, *stored.LastStage)
	},
	"completion candidates match the whole identity, newest first": func(t *testing.T, h Harness) {
		at := now()
		job := QueuedJob(uuid.NewString(), at)
		insert(t, h, job)
		identity := transcode.CompletionIdentity{
			EventID: job.EventID, QueueName: job.QueueName, EntityType: job.EntityType, EntityID: job.EntityID, FileID: job.FileID,
		}
		candidates := func(identity transcode.CompletionIdentity) []transcode.Job {
			var jobs []transcode.Job
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store transcode.Store) (err error) {
				jobs, err = store.CompletionCandidates(ctx, identity)
				return err
			}))
			return jobs
		}
		require.Len(t, candidates(identity), 1)
		for _, mismatch := range []func(*transcode.CompletionIdentity){
			func(i *transcode.CompletionIdentity) { i.QueueName = eventpkg.QueueTranscoderVideo },
			func(i *transcode.CompletionIdentity) { i.EntityType = "TRANSCODE_ENTITY_TYPE_PAGE" },
			func(i *transcode.CompletionIdentity) { i.EntityID = uuid.NewString() },
			func(i *transcode.CompletionIdentity) { i.FileID = uuid.NewString() },
		} {
			other := identity
			mismatch(&other)
			require.Empty(t, candidates(other))
		}
	},
	"jobs fail, cancel per File and delete": func(t *testing.T, h Harness) {
		at, fileID := now(), uuid.NewString()
		failing, first, second, done := QueuedJob(uuid.NewString(), at), QueuedJob(fileID, at), QueuedJob(fileID, at), QueuedJob(fileID, at)
		second.QueueName = eventpkg.QueueTranscoderVideo
		done.Status = transcode.StatusCompleted
		insert(t, h, failing, first, second, done)
		require.NoError(t, transact(t, h, func(ctx context.Context, store transcode.Store) error {
			if err := store.FailJob(ctx, failing.EventID, "render failed", at); err != nil {
				return err
			}
			return store.CancelFileJobs(ctx, fileID, "cancelled: TEST", at)
		}))
		failed := mustRead(t, h, failing.EventID)
		require.Equal(t, transcode.StatusFailedTerminal, failed.Status)
		require.Equal(t, "render failed", *failed.LastError)
		require.Equal(t, transcode.StatusCancelled, mustRead(t, h, first.EventID).Status)
		require.Equal(t, transcode.StatusCancelled, mustRead(t, h, second.EventID).Status)
		require.Equal(t, transcode.StatusCompleted, mustRead(t, h, done.EventID).Status)

		require.NoError(t, transact(t, h, func(ctx context.Context, store transcode.Store) error {
			return store.DeleteJob(ctx, failing.EventID)
		}))
		_, err := read(t, h, failing.EventID)
		require.ErrorIs(t, err, transcode.ErrJobNotFound)
	},
	"a locked job makes a second registration wait for its commit": func(t *testing.T, h Harness) {
		at := now()
		job := QueuedJob(uuid.NewString(), at)
		insert(t, h, job)
		var seen transcode.Job
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store transcode.Store) error {
				_, err := store.LockJob(ctx, job.EventID)
				return err
			},
			func(ctx context.Context, store transcode.Store) error {
				return store.FailJob(ctx, job.EventID, "first", at)
			},
			func(ctx context.Context, store transcode.Store) (err error) {
				seen, err = store.LockJob(ctx, job.EventID)
				return err
			})
		require.NoError(t, err)
		require.Equal(t, transcode.StatusFailedTerminal, seen.Status)
	},
	"queued Track waveform jobs cancel at their owner's request": func(t *testing.T, h Harness) {
		at, trackID := now(), uuid.NewString()
		waveform := func(entityType, entityID, status string) transcode.WaveformJob {
			job := transcode.WaveformJob{
				EventID: uuid.NewString(), EntityType: entityType, EntityID: entityID, FileID: uuid.NewString(),
				Status: status, CreatedAt: at, UpdatedAt: at,
			}
			h.SeedWaveformJob(t, job)
			return job
		}
		queued := waveform("TRANSCODE_ENTITY_TYPE_TRACK", trackID, transcode.WaveformJobStatusQueued)
		waveform("TRANSCODE_ENTITY_TYPE_TRACK", trackID, transcode.WaveformJobStatusFailed)
		waveform("TRANSCODE_ENTITY_TYPE_POST", trackID, transcode.WaveformJobStatusQueued)
		waveform("TRANSCODE_ENTITY_TYPE_TRACK", uuid.NewString(), transcode.WaveformJobStatusQueued)

		require.NoError(t, transact(t, h, func(ctx context.Context, store transcode.Store) error {
			jobs, err := store.QueuedTrackWaveformJobs(ctx, []string{trackID})
			require.NoError(t, err)
			require.Len(t, jobs, 1)
			require.Equal(t, queued.EventID, jobs[0].EventID)
			require.Equal(t, queued.FileID, jobs[0].FileID)
			return store.CancelWaveformJobs(ctx, []string{queued.EventID}, "cancelled: TEST", at)
		}))
		require.NoError(t, transact(t, h, func(ctx context.Context, store transcode.Store) error {
			jobs, err := store.QueuedTrackWaveformJobs(ctx, []string{trackID})
			require.Empty(t, jobs)
			return err
		}))
	},
}
