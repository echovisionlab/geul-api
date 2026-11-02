package filemediatest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func MeshCandidate(file string) domain.MeshCandidate {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return domain.MeshCandidate{ID: uuid.NewString(), SourceFileID: file, OutputObjectID: uuid.NewString(), TargetRatioPercent: 50, Method: domain.MeshMethodDraco, PipelineVersion: domain.MeshPipelineDracoWebpV1, CacheKey: uuid.NewString(), Status: domain.MeshStatusPending, JobID: new(uuid.NewString()), EnqueuedAt: &now, ExpiresAt: new(now.Add(7 * 24 * time.Hour)), CreatedAt: now, UpdatedAt: now}
}
func seedMeshCandidate(t *testing.T, h CatalogHarness, row domain.MeshCandidate) {
	t.Helper()
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error { return s.InsertMeshCandidate(ctx, row) }))
}
func readMeshCandidate(t *testing.T, h CatalogHarness, id string) domain.MeshCandidate {
	t.Helper()
	var row domain.MeshCandidate
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
		var err error
		row, err = s.MeshCandidate(ctx, id, "", false)
		return err
	}))
	return row
}

func meshCases() map[string]func(*testing.T, CatalogHarness) {
	cases := map[string]func(*testing.T, CatalogHarness){
		"mesh lookup filters pipeline source cache key and exact job":                         meshContractLookupFilters,
		"mesh insertion rejects duplicate identity and cache key without changing the winner": meshContractDuplicateIdentity,
		"mesh candidate and queued command roll back together":                                meshContractCommandRollback,
		"mesh source reads reject deletion and require the exact content owner":               meshContractSourceOwnership,
		"mesh output insertion preserves an existing File and rolls back a new one":           meshContractOutputInsertion,
	}
	for name, run := range meshLifecycleCases() {
		cases[name] = run
	}
	for name, run := range meshLockCases() {
		cases[name] = run
	}
	return cases
}

func meshContractLookupFilters(t *testing.T, h CatalogHarness) {
	file, other := CatalogFile("mesh", nil), CatalogFile("other", nil)
	h.SeedFile(t, file, false, false)
	h.SeedFile(t, other, false, false)
	a, b, legacy, foreign := MeshCandidate(file.ID), MeshCandidate(file.ID), MeshCandidate(file.ID), MeshCandidate(other.ID)
	a.TargetRatioPercent = 10
	b.TargetRatioPercent = 90
	legacy.PipelineVersion = domain.MeshPipelineDracoV1
	a.EntityType = new("TRANSCODE_ENTITY_TYPE_WORK")
	a.EntityID = new(uuid.NewString())
	a.OriginalFileSize = new(int64(123))
	for _, row := range []domain.MeshCandidate{b, a, legacy, foreign} {
		seedMeshCandidate(t, h, row)
	}
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
		rows, err := s.MeshCandidates(ctx, file.ID, domain.MeshPipelineDracoWebpV1)
		require.NoError(t, err)
		require.Equal(t, []string{a.ID, b.ID}, []string{rows[0].ID, rows[1].ID})
		require.Len(t, rows, 2)
		got, err := s.MeshCandidateByCacheKey(ctx, a.CacheKey)
		require.NoError(t, err)
		require.Equal(t, a.EntityType, got.EntityType)
		require.Equal(t, a.OriginalFileSize, got.OriginalFileSize)
		require.True(t, a.CreatedAt.Equal(got.CreatedAt))
		*got.EntityType = "mutated"
		*got.OriginalFileSize = 999
		got, err = s.MeshCandidate(ctx, a.ID, *a.JobID, false)
		require.NoError(t, err)
		require.Equal(t, a.EntityType, got.EntityType)
		require.Equal(t, a.OriginalFileSize, got.OriginalFileSize)
		_, err = s.MeshCandidate(ctx, a.ID, uuid.NewString(), false)
		require.ErrorIs(t, err, domain.ErrMeshCandidateNotFound)
		_, err = s.MeshCandidate(ctx, uuid.NewString(), "", false)
		require.ErrorIs(t, err, domain.ErrMeshCandidateNotFound)
		_, err = s.MeshCandidateByCacheKey(ctx, uuid.NewString())
		require.ErrorIs(t, err, domain.ErrMeshCandidateNotFound)
		return nil
	}))
}

func meshContractDuplicateIdentity(t *testing.T, h CatalogHarness) {
	file := CatalogFile("mesh", nil)
	h.SeedFile(t, file, false, false)
	row := MeshCandidate(file.ID)
	seedMeshCandidate(t, h, row)
	for _, sameID := range []bool{true, false} {
		candidate := row
		if !sameID {
			candidate.ID = uuid.NewString()
		}
		require.Error(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error { return s.InsertMeshCandidate(ctx, candidate) }))
	}
	got := readMeshCandidate(t, h, row.ID)
	require.Equal(t, row.CacheKey, got.CacheKey)
	require.Equal(t, row.OutputObjectID, got.OutputObjectID)
}

