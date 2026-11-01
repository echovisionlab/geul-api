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

func uploadPartCases() map[string]func(*testing.T, UploadHarness) {
	cases := map[string]func(*testing.T, UploadHarness){
		"uploaded byte totals follow replacement and owner rollback": func(t *testing.T, h UploadHarness) {
			row := contractUpload()
			h.Seed(t, row, []domain.UploadPart{{UploadID: row.UploadID, PartNumber: 1, ETag: "first", Size: 3, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, {UploadID: row.UploadID, PartNumber: 2, ETag: "second", Size: 7, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}})
			abort := errors.New("owner rollback")
			require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.UploadedPartBytes(ctx, row.UploadID)
				require.NoError(t, err)
				require.Equal(t, int64(10), got)
				require.NoError(t, s.UpsertUploadPart(ctx, domain.UploadPart{UploadID: row.UploadID, PartNumber: 1, ETag: "empty", Size: 0, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}))
				got, err = s.UploadedPartBytes(ctx, row.UploadID)
				require.NoError(t, err)
				require.Equal(t, int64(7), got)
				require.NoError(t, s.DeleteUploadPart(ctx, row.UploadID, 2))
				got, err = s.UploadedPartBytes(ctx, row.UploadID)
				require.NoError(t, err)
				require.Zero(t, got)
				return abort
			}), abort)
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.UploadedPartBytes(ctx, row.UploadID)
				require.NoError(t, err)
				require.Equal(t, int64(10), got)
				got, err = s.UploadedPartBytes(ctx, uuid.NewString())
				require.NoError(t, err)
				require.Zero(t, got)
				return nil
			}))
		},
		"part replay replaces receipt and preserves creation time": func(t *testing.T, h UploadHarness) {
			row := contractUpload()
			old := domain.UploadPart{UploadID: row.UploadID, PartNumber: 1, ETag: "old", Size: 3, CreatedAt: row.CreatedAt, UpdatedAt: row.CreatedAt}
			h.Seed(t, row, []domain.UploadPart{old})
			now := row.CreatedAt.Add(time.Minute)
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				changed, err := s.MarkUploadPartUploading(ctx, row.UploadID, row.FileID, now)
				require.NoError(t, err)
				require.True(t, changed)
				return s.UpsertUploadPart(ctx, domain.UploadPart{UploadID: row.UploadID, PartNumber: 1, ETag: "replacement", Size: 6, CreatedAt: now, UpdatedAt: now})
			}))
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.UploadSession(ctx, row.UploadID, row.FileID, false)
				require.NoError(t, err)
				require.Equal(t, domain.UploadSessionStatusUploading, got.Status)
				require.True(t, now.Equal(got.LastActivityAt))
				parts, err := s.UploadParts(ctx, row.UploadID)
				require.NoError(t, err)
				require.Len(t, parts, 1)
				require.Equal(t, "replacement", parts[0].ETag)
				require.Equal(t, int64(6), parts[0].Size)
				require.True(t, row.CreatedAt.Equal(parts[0].CreatedAt))
				require.True(t, now.Equal(parts[0].UpdatedAt))
				return nil
			}))
		},
		"first MIME wins without renewing upload activity": func(t *testing.T, h UploadHarness) {
			row := contractUpload()
			h.Seed(t, row, nil)
			now := row.CreatedAt.Add(time.Minute)
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				changed, err := s.DetectUploadMIME(ctx, row.UploadID, row.FileID, "image/webp", now)
				require.NoError(t, err)
				require.True(t, changed)
				changed, err = s.DetectUploadMIME(ctx, row.UploadID, row.FileID, "image/png", now.Add(time.Minute))
				require.NoError(t, err)
				require.False(t, changed)
				state, err := s.UploadMIMEState(ctx, row.UploadID, row.FileID)
				require.NoError(t, err)
				require.Equal(t, new("image/webp"), state.DetectedMime)
				got, err := s.UploadSession(ctx, row.UploadID, row.FileID, false)
				require.NoError(t, err)
				require.True(t, now.Equal(*got.VerifiedAt))
				require.True(t, now.Equal(got.UpdatedAt))
				require.True(t, row.LastActivityAt.Equal(got.LastActivityAt))
				_, err = s.UploadMIMEState(ctx, row.UploadID, uuid.NewString())
				require.ErrorIs(t, err, domain.ErrUploadSessionNotFound)
				return nil
			}))
		},
		"part cache and activity rollback together": func(t *testing.T, h UploadHarness) {
			row := contractUpload()
			old := domain.UploadPart{UploadID: row.UploadID, PartNumber: 2, ETag: "cached", Size: 6, CreatedAt: row.CreatedAt, UpdatedAt: row.CreatedAt}
			h.Seed(t, row, []domain.UploadPart{old})
			abort := errors.New("abort reconciliation")
			require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				require.NoError(t, s.ReplaceUploadParts(ctx, row.UploadID, []domain.UploadPart{{UploadID: row.UploadID, PartNumber: 1, ETag: "remote", Size: 6}}))
				changed, err := s.RenewActiveUploadActivity(ctx, row.UploadID, row.FileID, true, row.CreatedAt.Add(time.Hour))
				require.NoError(t, err)
				require.True(t, changed)
				return abort
			}), abort)
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.UploadSession(ctx, row.UploadID, row.FileID, false)
				require.NoError(t, err)
				require.True(t, row.LastActivityAt.Equal(got.LastActivityAt))
				parts, err := s.UploadParts(ctx, row.UploadID)
				require.NoError(t, err)
				require.Len(t, parts, 1)
				require.Equal(t, old.PartNumber, parts[0].PartNumber)
				require.Equal(t, old.ETag, parts[0].ETag)
				return nil
			}))
		},
		"invalid replacement rolls back deletion of cached parts": func(t *testing.T, h UploadHarness) {
			row := contractUpload()
			h.Seed(t, row, []domain.UploadPart{{UploadID: row.UploadID, PartNumber: 1, ETag: "cached", Size: 6}})
			require.Error(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				return s.ReplaceUploadParts(ctx, row.UploadID, []domain.UploadPart{{UploadID: row.UploadID, PartNumber: 0, ETag: "invalid", Size: 6}})
			}))
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				parts, err := s.UploadParts(ctx, row.UploadID)
				require.NoError(t, err)
				require.Len(t, parts, 1)
				require.Equal(t, "cached", parts[0].ETag)
				return nil
			}))
		},
		"part deletion and empty replacement stay within upload": func(t *testing.T, h UploadHarness) {
			row, other := contractUpload(), contractUpload()
			h.Seed(t, row, []domain.UploadPart{{UploadID: row.UploadID, PartNumber: 1, ETag: "one", Size: 6}, {UploadID: row.UploadID, PartNumber: 2, ETag: "two", Size: 6}})
			h.Seed(t, other, []domain.UploadPart{{UploadID: other.UploadID, PartNumber: 1, ETag: "other", Size: 6}})
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				require.NoError(t, s.DeleteUploadPart(ctx, row.UploadID, 1))
				parts, err := s.UploadParts(ctx, row.UploadID)
				require.NoError(t, err)
				require.Len(t, parts, 1)
				require.Equal(t, int32(2), parts[0].PartNumber)
				require.NoError(t, s.ReplaceUploadParts(ctx, row.UploadID, nil))
				parts, err = s.UploadParts(ctx, row.UploadID)
				require.NoError(t, err)
				require.Empty(t, parts)
				parts, err = s.UploadParts(ctx, other.UploadID)
				require.NoError(t, err)
				require.Len(t, parts, 1)
				require.Equal(t, "other", parts[0].ETag)
				return nil
			}))
		},
		"writable session lock blocks finalization": func(t *testing.T, h UploadHarness) {
			row := contractUpload()
			h.Seed(t, row, nil)
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, err := s.WritableUploadSession(ctx, row.UploadID, row.FileID)
				return err
			}, func(context.Context, domain.Store) error { return nil }, func(ctx context.Context, s domain.Store) error {
				changed, err := s.ClaimUploadFinalizing(ctx, row.UploadID, row.FileID, time.Now().UTC())
				require.True(t, changed)
				return err
			}))
		},
	}
	for _, status := range []domain.UploadSessionStatus{domain.UploadSessionStatusFinalizing, domain.UploadSessionStatusFailed, domain.UploadSessionStatusAborted} {
		cases["part writes reject "+string(status)] = func(t *testing.T, h UploadHarness) {
			row := contractUpload()
			row.Status = status
			h.Seed(t, row, nil)
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, err := s.WritableUploadSession(ctx, row.UploadID, row.FileID)
				require.ErrorIs(t, err, domain.ErrUploadSessionNotFound)
				changed, err := s.MarkUploadPartUploading(ctx, row.UploadID, row.FileID, time.Now())
				require.NoError(t, err)
				require.False(t, changed)
				changed, err = s.DetectUploadMIME(ctx, row.UploadID, row.FileID, "image/webp", time.Now())
				require.NoError(t, err)
				require.False(t, changed)
				return nil
			}))
		}
	}
	return cases
}
