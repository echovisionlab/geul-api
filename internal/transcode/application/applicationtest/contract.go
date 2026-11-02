package applicationtest

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/transcode"
	"github.com/echovisionlab/geul-api/internal/transcode/application"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

// Harness is one Store implementation the contract runs against, with the
// rows results apply to but File media owns.
type Harness struct {
	mutationtest.Harness[application.Store]
	// SeedFile stores a File and returns its ID.
	SeedFile func(t *testing.T, extension, mimeType string) string
	// RequestFileDeletion marks a File pending deletion.
	RequestFileDeletion func(t *testing.T, fileID string)
	// SeedAllocatedAsset allocates a public asset of kind made from fileID.
	SeedAllocatedAsset func(t *testing.T, kind, fileID, extension, mimeType string) *commonv1.AssetWriteTarget
	// SeedAllocatedGeneration allocates an HLS generation of fileID.
	SeedAllocatedGeneration func(t *testing.T, fileID string) *commonv1.MediaGenerationWriteTarget
	// SeedJob stores a transcode job.
	SeedJob func(t *testing.T, job transcode.Job)
	// SeedWaveformJob stores a waveform job.
	SeedWaveformJob func(t *testing.T, job transcode.WaveformJob)
}

// FakeHarness is the Harness of a fresh in-memory Store.
func FakeHarness() Harness {
	store := NewStore()
	return Harness{
		Harness: mutationtest.FakeHarness[application.Store](store),
		SeedFile: func(_ *testing.T, extension, mimeType string) string {
			id := uuid.NewString()
			store.SeedFile(File{File: application.File{ID: id, MimeType: mimeType}, Extension: extension})
			return id
		},
		RequestFileDeletion: func(_ *testing.T, fileID string) { store.RequestFileDeletion(fileID) },
		SeedAllocatedAsset: func(_ *testing.T, kind, fileID, extension, mimeType string) *commonv1.AssetWriteTarget {
			return store.SeedAllocatedAsset(kind, fileID, extension, mimeType)
		},
		SeedAllocatedGeneration: func(_ *testing.T, fileID string) *commonv1.MediaGenerationWriteTarget {
			return store.SeedAllocatedGeneration(fileID)
		},
		SeedJob:         func(_ *testing.T, job transcode.Job) { store.SeedJob(job) },
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

func transact(t *testing.T, h Harness, work func(ctx context.Context, store application.Store) error) {
	t.Helper()
	require.NoError(t, mutationtest.Transact(t, h.Harness, work))
}

// Written is the renderer's result for an allocated asset.
func Written(target *commonv1.AssetWriteTarget, fill byte) *commonv1.AssetWriteResult {
	return &commonv1.AssetWriteResult{AssetId: target.GetAssetId(), FileSize: 1024, Sha256: bytes.Repeat([]byte{fill}, 32)}
}

// WrittenGeneration is the transcoder's result for an allocated generation.
func WrittenGeneration(target *commonv1.MediaGenerationWriteTarget, fill byte) *commonv1.MediaGenerationWriteResult {
	return &commonv1.MediaGenerationWriteResult{
		GenerationId: target.GetGenerationId(), ManifestSha256: bytes.Repeat([]byte{fill}, 32), ObjectCount: 3, TotalSize: 4096,
	}
}

var storeContract = map[string]func(t *testing.T, h Harness){
	"a File accepts results until its deletion is requested": func(t *testing.T, h Harness) {
		fileID := h.SeedFile(t, "ogg", "audio/ogg")
		transact(t, h, func(ctx context.Context, store application.Store) error {
			accepts, err := store.FileAcceptsResults(ctx, fileID)
			require.True(t, accepts)
			require.NoError(t, err)
			locked, err := store.LockFileAcceptingResults(ctx, fileID)
			require.True(t, locked)
			require.NoError(t, err)
			source, found, err := store.WaveformSource(ctx, fileID)
			require.True(t, found)
			require.Equal(t, "ogg", source.GetExtension())
			require.Equal(t, "audio/ogg", source.GetMimeType())
			require.NoError(t, err)
			return store.SetFileDuration(ctx, fileID, 42)
		})
		h.RequestFileDeletion(t, fileID)
		transact(t, h, func(ctx context.Context, store application.Store) error {
			accepts, err := store.FileAcceptsResults(ctx, fileID)
			require.False(t, accepts)
			require.NoError(t, err)
			locked, err := store.LockFileAcceptingResults(ctx, fileID)
			require.False(t, locked)
			require.NoError(t, err)
			_, found, err := store.WaveformSource(ctx, fileID)
			require.False(t, found)
			require.NoError(t, err)
			file, err := store.ProcessingFile(ctx, fileID)
			require.Equal(t, 42, *file.DurationSeconds)
			require.Equal(t, "audio/ogg", file.MimeType)
			require.NoError(t, err)
			_, err = store.ProcessingFile(ctx, uuid.NewString())
			require.ErrorIs(t, err, application.ErrFileNotFound)
			return nil
		})
	},
	"a derivative switches outputs and only ready outputs count as ready": func(t *testing.T, h Harness) {
		fileID := h.SeedFile(t, "ogg", "audio/ogg")
		first := h.SeedAllocatedAsset(t, "spectrogram", fileID, "png", "image/png")
		second := h.SeedAllocatedAsset(t, "spectrogram", fileID, "png", "image/png")
		generation := h.SeedAllocatedGeneration(t, fileID)
		const spectrogram, hls = "FILE_DERIVATIVE_TYPE_SPECTROGRAM", "FILE_DERIVATIVE_TYPE_HLS"
		transact(t, h, func(ctx context.Context, store application.Store) error {
			if _, err := store.CompleteAsset(ctx, Written(first, 1)); err != nil {
				return err
			}
			if err := store.UpsertDerivative(ctx, application.Derivative{ID: uuid.NewString(), FileID: fileID, Type: spectrogram, AssetID: &first.AssetId}); err != nil {
				return err
			}
			return store.UpsertDerivative(ctx, application.Derivative{ID: uuid.NewString(), FileID: fileID, Type: hls, MediaGenerationID: &generation.GenerationId})
		})
		transact(t, h, func(ctx context.Context, store application.Store) error {
			ready, err := store.ReadyDerivatives(ctx, fileID)
			require.Len(t, ready, 1, "the allocated generation is not ready yet")
			require.Equal(t, spectrogram, ready[0].Type)
			if err != nil {
				return err
			}
			if err := store.UpsertDerivative(ctx, application.Derivative{ID: uuid.NewString(), FileID: fileID, Type: spectrogram, AssetID: &second.AssetId}); err != nil {
				return err
			}
			assetID, found, err := store.DerivativeAsset(ctx, fileID, spectrogram)
			require.True(t, found)
			require.Equal(t, second.AssetId, *assetID)
			count, _ := store.AssetDerivativeCount(ctx, first.AssetId)
			require.Zero(t, count)
			count, _ = store.GenerationDerivativeCount(ctx, generation.GenerationId)
			require.EqualValues(t, 1, count)
			types, _ := store.DerivativeTypes(ctx, fileID, []string{hls, spectrogram, "FILE_DERIVATIVE_TYPE_WAVEFORM"})
			require.ElementsMatch(t, []string{hls, spectrogram}, types)
			ready, _ = store.ReadyDerivatives(ctx, fileID)
			require.Empty(t, ready, "the second spectrogram is still allocated")
			return err
		})
		transact(t, h, func(ctx context.Context, store application.Store) error {
			if err := store.DeleteDerivative(ctx, fileID, spectrogram); err != nil {
				return err
			}
			_, found, err := store.DerivativeAsset(ctx, fileID, spectrogram)
			require.False(t, found)
			return err
		})
	},
	"an asset completes once and is retired only from a live state": func(t *testing.T, h Harness) {
		fileID := h.SeedFile(t, "ogg", "audio/ogg")
		target := h.SeedAllocatedAsset(t, "waveform", fileID, "json", "application/json")
		transact(t, h, func(ctx context.Context, store application.Store) error {
			asset, err := store.CompleteAsset(ctx, Written(target, 7))
			require.NoError(t, err)
			require.Equal(t, application.AssetReady, asset.Status)
			require.Equal(t, "waveform", asset.Kind)
			require.Equal(t, fileID, *asset.SourceFileID)
			again, err := store.CompleteAsset(ctx, Written(target, 7))
			require.Equal(t, asset.ID, again.ID)
			return err
		})
		transact(t, h, func(ctx context.Context, store application.Store) error {
			_, err := store.CompleteAsset(ctx, Written(target, 8))
			require.Error(t, err, "a ready asset keeps its digest")
			return nil
		})
		transact(t, h, func(ctx context.Context, store application.Store) error {
			if err := store.MarkAssetDeletePending(ctx, target.AssetId, time.Now().UTC()); err != nil {
				return err
			}
			asset, found, err := store.LockAsset(ctx, target.AssetId)
			require.True(t, found)
			require.Equal(t, application.AssetDeletePending, asset.Status)
			require.EqualValues(t, 1024, *asset.FileSize)
			_, found, _ = store.Asset(ctx, uuid.NewString())
			require.False(t, found)
			count, _ := store.AssetBindingCount(ctx, target.AssetId)
			require.Zero(t, count)
			return err
		})
	},
	"a waveform output allocates for its File and completes": func(t *testing.T, h Harness) {
		fileID := h.SeedFile(t, "ogg", "audio/ogg")
		var target *commonv1.AssetWriteTarget
		transact(t, h, func(ctx context.Context, store application.Store) (err error) {
			target, err = store.AllocateWaveformOutput(ctx, fileID)
			return err
		})
		require.Equal(t, "json", target.GetExtension())
		require.Equal(t, commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE, target.GetDisposition())
		transact(t, h, func(ctx context.Context, store application.Store) error {
			asset, err := store.CompleteAsset(ctx, Written(target, 3))
			require.Equal(t, fileID, *asset.SourceFileID)
			return err
		})
	},
	"a media generation completes and retires once unused": func(t *testing.T, h Harness) {
		fileID := h.SeedFile(t, "mp4", "video/mp4")
		ready, retiring := h.SeedAllocatedGeneration(t, fileID), h.SeedAllocatedGeneration(t, fileID)
		now := time.Now().UTC()
		transact(t, h, func(ctx context.Context, store application.Store) error {
			generation, err := store.CompleteMediaGeneration(ctx, WrittenGeneration(ready, 1))
			require.Equal(t, application.GenerationReady, generation.Status)
			require.Equal(t, fileID, generation.FileID)
			if err != nil {
				return err
			}
			if err := store.RetireMediaGeneration(ctx, retiring.GenerationId, WrittenGeneration(retiring, 2), now); err != nil {
				return err
			}
			return store.RetireMediaGeneration(ctx, ready.GenerationId, nil, now)
		})
		transact(t, h, func(ctx context.Context, store application.Store) error {
			for _, id := range []string{ready.GenerationId, retiring.GenerationId} {
				generation, found, err := store.LockMediaGeneration(ctx, id)
				require.True(t, found)
				require.Equal(t, application.GenerationRetired, generation.Status)
				require.Equal(t, "hls", generation.Kind)
				require.NoError(t, err)
			}
			_, found, err := store.LockMediaGeneration(ctx, uuid.NewString())
			require.False(t, found)
			return err
		})
	},
	"a completion reads its job by the whole identity and settles only an active one": func(t *testing.T, h Harness) {
		fileID := uuid.NewString()
		job := transcode.Job{
			EventID: uuid.NewString(), QueueName: eventpkg.QueueTranscoderAudio, EntityType: "TRANSCODE_ENTITY_TYPE_POST",
			EntityID: uuid.NewString(), FileID: fileID, Payload: []byte{1}, Status: transcode.StatusProcessing,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		h.SeedJob(t, job)
		identity := transcode.CompletionIdentity{EventID: job.EventID, QueueName: job.QueueName, EntityType: job.EntityType, EntityID: job.EntityID, FileID: fileID}
		now := time.Now().UTC()
		transact(t, h, func(ctx context.Context, store application.Store) error {
			found, err := store.TranscodeJob(ctx, identity, true)
			require.NoError(t, err)
			require.Equal(t, job.Payload, found.Payload)
			require.Equal(t, transcode.StatusProcessing, found.Status)
			other := identity
			other.QueueName = eventpkg.QueueTranscoderVideo
			missing, err := store.TranscodeJob(ctx, other, false)
			require.Nil(t, missing)
			if err != nil {
				return err
			}
			failed, err := store.FailActiveTranscodeJob(ctx, job.EventID, "render failed", now)
			require.True(t, failed)
			if err != nil {
				return err
			}
			failed, err = store.FailActiveTranscodeJob(ctx, job.EventID, "again", now)
			require.False(t, failed)
			if err != nil {
				return err
			}
			if err := store.CancelActiveTranscodeJob(ctx, job.EventID, "stale", now); err != nil {
				return err
			}
			count, err := store.FailedTranscodeJobs(ctx, fileID, job.QueueName)
			require.EqualValues(t, 1, count)
			jobs, _ := store.FileTranscodeJobs(ctx, fileID)
			require.Len(t, jobs, 1)
			require.Equal(t, transcode.StatusFailedTerminal, jobs[0].Status)
			if err != nil {
				return err
			}
			if err := store.DeleteTranscodeJob(ctx, job.EventID, uuid.NewString()); err != nil {
				return err
			}
			jobs, _ = store.FileTranscodeJobs(ctx, fileID)
			require.Len(t, jobs, 1, "a job deletes only with its own File")
			return store.DeleteTranscodeJob(ctx, job.EventID, fileID)
		})
	},
	"waveform jobs count only uncancelled ones and progress only while queued": func(t *testing.T, h Harness) {
		fileID, now := uuid.NewString(), time.Now().UTC()
		waveform := func(status string, cancelRequested bool) transcode.WaveformJob {
			job := transcode.WaveformJob{
				EventID: uuid.NewString(), EntityType: "TRANSCODE_ENTITY_TYPE_POST", EntityID: uuid.NewString(),
				FileID: fileID, Status: status, CancelRequested: cancelRequested, CreatedAt: now, UpdatedAt: now,
			}
			h.SeedWaveformJob(t, job)
			return job
		}
		queued := waveform(transcode.WaveformJobStatusQueued, false)
		waveform(transcode.WaveformJobStatusQueued, true)
		failed := waveform(transcode.WaveformJobStatusFailed, false)
		transact(t, h, func(ctx context.Context, store application.Store) error {
			count, err := store.QueuedWaveformJobs(ctx, fileID)
			require.EqualValues(t, 1, count)
			require.NoError(t, err)
			count, err = store.FailedWaveformJobs(ctx, fileID)
			require.EqualValues(t, 1, count)
			require.NoError(t, err)
			if err := store.RecordWaveformProgress(ctx, queued.EventID, 40, 2, "WAVEFORM_STAGE_PROCESSING", now); err != nil {
				return err
			}
			if err := store.RecordWaveformProgress(ctx, queued.EventID, 10, 1, "WAVEFORM_STAGE_DOWNLOADING", now); err != nil {
				return err
			}
			return store.RecordWaveformProgress(ctx, failed.EventID, 90, 3, "WAVEFORM_STAGE_PROCESSING", now)
		})
		transact(t, h, func(ctx context.Context, store application.Store) error {
			progressed, err := store.WaveformJob(ctx, queued.EventID)
			require.Equal(t, 40, progressed.Progress)
			untouched, _ := store.WaveformJob(ctx, failed.EventID)
			require.Zero(t, untouched.Progress)
			missing, _ := store.WaveformJob(ctx, uuid.NewString())
			require.Nil(t, missing)
			jobs, _ := store.FileWaveformJobs(ctx, fileID)
			require.Len(t, jobs, 3)
			if err != nil {
				return err
			}
			if err := store.CancelWaveformJob(ctx, queued.EventID, "stale", now); err != nil {
				return err
			}
			cancelled, _ := store.WaveformJob(ctx, queued.EventID)
			require.Equal(t, transcode.WaveformJobStatusCancelled, cancelled.Status)
			require.True(t, cancelled.CancelRequested)
			if err := store.DeleteFileWaveformJob(ctx, queued.EventID, uuid.NewString()); err != nil {
				return err
			}
			kept, _ := store.WaveformJob(ctx, queued.EventID)
			require.NotNil(t, kept)
			return store.DeleteWaveformJob(ctx, queued.EventID)
		})
	},
}