func meshContractCommandRollback(t *testing.T, h CatalogHarness) {
	file := CatalogFile("mesh", nil)
	h.SeedFile(t, file, false, false)
	row := MeshCandidate(file.ID)
	abort := errors.New("owner rollback")
	command := domain.MeshJob{JobID: *row.JobID, CandidateID: row.ID, SourceFileID: file.ID, EntityID: uuid.NewString(), EntityType: 999, CompressionMethod: 998, Profile: 997, TargetRatioPercent: 42, Source: &domain.ObjectTarget{FileID: file.ID, ObjectKey: "media/" + file.ID + ".glb", Extension: "glb", MimeType: "model/gltf-binary"}, Output: &domain.ObjectTarget{FileID: row.OutputObjectID, ObjectKey: "media/" + row.OutputObjectID + ".glb", Extension: "glb", MimeType: "model/gltf-binary"}, SlotID: new("slot"), AttemptID: new("attempt"), TimestampMS: 123}
	require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
		require.NoError(t, s.InsertMeshCandidate(ctx, row))
		require.NoError(t, s.EnqueueMeshJob(ctx, command))
		return abort
	}), abort)
	require.Empty(t, h.MeshJobs(t))
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
		_, err := s.MeshCandidate(ctx, row.ID, "", false)
		require.ErrorIs(t, err, domain.ErrMeshCandidateNotFound)
		return nil
	}))
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
		require.NoError(t, s.InsertMeshCandidate(ctx, row))
		return s.EnqueueMeshJob(ctx, command)
	}))
	require.Equal(t, []domain.MeshJob{command}, h.MeshJobs(t))
	command.Source.ObjectKey = "mutated"
	*command.SlotID = "mutated"
	require.Equal(t, "media/"+file.ID+".glb", h.MeshJobs(t)[0].Source.ObjectKey)
	require.Equal(t, new("slot"), h.MeshJobs(t)[0].SlotID)
}

func meshContractSourceOwnership(t *testing.T, h CatalogHarness) {
	file, pending := CatalogFile("mesh", nil), CatalogFile("pending", nil)
	file.Extension = new("glb")
	file.MimeType = new("model/gltf-binary")
	h.SeedFile(t, file, false, false)
	h.SeedFile(t, pending, true, false)
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
		got, err := s.MeshSourceFile(ctx, file.ID)
		require.NoError(t, err)
		require.Equal(t, file.ID, got.ID)
		require.Equal(t, "glb", got.Extension)
		_, err = s.MeshSourceFile(ctx, pending.ID)
		require.ErrorIs(t, err, domain.ErrFileNotFound)
		_, err = s.MeshSourceFile(ctx, uuid.NewString())
		require.ErrorIs(t, err, domain.ErrFileNotFound)
		return nil
	}))
	for _, kind := range []string{"post", "page", "work", "program_event"} {
		id, doc := uuid.NewString(), uuid.NewString()
		h.SeedScoped(t, kind, id, ScopedOwner{DocumentID: doc}, domain.ScopedAttachment{BlockID: uuid.NewString(), ReferencePath: "file", FileID: file.ID})
		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
			linked, err := s.MeshSourceLinked(ctx, kind, id, file.ID)
			require.NoError(t, err)
			require.True(t, linked)
			linked, err = s.MeshSourceLinked(ctx, kind, id, pending.ID)
			require.NoError(t, err)
			require.False(t, linked)
			linked, err = s.MeshSourceLinked(ctx, kind, uuid.NewString(), file.ID)
			require.NoError(t, err)
			require.False(t, linked)
			return nil
		}))
	}
}

func meshContractOutputInsertion(t *testing.T, h CatalogHarness) {
	id := uuid.NewString()
	at := time.Now().UTC().Truncate(time.Microsecond)
	file := domain.File{ID: id, FileName: id, Extension: "glb", MimeType: "model/gltf-binary", FileSize: 64, SHA256: make([]byte, 32), CreatedAt: at}
	abort := errors.New("owner rollback")
	require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
		created, err := s.InsertMeshOutputFile(ctx, file)
		require.NoError(t, err)
		require.True(t, created)
		return abort
	}), abort)
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
		created, err := s.InsertMeshOutputFile(ctx, file)
		require.NoError(t, err)
		require.True(t, created)
		changed := file
		changed.FileSize = 128
		created, err = s.InsertMeshOutputFile(ctx, changed)
		require.NoError(t, err)
		require.False(t, created)
		got, err := s.IngestFile(ctx, id)
		require.NoError(t, err)
		require.Equal(t, int64(64), got.FileSize)
		require.Equal(t, file.SHA256, got.SHA256)
		require.True(t, at.Equal(got.CreatedAt))
		return nil
	}))
}
