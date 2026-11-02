package filemediatest

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func ingestGeneration(file string) mediaasset.Generation {
	id := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Microsecond)
	return mediaasset.Generation{ID: id, FileID: file, Kind: "hls", ObjectPrefix: "media/" + file + "/hls/" + id, ManifestName: "master.m3u8", Status: mediaasset.GenerationAllocated, CreatedAt: now, UpdatedAt: now}
}

func ingestAsset(file string) mediaasset.Asset {
	id := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Microsecond)
	return mediaasset.Asset{ID: id, SourceFileID: &file, Kind: "thumbnail", ObjectKey: "asset/" + id + ".webp", Extension: "webp", MimeType: "image/webp", Disposition: "inline", Status: mediaasset.AssetAllocated, CreatedAt: now, UpdatedAt: now}
}

func completeIngestOutputs(t *testing.T, ctx context.Context, s domain.Store, g mediaasset.Generation, a mediaasset.Asset) {
	t.Helper()
	_, err := s.EnsureIngestGeneration(ctx, g)
	require.NoError(t, err)
	_, err = s.EnsureIngestAsset(ctx, a)
	require.NoError(t, err)
	require.NoError(t, s.CompleteIngestGeneration(ctx, "https://cdn.example.com", mediaasset.WrittenGeneration{GenerationID: g.ID, ManifestSHA256: bytes.Repeat([]byte{3}, 32), ObjectCount: 2, TotalSize: 128}))
	require.NoError(t, s.CompleteIngestAsset(ctx, "https://cdn.example.com", mediaasset.WrittenAsset{AssetID: a.ID, SHA256: bytes.Repeat([]byte{4}, 32), FileSize: 64}))
}

func ingestMediaCases() map[string]func(*testing.T, CatalogHarness) {
	return map[string]func(*testing.T, CatalogHarness){
		"ingest command commits every target without retaining caller pointers": ingestMediaContractCommandIsolation,
		"stable outputs retain ready metadata on allocation retry":              ingestMediaContractAllocationRetry,
		"client readiness requires ready outputs from the same File":            ingestMediaContractClientReadiness,
		"media outputs File receipt and command roll back together":             ingestMediaContractAtomicRollback,
		"client receipt update ignores deleting File":                           ingestMediaContractDeletingReceipt,
		"ingest File lock blocks deletion intent":                               ingestMediaContractFileLock,
	}
}

func ingestMediaContractCommandIsolation(t *testing.T, h CatalogHarness) {
	command := domain.IngestTranscodeCommand{Kind: domain.IngestTranscodeAudio, EventID: uuid.NewString(), FileID: uuid.NewString(), EntityID: uuid.NewString(), EntityType: 999, Source: &domain.ObjectTarget{FileID: "source", ObjectKey: "media/source.mp4", Extension: "mp4", MimeType: "audio/mp4"}, HLSOutput: &domain.GenerationTarget{GenerationID: "generation", FileID: "source", ObjectPrefix: "media/source/hls/generation"}, ArtifactOutput: &mediaasset.WriteTarget{AssetID: "asset", ObjectKey: "asset/asset.png", Extension: "png", MimeType: "image/png", Disposition: mediaasset.DispositionInline, DownloadFilename: new("source.png")}}
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error { return s.EnqueueIngestTranscode(ctx, command) }))
	got := h.IngestCommands(t)
	require.Equal(t, []domain.IngestTranscodeCommand{command}, got)
	command.Source.ObjectKey = "mutated"
	*command.ArtifactOutput.DownloadFilename = "mutated"
	again := h.IngestCommands(t)
	require.Equal(t, "media/source.mp4", again[0].Source.ObjectKey)
	require.Equal(t, new("source.png"), again[0].ArtifactOutput.DownloadFilename)
}

func ingestMediaContractAllocationRetry(t *testing.T, h CatalogHarness) {
	file := CatalogFile("source", nil)
	h.SeedFile(t, file, false, false)
	generation, asset := ingestGeneration(file.ID), ingestAsset(file.ID)
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
		require.NoError(t, s.LockIngestFile(ctx, file.ID))
		completeIngestOutputs(t, ctx, s, generation, asset)
		got, err := s.EnsureIngestGeneration(ctx, generation)
		require.NoError(t, err)
		require.Equal(t, mediaasset.GenerationReady, got.Status)
		require.Equal(t, new(int32(2)), got.ObjectCount)
		require.Equal(t, new(int64(128)), got.TotalSize)
		require.Equal(t, bytes.Repeat([]byte{3}, 32), got.ManifestSHA256)
		ready, err := s.EnsureIngestAsset(ctx, asset)
		require.NoError(t, err)
		require.Equal(t, mediaasset.AssetReady, ready.Status)
		require.Equal(t, new(int64(64)), ready.FileSize)
		require.Equal(t, bytes.Repeat([]byte{4}, 32), ready.SHA256)
		return nil
	}))
}

