package filemediatest

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/application"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type DeletionHarness struct {
	mutationtest.Harness[application.DeletionStore]
	SeedFile       func(*testing.T, filemedia.File)
	SeedGeneration func(*testing.T, mediaasset.Generation)
	SeedReference  func(*testing.T, string)
	SeedMeshResult func(*testing.T, string, string, bool)
}

func RunDeletionContract(t *testing.T, open func(*testing.T) DeletionHarness) {
	t.Helper()
	cases := map[string]func(*testing.T, DeletionHarness){
		"missing file":                            deletionContractMissingFile,
		"lock projection":                         deletionContractLockProjection,
		"requested deletion":                      deletionContractRequestedDeletion,
		"unrequested deletion":                    deletionContractUnrequestedDeletion,
		"rollback restores file":                  deletionContractRollback,
		"row lock serializes deletion":            deletionContractRowLock,
		"generation scope and order":              deletionContractGenerationScope,
		"active references":                       deletionContractActiveReferences,
		"only unselected unfinished mesh results": deletionContractPendingMeshResults,
		"empty pending results":                   deletionContractEmptyResults,
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) { t.Parallel(); run(t, open(t)) })
	}
}
func deletionContractFile(pending bool) filemedia.File {
	now := time.Now().UTC().Truncate(time.Microsecond)
	f := filemedia.File{ID: uuid.NewString(), FileName: "source", MimeType: "image/webp", FileSize: 1024, Extension: "webp", CreatedAt: now}
	if pending {
		f.DeleteRequestedAt = &now
	}
	return f
}
func deletionContractGeneration(fileID, id string) mediaasset.Generation {
	now := time.Now().UTC().Truncate(time.Microsecond)
	hash := sha256.Sum256([]byte(id))
	count, size := int32(2), int64(1024)
	return mediaasset.Generation{ID: id, FileID: fileID, Kind: "hls", ObjectPrefix: "media/" + fileID + "/hls/" + id, ManifestName: "master.m3u8", ManifestSHA256: hash[:], ObjectCount: &count, TotalSize: &size, Status: mediaasset.GenerationReady, ReadyAt: &now, CreatedAt: now, UpdatedAt: now}
}

func deletionContractMissingFile(t *testing.T, h DeletionHarness) {
	require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s application.DeletionStore) error {
		_, err := s.LockDeletionFile(ctx, uuid.NewString())
		return err
	}), filemedia.ErrFileNotFound)
}

func deletionContractLockProjection(t *testing.T, h DeletionHarness) {
	f := deletionContractFile(true)
	h.SeedFile(t, f)
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s application.DeletionStore) error {
		got, err := s.LockDeletionFile(ctx, f.ID)
		require.NoError(t, err)
		require.Equal(t, f.ID, got.ID)
		require.Equal(t, f.MimeType, got.MimeType)
		require.Equal(t, f.Extension, got.Extension)
		require.True(t, f.DeleteRequestedAt.Equal(*got.DeleteRequestedAt))
		require.Empty(t, got.FileName)
		return nil
	}))
}

func deletionContractRequestedDeletion(t *testing.T, h DeletionHarness) {
	f := deletionContractFile(true)
	h.SeedFile(t, f)
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s application.DeletionStore) error {
		ok, err := s.DeleteRequestedFile(ctx, f.ID)
		require.NoError(t, err)
		require.True(t, ok)
		exists, err := s.FileExists(ctx, f.ID)
		require.NoError(t, err)
		require.False(t, exists)
		ok, err = s.DeleteRequestedFile(ctx, f.ID)
		require.NoError(t, err)
		require.False(t, ok)
		return nil
	}))
}

func deletionContractUnrequestedDeletion(t *testing.T, h DeletionHarness) {
	f := deletionContractFile(false)
	h.SeedFile(t, f)
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s application.DeletionStore) error {
		ok, err := s.DeleteRequestedFile(ctx, f.ID)
		require.NoError(t, err)
		require.False(t, ok)
		exists, err := s.FileExists(ctx, f.ID)
		require.NoError(t, err)
		require.True(t, exists)
		return nil
	}))
}

