package favicontest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/favicon"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func stagingCases() map[string]func(*testing.T, Harness) {
	return map[string]func(*testing.T, Harness){
		"source attachment requires an unbound ready favicon and rolls back": func(t *testing.T, h Harness) {
			file := uuid.NewString()
			h.SeedFile(t, file, "image/svg+xml")
			ready, unready, foreign := ContractAsset(time.Now()), ContractAsset(time.Now()), ContractAsset(time.Now())
			unready.Status = favicon.AssetAllocated
			unready.FileSize, unready.SHA256 = nil, nil
			foreign.Kind = "image"
			for _, a := range []favicon.DerivativeAsset{ready, unready, foreign} {
				h.SeedAsset(t, "", "", a)
			}
			abort := errors.New("owner rollback")
			require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s favicon.Store) error {
				attached, err := s.AttachSourceAsset(ctx, ready.ID, file, time.Now())
				require.NoError(t, err)
				require.True(t, attached)
				attached, err = s.AttachSourceAsset(ctx, ready.ID, file, time.Now())
				require.NoError(t, err)
				require.False(t, attached)
				for _, id := range []string{unready.ID, foreign.ID, uuid.NewString()} {
					attached, err = s.AttachSourceAsset(ctx, id, file, time.Now())
					require.NoError(t, err)
					require.False(t, attached)
				}
				return abort
			}), abort)
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s favicon.Store) error {
				attached, err := s.AttachSourceAsset(ctx, ready.ID, file, time.Now())
				require.NoError(t, err)
				require.True(t, attached)
				return nil
			}))
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s favicon.Store) error {
				a, found, err := s.NewestReadySourceAsset(ctx, file)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, ready.ID, a.ID)
				return nil
			}))
		},
		"staged cleanup only transitions listed ready or failed assets": func(t *testing.T, h Harness) {
			ready, failed, allocated, deleted, other := ContractAsset(time.Now()), ContractAsset(time.Now()), ContractAsset(time.Now()), ContractAsset(time.Now()), ContractAsset(time.Now())
			failed.Status = favicon.AssetFailed
			allocated.Status = favicon.AssetAllocated
			allocated.FileSize, allocated.SHA256 = nil, nil
			deleted.Status = favicon.AssetDeleted
			for _, a := range []favicon.DerivativeAsset{ready, failed, allocated, deleted, other} {
				h.SeedAsset(t, "", "", a)
			}
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s favicon.Store) error {
				return s.ScheduleStagedDeletion(ctx, []string{ready.ID, failed.ID, allocated.ID, deleted.ID, uuid.NewString()}, time.Now())
			}))
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s favicon.Store) error {
				for _, pair := range []struct{ id, status string }{{ready.ID, favicon.AssetDeletePending}, {failed.ID, favicon.AssetDeletePending}, {allocated.ID, favicon.AssetAllocated}, {deleted.ID, favicon.AssetDeleted}, {other.ID, favicon.AssetReady}} {
					a, found, err := s.LockAsset(ctx, pair.id)
					require.NoError(t, err)
					require.True(t, found)
					require.Equal(t, pair.status, a.Status)
				}
				return nil
			}))
		},
	}
}