func ingestMediaContractClientReadiness(t *testing.T, h CatalogHarness) {
	file, other := CatalogFile("source", nil), CatalogFile("other", nil)
	h.SeedFile(t, file, false, false)
	h.SeedFile(t, other, false, false)
	generation, asset := ingestGeneration(file.ID), ingestAsset(file.ID)
	otherGen, otherAsset := ingestGeneration(other.ID), ingestAsset(other.ID)
	required := []string{"FILE_DERIVATIVE_TYPE_HLS", "FILE_DERIVATIVE_TYPE_THUMBNAIL"}
	bundle := uuid.NewString()
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
		require.NoError(t, s.LockIngestFile(ctx, file.ID))
		require.NoError(t, s.LockIngestFile(ctx, other.ID))
		completeIngestOutputs(t, ctx, s, generation, asset)
		completeIngestOutputs(t, ctx, s, otherGen, otherAsset)
		hls := domain.IngestDerivative{ID: uuid.NewString(), FileID: file.ID, Type: required[0], GenerationID: &generation.ID, CreatedAt: time.Now().UTC()}
		thumbnail := domain.IngestDerivative{ID: uuid.NewString(), FileID: file.ID, Type: required[1], AssetID: &asset.ID, CreatedAt: time.Now().UTC()}
		require.NoError(t, s.UpsertIngestDerivative(ctx, hls))
		require.NoError(t, s.UpsertIngestDerivative(ctx, thumbnail))
		require.NoError(t, s.UpsertIngestDerivative(ctx, thumbnail))
		count, err := s.ReadyIngestDerivativeCount(ctx, file.ID, required)
		require.NoError(t, err)
		require.Equal(t, int64(2), count)
		thumbnail.AssetID = &otherAsset.ID
		require.NoError(t, s.UpsertIngestDerivative(ctx, thumbnail))
		count, err = s.ReadyIngestDerivativeCount(ctx, file.ID, required)
		require.NoError(t, err)
		require.Equal(t, int64(1), count)
		allocated := ingestAsset(file.ID)
		_, err = s.EnsureIngestAsset(ctx, allocated)
		require.NoError(t, err)
		thumbnail.AssetID = &allocated.ID
		require.NoError(t, s.UpsertIngestDerivative(ctx, thumbnail))
		count, err = s.ReadyIngestDerivativeCount(ctx, file.ID, required)
		require.NoError(t, err)
		require.Equal(t, int64(1), count)
		require.NoError(t, s.SetClientMediaBundle(ctx, file.ID, bundle, 6))
		return nil
	}))
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
		got, err := s.IngestFile(ctx, file.ID)
		require.NoError(t, err)
		require.Equal(t, new(bundle), got.ClientMediaBundleID)
		require.Equal(t, new(6), got.DurationSeconds)
		return nil
	}))
}

func ingestMediaContractAtomicRollback(t *testing.T, h CatalogHarness) {
	file := CatalogFile("source", nil)
	h.SeedFile(t, file, false, false)
	generation, asset := ingestGeneration(file.ID), ingestAsset(file.ID)
	abort := errors.New("owner rollback")
	require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
		require.NoError(t, s.LockIngestFile(ctx, file.ID))
		completeIngestOutputs(t, ctx, s, generation, asset)
		require.NoError(t, s.UpsertIngestDerivative(ctx, domain.IngestDerivative{ID: uuid.NewString(), FileID: file.ID, Type: "FILE_DERIVATIVE_TYPE_HLS", GenerationID: &generation.ID, CreatedAt: time.Now().UTC()}))
		require.NoError(t, s.SetClientMediaBundle(ctx, file.ID, uuid.NewString(), 6))
		require.NoError(t, s.EnqueueIngestTranscode(ctx, domain.IngestTranscodeCommand{Kind: domain.IngestTranscodeAudio, EventID: uuid.NewString(), FileID: file.ID}))
		return abort
	}), abort)
	require.Empty(t, h.IngestCommands(t))
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
		got, err := s.IngestFile(ctx, file.ID)
		require.NoError(t, err)
		require.Nil(t, got.ClientMediaBundleID)
		rows, err := s.StoredDerivativeDeliveries(ctx, []string{file.ID})
		require.NoError(t, err)
		require.Empty(t, rows)
		generations, err := s.DeletionGenerationRefs(ctx, file.ID)
		require.NoError(t, err)
		require.Empty(t, generations)
		_, err = s.PromotionAsset(ctx, asset.ID, false)
		require.ErrorIs(t, err, mediaasset.ErrAssetNotFound)
		return nil
	}))
}

func ingestMediaContractDeletingReceipt(t *testing.T, h CatalogHarness) {
	file := CatalogFile("pending", nil)
	h.SeedFile(t, file, true, false)
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
		return s.SetClientMediaBundle(ctx, file.ID, uuid.NewString(), 6)
	}))
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
		got, err := s.IngestFile(ctx, file.ID)
		require.NoError(t, err)
		require.Nil(t, got.ClientMediaBundleID)
		require.Equal(t, int(*file.DurationSeconds), *got.DurationSeconds)
		return nil
	}))
}

func ingestMediaContractFileLock(t *testing.T, h CatalogHarness) {
	file := CatalogFile("source", nil)
	h.SeedFile(t, file, false, false)
	require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s domain.Store) error { return s.LockIngestFile(ctx, file.ID) }, func(context.Context, domain.Store) error { return nil }, func(ctx context.Context, s domain.Store) error {
		changed, err := s.MarkFileDeletion(ctx, file.ID, time.Now().UTC())
		require.True(t, changed)
		return err
	}))
}
