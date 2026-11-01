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

type UploadHarness struct {
	mutationtest.Harness[domain.Store]
	Seed func(*testing.T, domain.UploadSession, []domain.UploadPart)
}

func contractUpload() domain.UploadSession {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return domain.UploadSession{UploadID: uuid.NewString(), FileID: uuid.NewString(), UploadType: "UPLOAD_TYPE_GENERAL_FILE", FileName: "source", FileSize: 12, RequestedMime: "image/webp", TotalParts: 2, ChunkSize: 6, Status: domain.UploadSessionStatusInitiated, AttemptID: new(uuid.NewString()), CreatedAt: now, UpdatedAt: now, LastActivityAt: now}
}
func RunUploadContract(t *testing.T, open func(*testing.T) UploadHarness) {
	t.Helper()
	cases := map[string]func(*testing.T, UploadHarness){
		"session metadata and sorted parts retain optional values": func(t *testing.T, h UploadHarness) {
			row := contractUpload()
			row.ClientMediaBundleID = new(uuid.NewString())
			row.ClientMediaManifest = new(`{"version":1}`)
			row.FileLastModified = new(int64(123))
			row.DetectedMime = new("image/webp")
			row.VerifiedAt = &row.CreatedAt
			parts := []domain.UploadPart{{UploadID: row.UploadID, PartNumber: 2, ETag: "second", Size: 6, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, {UploadID: row.UploadID, PartNumber: 1, ETag: "first", Size: 6, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}}
			h.Seed(t, row, parts)
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.UploadSession(ctx, row.UploadID, row.FileID, false)
				require.NoError(t, err)
				require.Equal(t, row.UploadID, got.UploadID)
				require.Equal(t, row.FileID, got.FileID)
				require.Equal(t, row.FileName, got.FileName)
				require.Equal(t, row.FileSize, got.FileSize)
				require.Equal(t, row.Status, got.Status)
				require.Equal(t, row.UploadType, got.UploadType)
				require.Equal(t, row.RequestedMime, got.RequestedMime)
				require.Equal(t, row.TotalParts, got.TotalParts)
				require.Equal(t, row.ChunkSize, got.ChunkSize)
				require.Equal(t, row.ClientMediaBundleID, got.ClientMediaBundleID)
				require.JSONEq(t, *row.ClientMediaManifest, *got.ClientMediaManifest)
				require.Equal(t, row.FileLastModified, got.FileLastModified)
				require.Equal(t, row.DetectedMime, got.DetectedMime)
				require.Equal(t, row.AttemptID, got.AttemptID)
				require.True(t, row.CreatedAt.Equal(got.CreatedAt))
				require.True(t, row.VerifiedAt.Equal(*got.VerifiedAt))
				*got.AttemptID = "mutated copy"
				again, err := s.UploadSession(ctx, row.UploadID, row.FileID, false)
				require.NoError(t, err)
				require.Equal(t, row.AttemptID, again.AttemptID)
				loaded, err := s.UploadParts(ctx, row.UploadID)
				require.NoError(t, err)
				require.Len(t, loaded, 2)
				require.Equal(t, int32(1), loaded[0].PartNumber)
				require.Equal(t, "first", loaded[0].ETag)
				require.Equal(t, int32(2), loaded[1].PartNumber)
				_, err = s.UploadSession(ctx, row.UploadID, uuid.NewString(), false)
				require.ErrorIs(t, err, domain.ErrUploadSessionNotFound)
				return nil
			}))
		},
		"session changes and cascading part deletion roll back": func(t *testing.T, h UploadHarness) {
			row := contractUpload()
			row.Status = domain.UploadSessionStatusFinalizing
			h.Seed(t, row, []domain.UploadPart{{UploadID: row.UploadID, PartNumber: 1, ETag: "part", Size: 6}})
			abort := errors.New("abort owner")
			require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				require.NoError(t, s.RenewUploadActivity(ctx, row.UploadID, row.CreatedAt.Add(time.Hour)))
				_, err := s.NextUploadIngestSequence(ctx, row.UploadID)
				require.NoError(t, err)
				deleted, err := s.DeleteFinalizingUpload(ctx, row.UploadID)
				require.NoError(t, err)
				require.True(t, deleted)
				return abort
			}), abort)
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.UploadSession(ctx, row.UploadID, row.FileID, false)
				require.NoError(t, err)
				require.Zero(t, got.IngestSequence)
				require.True(t, row.LastActivityAt.Equal(got.LastActivityAt))
				parts, err := s.UploadParts(ctx, row.UploadID)
				require.Len(t, parts, 1)
				return err
			}))
		},
		"session update lock blocks lifecycle claims": func(t *testing.T, h UploadHarness) {
			row := contractUpload()
			h.Seed(t, row, nil)
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, err := s.UploadSession(ctx, row.UploadID, row.FileID, true)
				return err
			}, func(context.Context, domain.Store) error { return nil }, func(ctx context.Context, s domain.Store) error {
				claimed, err := s.ClaimUploadAborted(ctx, row.UploadID, row.FileID, time.Now().UTC())
				require.True(t, claimed)
				return err
			}))
		},
		"ingest sequence increments serialize and retain missing sentinel": func(t *testing.T, h UploadHarness) {
			row := contractUpload()
			h.Seed(t, row, nil)
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				n, err := s.NextUploadIngestSequence(ctx, row.UploadID)
				require.Equal(t, int64(1), n)
				return err
			}, func(context.Context, domain.Store) error { return nil }, func(ctx context.Context, s domain.Store) error {
				n, err := s.NextUploadIngestSequence(ctx, row.UploadID)
				require.Equal(t, int64(2), n)
				return err
			}))
			require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, err := s.NextUploadIngestSequence(ctx, uuid.NewString())
				return err
			}), domain.ErrUploadSessionNotFound)
		},
	}
	for _, status := range []domain.UploadSessionStatus{domain.UploadSessionStatusInitiated, domain.UploadSessionStatusUploading, domain.UploadSessionStatusFinalizing, domain.UploadSessionStatusFailed, domain.UploadSessionStatusAborted} {
		cases["session lifecycle guards "+string(status)] = func(t *testing.T, h UploadHarness) {
			row, other := contractUpload(), contractUpload()
			row.Status = status
			other.Status = status
			h.Seed(t, row, nil)
			h.Seed(t, other, []domain.UploadPart{{UploadID: other.UploadID, PartNumber: 1, ETag: "part", Size: 6}})
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				now := row.CreatedAt.Add(time.Minute)
				changed, err := s.RenewActiveUploadActivity(ctx, row.UploadID, uuid.NewString(), false, now)
				require.NoError(t, err)
				require.False(t, changed)
				changed, err = s.RenewActiveUploadActivity(ctx, row.UploadID, row.FileID, true, now)
				require.NoError(t, err)
				require.Equal(t, status == domain.UploadSessionStatusInitiated || status == domain.UploadSessionStatusUploading, changed)
				changed, err = s.RenewActiveUploadActivity(ctx, row.UploadID, row.FileID, false, now)
				require.NoError(t, err)
				active := status == domain.UploadSessionStatusInitiated || status == domain.UploadSessionStatusUploading || status == domain.UploadSessionStatusFinalizing
				require.Equal(t, active, changed)
				changed, err = s.ClaimUploadFinalizing(ctx, row.UploadID, row.FileID, now)
				require.NoError(t, err)
				require.Equal(t, active, changed)
				deleted, err := s.DeleteFinalizingUpload(ctx, row.UploadID)
				require.NoError(t, err)
				require.Equal(t, active, deleted)
				aborted, err := s.ClaimUploadAborted(ctx, other.UploadID, other.FileID, now)
				require.NoError(t, err)
				require.Equal(t, status != domain.UploadSessionStatusFinalizing, aborted)
				deleted, err = s.DeleteAbortedUpload(ctx, other.UploadID)
				require.NoError(t, err)
				require.Equal(t, aborted, deleted)
				if aborted {
					parts, err := s.UploadParts(ctx, other.UploadID)
					require.NoError(t, err)
					require.Empty(t, parts)
				}
				return nil
			}))
		}
	}
	for name, run := range uploadSelectionCases() {
		cases[name] = run
	}
	for name, run := range uploadPartCases() {
		cases[name] = run
	}
	for name, run := range uploadCreationCases() {
		cases[name] = run
	}
	for name, run := range uploadFailureCases() {
		cases[name] = run
	}
	for name, run := range uploadBundleCases() {
		cases[name] = run
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) { t.Parallel(); run(t, open(t)) })
	}
}
