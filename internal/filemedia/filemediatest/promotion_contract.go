package filemediatest

import (
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

func promotionContractCases() map[string]func(*testing.T, CatalogHarness) {
	return map[string]func(*testing.T, CatalogHarness){
		"promotion allocation failure resume completion and ready reference": func(t *testing.T, h CatalogHarness) {
			row := CatalogFile("source", nil)
			h.SeedFile(t, row, false, false)
			var id, key string
			now := time.Now().UTC().Truncate(time.Microsecond)
			digest := make([]byte, 32)
			digest[0] = 9
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				file, err := s.PromotionFile(ctx, row.ID, true)
				require.NoError(t, err)
				require.Equal(t, domain.File{ID: row.ID, MimeType: *row.MimeType, FileSize: *row.FileSize, Extension: *row.Extension}, file)
				_, err = s.SourcePromotionAsset(ctx, row.ID, true)
				require.ErrorIs(t, err, mediaasset.ErrAssetNotFound)
				a, target, err := s.AllocatePromotionAsset(ctx, "https://cdn.example.com", mediaasset.Allocation{SourceFileID: &row.ID, Kind: "image", Extension: *row.Extension, MimeType: *row.MimeType, Disposition: mediaasset.DispositionInline})
				require.NoError(t, err)
				id, key = a.ID, target.ObjectKey
				require.Equal(t, "asset/"+id+".webp", key)
				changed, err := s.FailPromotionAsset(ctx, id, "storage failed", now)
				require.NoError(t, err)
				require.True(t, changed)
				changed, err = s.FailPromotionAsset(ctx, id, "duplicate", now)
				require.NoError(t, err)
				require.False(t, changed)
				a2, err := s.SourcePromotionAsset(ctx, row.ID, true)
				require.NoError(t, err)
				require.Equal(t, id, a2.ID)
				require.Equal(t, mediaasset.AssetFailed, a2.Status)
				require.Equal(t, "storage failed", *a2.FailureReason)
				changed, err = s.ResumePromotionAsset(ctx, id, true, now.Add(time.Second))
				require.NoError(t, err)
				require.True(t, changed)
				resumed, err := s.PromotionAsset(ctx, id, true)
				require.NoError(t, err)
				require.Equal(t, key, resumed.ObjectKey)
				require.Equal(t, mediaasset.AssetAllocated, resumed.Status)
				require.Nil(t, resumed.FailedAt)
				require.Nil(t, resumed.FailureReason)
				require.Nil(t, resumed.FileSize)
				require.Empty(t, resumed.SHA256)
				require.NoError(t, s.CompletePromotionAsset(ctx, "https://cdn.example.com", mediaasset.WrittenAsset{AssetID: id, FileSize: 64, SHA256: digest}))
				changed, err = s.FailPromotionAsset(ctx, id, "late failure", now)
				require.NoError(t, err)
				require.False(t, changed)
				ref, err := s.ReadyPromotionReference(ctx, "https://cdn.example.com", id)
				require.NoError(t, err)
				require.Equal(t, id, ref.AssetID)
				require.Equal(t, "https://cdn.example.com/asset/"+id+"/image.webp", ref.URL)
				require.Equal(t, int64(64), ref.FileSize)
				require.Equal(t, digest, ref.SHA256)
				return nil
			}))
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				a, err := s.SourcePromotionAsset(ctx, row.ID, false)
				require.NoError(t, err)
				require.Equal(t, id, a.ID)
				require.Equal(t, key, a.ObjectKey)
				require.Equal(t, mediaasset.AssetReady, a.Status)
				return nil
			}))
		},
		"promotion allocation and completion roll back with owner transaction": func(t *testing.T, h CatalogHarness) {
			row := CatalogFile("source", nil)
			h.SeedFile(t, row, false, false)
			abort := errors.New("owner abort")
			require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, err := s.PromotionFile(ctx, row.ID, true)
				require.NoError(t, err)
				a, _, err := s.AllocatePromotionAsset(ctx, "", mediaasset.Allocation{SourceFileID: &row.ID, Kind: "image", Extension: *row.Extension, MimeType: *row.MimeType, Disposition: mediaasset.DispositionInline})
				require.NoError(t, err)
				require.NoError(t, s.CompletePromotionAsset(ctx, "", mediaasset.WrittenAsset{AssetID: a.ID, FileSize: 64, SHA256: make([]byte, 32)}))
				return abort
			}), abort)
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, err := s.SourcePromotionAsset(ctx, row.ID, false)
				require.ErrorIs(t, err, mediaasset.ErrAssetNotFound)
				return nil
			}))
		},
		"promotion rejects pending source before allocation": func(t *testing.T, h CatalogHarness) {
			row := CatalogFile("pending", nil)
			h.SeedFile(t, row, true, false)
			require.ErrorContains(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, err := s.PromotionFile(ctx, row.ID, true)
				return err
			}), "file is pending deletion")
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				file, err := s.PromotionFile(ctx, row.ID, false)
				require.NoError(t, err)
				require.NotNil(t, file.DeleteRequestedAt)
				return nil
			}))
		},
		"promotion File lock blocks deletion": func(t *testing.T, h CatalogHarness) {
			row := CatalogFile("source", nil)
			h.SeedFile(t, row, false, false)
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, err := s.PromotionFile(ctx, row.ID, true)
				return err
			}, func(context.Context, domain.Store) error { return nil }, func(ctx context.Context, s domain.Store) error {
				_, err := s.LockDeletionCandidate(ctx, row.ID)
				return err
			}))
		},
		"promotion asset lock blocks failure compare-and-set": func(t *testing.T, h CatalogHarness) {
			row := CatalogFile("source", nil)
			h.SeedFile(t, row, false, false)
			id := uuid.NewString()
			now := time.Now().UTC().Truncate(time.Microsecond)
			h.SeedAsset(t, mediaasset.Asset{ID: id, SourceFileID: &row.ID, Kind: "image", Extension: *row.Extension, MimeType: *row.MimeType, Disposition: "inline", ObjectKey: "asset/" + id + ".webp", Status: mediaasset.AssetAllocated, CreatedAt: now, UpdatedAt: now})
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, err := s.SourcePromotionAsset(ctx, row.ID, true)
				return err
			}, func(context.Context, domain.Store) error { return nil }, func(ctx context.Context, s domain.Store) error {
				changed, err := s.FailPromotionAsset(ctx, id, "failed", now)
				require.True(t, changed)
				return err
			}))
		},
	}
}
