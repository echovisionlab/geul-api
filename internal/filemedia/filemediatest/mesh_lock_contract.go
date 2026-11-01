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

func meshLockCases() map[string]func(*testing.T, CatalogHarness) {
	return map[string]func(*testing.T, CatalogHarness){
		"mesh candidate lock serializes the pending transition": func(t *testing.T, h CatalogHarness) {
			file := CatalogFile("mesh", nil)
			h.SeedFile(t, file, false, false)
			row := MeshCandidate(file.ID)
			seedMeshCandidate(t, h, row)
			at := row.CreatedAt.Add(time.Hour)
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, err := s.MeshCandidate(ctx, row.ID, "", true)
				return err
			}, func(ctx context.Context, s domain.Store) error {
				return s.MarkMeshProcessing(ctx, row.ID, at, at.Add(time.Hour))
			}, func(ctx context.Context, s domain.Store) error {
				return s.MarkMeshProcessing(ctx, row.ID, at.Add(time.Minute), at.Add(2*time.Hour))
			}))
			got := readMeshCandidate(t, h, row.ID)
			require.Equal(t, domain.MeshStatusProcessing, got.Status)
			require.True(t, at.Equal(*got.ProcessingStartedAt))
			require.True(t, at.Add(time.Hour).Equal(*got.ExpiresAt))
		},
		"mesh completion locks source and existing output including deletion intent": func(t *testing.T, h CatalogHarness) {
			file, output, pending := CatalogFile("mesh", nil), CatalogFile("output", nil), CatalogFile("pending", nil)
			h.SeedFile(t, file, false, false)
			h.SeedFile(t, output, false, true)
			h.SeedFile(t, pending, true, false)
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				accepts, err := s.LockMeshCompletionFiles(ctx, pending.ID, uuid.NewString())
				require.NoError(t, err)
				require.False(t, accepts)
				_, err = s.LockMeshCompletionFiles(ctx, uuid.NewString(), output.ID)
				require.ErrorIs(t, err, domain.ErrFileNotFound)
				return nil
			}))
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				accepts, err := s.LockMeshCompletionFiles(ctx, file.ID, output.ID)
				require.True(t, accepts)
				return err
			}, func(context.Context, domain.Store) error { return nil }, func(ctx context.Context, s domain.Store) error {
				changed, err := s.MarkFileDeletion(ctx, output.ID, time.Now().UTC())
				require.True(t, changed)
				return err
			}))
		},
	}
}
