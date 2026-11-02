package mediaassettest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/stretchr/testify/require"
)

func allocationContractCases() map[string]func(*testing.T, Harness) {
	return map[string]func(*testing.T, Harness){
		"stable asset allocation preserves existing ready metadata": func(t *testing.T, h Harness) {
			asset := ReadyAsset(time.Now().UTC().Truncate(time.Microsecond))
			insertAsset(t, h, asset)
			candidate := asset
			candidate.Status = mediaasset.AssetAllocated
			candidate.FileSize, candidate.SHA256, candidate.ReadyAt = nil, nil, nil
			candidate.CreatedAt = candidate.CreatedAt.Add(time.Hour)
			candidate.UpdatedAt = candidate.CreatedAt
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error {
				got, err := s.EnsureAssetAllocation(ctx, candidate)
				require.NoError(t, err)
				require.Equal(t, asset.Status, got.Status)
				require.Equal(t, asset.FileSize, got.FileSize)
				require.Equal(t, asset.SHA256, got.SHA256)
				require.True(t, asset.CreatedAt.Equal(got.CreatedAt))
				require.True(t, asset.ReadyAt.Equal(*got.ReadyAt))
				return nil
			}))
		},
		"stable asset allocation reuses identity and rolls back": func(t *testing.T, h Harness) {
			asset := AllocatedAsset(time.Now().UTC().Truncate(time.Microsecond))
			file := h.SeedFile(t, nil)
			asset.SourceFileID = &file
			abort := errors.New("owner rollback")
			require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error {
				got, err := s.EnsureAssetAllocation(ctx, asset)
				require.NoError(t, err)
				require.Equal(t, asset.ID, got.ID)
				got, err = s.EnsureAssetAllocation(ctx, asset)
				require.NoError(t, err)
				require.Equal(t, asset.ID, got.ID)
				return abort
			}), abort)
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error {
				_, err := s.Asset(ctx, asset.ID)
				require.ErrorIs(t, err, mediaasset.ErrAssetNotFound)
				return nil
			}))
		},
	}
}
