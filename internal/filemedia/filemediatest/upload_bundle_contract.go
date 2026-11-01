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

func uploadBundleCases() map[string]func(*testing.T, UploadHarness) {
	cases := map[string]func(*testing.T, UploadHarness){
		"bundle preparation rolls back with owner": func(t *testing.T, h UploadHarness) {
			row := contractUpload()
			h.Seed(t, row, nil)
			abort := errors.New("owner rollback")
			require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				changed, err := s.PrepareUploadBundle(ctx, row.UploadID, row.FileID, uuid.NewString(), `{"version":1}`, row.CreatedAt.Add(time.Minute))
				require.NoError(t, err)
				require.True(t, changed)
				return abort
			}), abort)
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.UploadSession(ctx, row.UploadID, row.FileID, false)
				require.NoError(t, err)
				require.Nil(t, got.ClientMediaBundleID)
				require.Nil(t, got.ClientMediaManifest)
				require.True(t, row.LastActivityAt.Equal(got.LastActivityAt))
				return nil
			}))
		},
		"invalid manifest cannot reserve bundle identity": func(t *testing.T, h UploadHarness) {
			row := contractUpload()
			h.Seed(t, row, nil)
			require.Error(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, err := s.PrepareUploadBundle(ctx, row.UploadID, row.FileID, uuid.NewString(), "{", row.CreatedAt.Add(time.Minute))
				return err
			}))
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.UploadSession(ctx, row.UploadID, row.FileID, false)
				require.NoError(t, err)
				require.Nil(t, got.ClientMediaBundleID)
				require.Nil(t, got.ClientMediaManifest)
				return nil
			}))
		},
		"bundle preparation waits for session lock": func(t *testing.T, h UploadHarness) {
			row := contractUpload()
			h.Seed(t, row, nil)
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, err := s.UploadSession(ctx, row.UploadID, row.FileID, true)
				return err
			}, func(context.Context, domain.Store) error { return nil }, func(ctx context.Context, s domain.Store) error {
				changed, err := s.PrepareUploadBundle(ctx, row.UploadID, row.FileID, uuid.NewString(), `{"version":1}`, time.Now().UTC())
				require.True(t, changed)
				return err
			}))
		},
	}
	for _, status := range []domain.UploadSessionStatus{domain.UploadSessionStatusInitiated, domain.UploadSessionStatusUploading, domain.UploadSessionStatusFinalizing, domain.UploadSessionStatusFailed, domain.UploadSessionStatusAborted} {
		cases["bundle preparation guard "+string(status)] = func(t *testing.T, h UploadHarness) {
			row := contractUpload()
			row.Status = status
			h.Seed(t, row, nil)
			now, bundle, manifest := row.CreatedAt.Add(time.Minute), uuid.NewString(), `{"version":1}`
			writable := status == domain.UploadSessionStatusInitiated || status == domain.UploadSessionStatusUploading
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				changed, err := s.PrepareUploadBundle(ctx, row.UploadID, uuid.NewString(), bundle, manifest, now)
				require.NoError(t, err)
				require.False(t, changed)
				changed, err = s.PrepareUploadBundle(ctx, row.UploadID, row.FileID, bundle, manifest, now)
				require.NoError(t, err)
				require.Equal(t, writable, changed)
				changed, err = s.PrepareUploadBundle(ctx, row.UploadID, row.FileID, uuid.NewString(), `{"version":2}`, now.Add(time.Hour))
				require.NoError(t, err)
				require.False(t, changed)
				return nil
			}))
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.UploadSession(ctx, row.UploadID, row.FileID, false)
				require.NoError(t, err)
				require.Equal(t, status, got.Status)
				if writable {
					require.Equal(t, new(bundle), got.ClientMediaBundleID)
					require.JSONEq(t, manifest, *got.ClientMediaManifest)
					require.True(t, now.Equal(got.LastActivityAt))
				} else {
					require.Nil(t, got.ClientMediaBundleID)
					require.Nil(t, got.ClientMediaManifest)
					require.True(t, row.LastActivityAt.Equal(got.LastActivityAt))
				}
				return nil
			}))
		}
	}
	return cases
}
