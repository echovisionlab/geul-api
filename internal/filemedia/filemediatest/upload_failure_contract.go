package filemediatest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/stretchr/testify/require"
)

func uploadFailureCases() map[string]func(*testing.T, UploadHarness) {
	cases := map[string]func(*testing.T, UploadHarness){
		"any persisted session retains File completion authority": func(t *testing.T, h UploadHarness) {
			for _, status := range []domain.UploadSessionStatus{domain.UploadSessionStatusInitiated, domain.UploadSessionStatusUploading, domain.UploadSessionStatusFinalizing, domain.UploadSessionStatusFailed, domain.UploadSessionStatusAborted} {
				row := contractUpload()
				row.Status = status
				h.Seed(t, row, nil)
				require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
					exists, err := s.HasFileUploadSessions(ctx, row.FileID)
					require.NoError(t, err)
					require.True(t, exists)
					exists, err = s.HasFileUploadSessions(ctx, row.UploadID)
					require.NoError(t, err)
					require.False(t, exists)
					return nil
				}))
			}
		},
		"completion failure rolls back with owner": func(t *testing.T, h UploadHarness) {
			row := contractUpload()
			row.Status = domain.UploadSessionStatusFinalizing
			h.Seed(t, row, nil)
			abort := errors.New("owner rollback")
			require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				require.NoError(t, s.FailFinalizingUpload(ctx, row.UploadID, row.CreatedAt.Add(time.Minute)))
				return abort
			}), abort)
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.UploadSession(ctx, row.UploadID, row.FileID, false)
				require.NoError(t, err)
				require.Equal(t, row.Status, got.Status)
				require.True(t, row.LastActivityAt.Equal(got.LastActivityAt))
				return nil
			}))
		},
		"completion failure waits for session lock": func(t *testing.T, h UploadHarness) {
			row := contractUpload()
			row.Status = domain.UploadSessionStatusFinalizing
			h.Seed(t, row, nil)
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, err := s.UploadSession(ctx, row.UploadID, row.FileID, true)
				return err
			}, func(context.Context, domain.Store) error { return nil }, func(ctx context.Context, s domain.Store) error {
				return s.FailFinalizingUpload(ctx, row.UploadID, time.Now())
			}))
		},
	}
	for _, status := range []domain.UploadSessionStatus{domain.UploadSessionStatusInitiated, domain.UploadSessionStatusUploading, domain.UploadSessionStatusFinalizing, domain.UploadSessionStatusFailed, domain.UploadSessionStatusAborted} {
		cases["completion failure guard "+string(status)] = func(t *testing.T, h UploadHarness) {
			row := contractUpload()
			row.Status = status
			h.Seed(t, row, nil)
			now := row.CreatedAt.Add(time.Minute)
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error { return s.FailFinalizingUpload(ctx, row.UploadID, now) }))
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.UploadSession(ctx, row.UploadID, row.FileID, false)
				require.NoError(t, err)
				if status == domain.UploadSessionStatusFinalizing {
					require.Equal(t, domain.UploadSessionStatusFailed, got.Status)
					require.True(t, now.Equal(got.LastActivityAt))
					require.True(t, now.Equal(got.UpdatedAt))
				} else {
					require.Equal(t, status, got.Status)
					require.True(t, row.LastActivityAt.Equal(got.LastActivityAt))
				}
				return nil
			}))
		}
	}
	return cases
}