func deletionContractRollback(t *testing.T, h DeletionHarness) {
	f := deletionContractFile(true)
	h.SeedFile(t, f)
	abort := errors.New("abort")
	require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s application.DeletionStore) error {
		ok, err := s.DeleteRequestedFile(ctx, f.ID)
		require.NoError(t, err)
		require.True(t, ok)
		return abort
	}), abort)
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s application.DeletionStore) error {
		exists, err := s.FileExists(ctx, f.ID)
		require.True(t, exists)
		return err
	}))
}

func deletionContractRowLock(t *testing.T, h DeletionHarness) {
	f := deletionContractFile(true)
	h.SeedFile(t, f)
	require.ErrorIs(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s application.DeletionStore) error {
		_, err := s.LockDeletionFile(ctx, f.ID)
		return err
	}, func(ctx context.Context, s application.DeletionStore) error {
		_, err := s.DeleteRequestedFile(ctx, f.ID)
		return err
	}, func(ctx context.Context, s application.DeletionStore) error {
		_, err := s.LockDeletionFile(ctx, f.ID)
		return err
	}), filemedia.ErrFileNotFound)
}

func deletionContractGenerationScope(t *testing.T, h DeletionHarness) {
	f := deletionContractFile(true)
	other := deletionContractFile(false)
	h.SeedFile(t, f)
	h.SeedFile(t, other)
	first := deletionContractGeneration(f.ID, "11111111-1111-4111-8111-111111111111")
	last := deletionContractGeneration(f.ID, "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee")
	h.SeedGeneration(t, last)
	h.SeedGeneration(t, first)
	h.SeedGeneration(t, deletionContractGeneration(other.ID, uuid.NewString()))
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s application.DeletionStore) error {
		rows, err := s.FileGenerations(ctx, f.ID)
		require.NoError(t, err)
		require.Len(t, rows, 2)
		require.Equal(t, first.ID, rows[0].ID)
		require.Equal(t, last.ID, rows[1].ID)
		require.Equal(t, first.ObjectPrefix, rows[0].ObjectPrefix)
		require.Equal(t, first.ManifestSHA256, rows[0].ManifestSHA256)
		return nil
	}))
}

func deletionContractActiveReferences(t *testing.T, h DeletionHarness) {
	f := deletionContractFile(true)
	other := deletionContractFile(false)
	h.SeedFile(t, f)
	h.SeedFile(t, other)
	h.SeedReference(t, f.ID)
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s application.DeletionStore) error {
		refs, err := s.ActiveFileReferenceNames(ctx, f.ID)
		require.NoError(t, err)
		require.Equal(t, []string{"client.logo_file_ids"}, refs)
		refs, err = s.ActiveFileReferenceNames(ctx, other.ID)
		require.NoError(t, err)
		require.Empty(t, refs)
		return nil
	}))
}

func deletionContractPendingMeshResults(t *testing.T, h DeletionHarness) {
	f := deletionContractFile(true)
	other := deletionContractFile(false)
	h.SeedFile(t, f)
	h.SeedFile(t, other)
	for _, status := range []string{"pending", "processing", "cancelled", "ready", "failed"} {
		h.SeedMeshResult(t, f.ID, status, false)
		h.SeedMeshResult(t, f.ID, status, true)
	}
	h.SeedMeshResult(t, other.ID, "pending", false)
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s application.DeletionStore) error {
		n, err := s.PendingMeshResultCount(ctx, f.ID)
		require.Equal(t, int64(3), n)
		return err
	}))
}

func deletionContractEmptyResults(t *testing.T, h DeletionHarness) {
	f := deletionContractFile(true)
	h.SeedFile(t, f)
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s application.DeletionStore) error {
		a, b, err := s.PendingTranscodeResultCounts(ctx, f.ID)
		require.NoError(t, err)
		require.Zero(t, a)
		require.Zero(t, b)
		n, err := s.PendingMeshResultCount(ctx, f.ID)
		require.Zero(t, n)
		return err
	}))
}
