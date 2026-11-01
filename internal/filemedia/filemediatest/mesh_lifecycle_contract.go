package filemediatest

import (
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func meshLifecycleCases() map[string]func(*testing.T, CatalogHarness) {
	return map[string]func(*testing.T, CatalogHarness){
		"mesh processing transition only accepts pending candidates": func(t *testing.T, h CatalogHarness) {
			file := CatalogFile("mesh", nil)
			h.SeedFile(t, file, false, false)
			for _, status := range []string{domain.MeshStatusPending, domain.MeshStatusProcessing, domain.MeshStatusReady, domain.MeshStatusFailed, domain.MeshStatusCancelled} {
				row := MeshCandidate(file.ID)
				row.Status = status
				seedMeshCandidate(t, h, row)
				at := row.CreatedAt.Add(time.Minute)
				require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
					return s.MarkMeshProcessing(ctx, row.ID, at, at.Add(time.Hour))
				}))
				got := readMeshCandidate(t, h, row.ID)
				if status == domain.MeshStatusPending {
					require.Equal(t, domain.MeshStatusProcessing, got.Status)
					require.True(t, at.Equal(*got.ProcessingStartedAt))
					require.True(t, at.Add(time.Hour).Equal(*got.ExpiresAt))
				} else {
					require.Equal(t, status, got.Status)
					require.Nil(t, got.ProcessingStartedAt)
					require.True(t, row.UpdatedAt.Equal(got.UpdatedAt))
				}
			}
		},
		"mesh requeue resets job state and retains metrics and omitted entity targets": func(t *testing.T, h CatalogHarness) {
			file, output := CatalogFile("mesh", nil), CatalogFile("output", nil)
			h.SeedFile(t, file, false, false)
			h.SeedFile(t, output, false, true)
			row := MeshCandidate(file.ID)
			row.Status = domain.MeshStatusFailed
			row.OutputFileID = &output.ID
			row.EntityType = new("TRANSCODE_ENTITY_TYPE_PAGE")
			row.EntityID = new(uuid.NewString())
			row.ErrorMessage = new("previous failure")
			row.SelectedAt = &row.CreatedAt
			row.ProcessingStartedAt = &row.CreatedAt
			row.CompletedAt = &row.CreatedAt
			row.FailedAt = &row.CreatedAt
			row.CancelledAt = &row.CreatedAt
			row.OriginalFileSize = new(int64(123))
			row.OptimizedVertexes = new(int64(12))
			seedMeshCandidate(t, h, row)
			at := row.CreatedAt.Add(time.Hour)
			input := domain.MeshRequeue{OutputObjectID: uuid.NewString(), JobID: uuid.NewString(), At: at, ExpiresAt: at.Add(time.Hour)}
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error { return s.RequeueMeshCandidate(ctx, row.ID, input) }))
			got := readMeshCandidate(t, h, row.ID)
			require.Equal(t, domain.MeshStatusPending, got.Status)
			require.Equal(t, input.OutputObjectID, got.OutputObjectID)
			require.Equal(t, new(input.JobID), got.JobID)
			require.Nil(t, got.OutputFileID)
			require.Nil(t, got.ErrorMessage)
			require.Nil(t, got.SelectedAt)
			require.Nil(t, got.ProcessingStartedAt)
			require.Nil(t, got.CompletedAt)
			require.Nil(t, got.FailedAt)
			require.Nil(t, got.CancelledAt)
			require.Equal(t, row.EntityType, got.EntityType)
			require.Equal(t, row.EntityID, got.EntityID)
			require.Equal(t, row.OriginalFileSize, got.OriginalFileSize)
			require.Equal(t, row.OptimizedVertexes, got.OptimizedVertexes)
			require.True(t, at.Equal(*got.EnqueuedAt))
			require.True(t, input.ExpiresAt.Equal(*got.ExpiresAt))
			input.EntityType = new("TRANSCODE_ENTITY_TYPE_WORK")
			input.EntityID = new(uuid.NewString())
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error { return s.RequeueMeshCandidate(ctx, row.ID, input) }))
			got = readMeshCandidate(t, h, row.ID)
			require.Equal(t, input.EntityType, got.EntityType)
			require.Equal(t, input.EntityID, got.EntityID)
		},
		"mesh completion writes metrics and clears failure only for ready results": func(t *testing.T, h CatalogHarness) {
			file, output := CatalogFile("mesh", nil), CatalogFile("output", nil)
			h.SeedFile(t, file, false, false)
			h.SeedFile(t, output, false, true)
			for _, status := range []string{domain.MeshStatusReady, domain.MeshStatusCancelled} {
				row := MeshCandidate(file.ID)
				row.FailedAt = &row.CreatedAt
				row.CancelledAt = &row.CreatedAt
				row.ErrorMessage = new("old failure")
				seedMeshCandidate(t, h, row)
				at := row.CreatedAt.Add(time.Hour)
				v := domain.MeshCompletion{OutputFileID: output.ID, Status: status, OptimizedFileSize: new(int64(64)), OriginalVertexes: new(int64(123)), OptimizedVertexes: new(int64(12)), OriginalTriangles: new(int64(90)), OptimizedTriangles: new(int64(9)), CompletedAt: at, ExpiresAt: at.Add(time.Hour)}
				require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error { return s.CompleteMeshCandidate(ctx, row.ID, v) }))
				got := readMeshCandidate(t, h, row.ID)
				require.Equal(t, status, got.Status)
				require.Equal(t, new(output.ID), got.OutputFileID)
				require.Equal(t, v.OptimizedFileSize, got.OptimizedFileSize)
				require.Equal(t, v.OriginalVertexes, got.OriginalVertexes)
				require.Equal(t, v.OptimizedTriangles, got.OptimizedTriangles)
				require.Nil(t, got.OriginalFileSize)
				require.Nil(t, got.ProcessingTimeMs)
				require.Nil(t, got.ErrorMessage)
				require.True(t, at.Equal(*got.CompletedAt))
				require.True(t, v.ExpiresAt.Equal(*got.ExpiresAt))
				if status == domain.MeshStatusReady {
					require.Nil(t, got.FailedAt)
					require.Nil(t, got.CancelledAt)
				} else {
					require.True(t, row.FailedAt.Equal(*got.FailedAt))
					require.True(t, at.Equal(*got.CancelledAt))
				}
			}
		},
		"mesh selection cancellation and retirement preserve their distinct fields": func(t *testing.T, h CatalogHarness) {
			file, output := CatalogFile("mesh", nil), CatalogFile("output", nil)
			h.SeedFile(t, file, false, false)
			h.SeedFile(t, output, false, true)
			row := MeshCandidate(file.ID)
			row.OutputFileID = &output.ID
			seedMeshCandidate(t, h, row)
			at := row.CreatedAt.Add(time.Hour)
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error { return s.SelectMeshCandidate(ctx, row.ID, at) }))
			got := readMeshCandidate(t, h, row.ID)
			require.True(t, at.Equal(*got.SelectedAt))
			require.Nil(t, got.ExpiresAt)
			require.Equal(t, row.Status, got.Status)
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				return s.CancelMeshCandidate(ctx, row.ID, at, at.Add(time.Hour))
			}))
			got = readMeshCandidate(t, h, row.ID)
			require.Equal(t, domain.MeshStatusCancelled, got.Status)
			require.Equal(t, row.OutputFileID, got.OutputFileID)
			require.True(t, at.Add(time.Hour).Equal(*got.ExpiresAt))
			require.True(t, at.Equal(*got.SelectedAt))
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				return s.RetireMeshCandidate(ctx, row.ID, at.Add(time.Minute))
			}))
			got = readMeshCandidate(t, h, row.ID)
			require.Nil(t, got.OutputFileID)
			require.Equal(t, row.OutputObjectID, got.OutputObjectID)
			require.True(t, at.Add(time.Hour).Equal(*got.ExpiresAt))
			require.True(t, at.Add(time.Minute).Equal(*got.CancelledAt))
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error { return s.DeleteMeshCandidate(ctx, row.ID) }))
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, err := s.MeshCandidate(ctx, row.ID, "", false)
				require.ErrorIs(t, err, domain.ErrMeshCandidateNotFound)
				_, err = s.IngestFile(ctx, output.ID)
				return err
			}))
		},
		"mesh failure writes text timestamps and expiry without clearing output": func(t *testing.T, h CatalogHarness) {
			file, output := CatalogFile("mesh", nil), CatalogFile("output", nil)
			h.SeedFile(t, file, false, false)
			h.SeedFile(t, output, false, true)
			row := MeshCandidate(file.ID)
			row.OutputFileID = &output.ID
			seedMeshCandidate(t, h, row)
			at := row.CreatedAt.Add(time.Hour)
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				return s.FailMeshCandidate(ctx, row.ID, "rejected", at, at.Add(time.Hour))
			}))
			got := readMeshCandidate(t, h, row.ID)
			require.Equal(t, domain.MeshStatusFailed, got.Status)
			require.Equal(t, new("rejected"), got.ErrorMessage)
			require.Equal(t, row.OutputFileID, got.OutputFileID)
			require.True(t, at.Equal(*got.FailedAt))
			require.True(t, at.Add(time.Hour).Equal(*got.ExpiresAt))
		},
		"mesh expiry excludes selected null expiry and exact cutoff": func(t *testing.T, h CatalogHarness) {
			file := CatalogFile("mesh", nil)
			h.SeedFile(t, file, false, false)
			now := time.Now().UTC().Truncate(time.Microsecond)
			expired, selected, none, edge := MeshCandidate(file.ID), MeshCandidate(file.ID), MeshCandidate(file.ID), MeshCandidate(file.ID)
			expired.ExpiresAt = new(now.Add(-time.Second))
			selected.ExpiresAt = expired.ExpiresAt
			selected.SelectedAt = &now
			none.ExpiresAt = nil
			edge.ExpiresAt = &now
			for _, row := range []domain.MeshCandidate{expired, selected, none, edge} {
				seedMeshCandidate(t, h, row)
			}
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				rows, err := s.ExpiredMeshCandidates(ctx, now)
				require.NoError(t, err)
				require.Len(t, rows, 1)
				require.Equal(t, expired.ID, rows[0].ID)
				return nil
			}))
		},
	}
}
