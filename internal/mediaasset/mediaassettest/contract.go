package mediaassettest

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type Harness struct {
	Cleanup mutationtest.Harness[mediaasset.CleanupStore]
	mutationtest.Harness[mediaasset.Store]
	SeedFile       func(*testing.T, *time.Time) string
	SeedGeneration func(*testing.T, mediaasset.Generation)
}

func AllocatedAsset(at time.Time) mediaasset.Asset {
	id := uuid.NewString()
	return mediaasset.Asset{ID: id, Kind: "image", ObjectKey: "asset/" + id + ".webp", Extension: "webp", MimeType: "image/webp", Disposition: "inline", Status: mediaasset.AssetAllocated, CreatedAt: at, UpdatedAt: at}
}
func ReadyAsset(at time.Time) mediaasset.Asset {
	asset := AllocatedAsset(at)
	size := int64(64)
	digest := sha256.Sum256([]byte(asset.ID))
	asset.FileSize = &size
	asset.SHA256 = digest[:]
	asset.Status = mediaasset.AssetReady
	asset.ReadyAt = &at
	return asset
}
func insertAsset(t *testing.T, h Harness, asset mediaasset.Asset) {
	t.Helper()
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error { return s.InsertAsset(ctx, asset) }))
}
func readAsset(t *testing.T, h Harness, id string) mediaasset.Asset {
	t.Helper()
	var asset mediaasset.Asset
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error {
		var err error
		asset, err = s.Asset(ctx, id)
		return err
	}))
	return asset
}
func binding(assetID, ownerID, key string, at time.Time) mediaasset.AssetBinding {
	return mediaasset.AssetBinding{AssetID: assetID, OwnerType: "post", OwnerID: ownerID, BindingKey: key, CreatedAt: at, UpdatedAt: at}
}
func insertBinding(t *testing.T, h Harness, b mediaasset.AssetBinding) {
	t.Helper()
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error { return s.InsertBinding(ctx, b) }))
}

func RunStoreContract(t *testing.T, open func(*testing.T) Harness) {
	t.Helper()
	cases := map[string]func(*testing.T, Harness){
		"asset round trip and missing sentinel": func(t *testing.T, h Harness) {
			at := time.Now().UTC().Truncate(time.Microsecond)
			asset := AllocatedAsset(at)
			insertAsset(t, h, asset)
			got := readAsset(t, h, asset.ID)
			require.True(t, asset.CreatedAt.Equal(got.CreatedAt))
			require.True(t, asset.UpdatedAt.Equal(got.UpdatedAt))
			got.CreatedAt = got.CreatedAt.UTC()
			got.UpdatedAt = got.UpdatedAt.UTC()
			require.Equal(t, asset, got)
			require.ErrorIs(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error {
				_, err := s.Asset(ctx, uuid.NewString())
				return err
			}), mediaasset.ErrAssetNotFound)
		},
		"object keys are unique": func(t *testing.T, h Harness) {
			asset := AllocatedAsset(time.Now().UTC().Truncate(time.Microsecond))
			insertAsset(t, h, asset)
			other := asset
			other.ID = uuid.NewString()
			require.Error(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error { return s.InsertAsset(ctx, other) }))
		},
		"completion writes immutable metadata only while allocated": func(t *testing.T, h Harness) {
			at := time.Now().UTC().Truncate(time.Microsecond)
			asset := AllocatedAsset(at)
			insertAsset(t, h, asset)
			digest := sha256.Sum256([]byte("bytes"))
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error {
				return s.CompleteAsset(ctx, asset.ID, 64, digest[:], at)
			}))
			got := readAsset(t, h, asset.ID)
			require.Equal(t, mediaasset.AssetReady, got.Status)
			require.Equal(t, int64(64), *got.FileSize)
			require.Equal(t, digest[:], got.SHA256)
			require.True(t, got.ReadyAt.Equal(at))
			require.Nil(t, got.FailedAt)
			require.Nil(t, got.FailureReason)
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error {
				return s.CompleteAsset(ctx, asset.ID, 128, make([]byte, 32), at.Add(time.Minute))
			}))
			require.Equal(t, got, readAsset(t, h, asset.ID))
		},
		"ready files are ordered and filtered": func(t *testing.T, h Harness) {
			at := time.Now().UTC().Truncate(time.Microsecond)
			file := h.SeedFile(t, nil)
			older := ReadyAsset(at)
			older.SourceFileID = &file
			older.Kind = "thumbnail"
			newer := ReadyAsset(at.Add(time.Minute))
			newer.SourceFileID = &file
			newer.Kind = "thumbnail"
			allocated := AllocatedAsset(at.Add(2 * time.Minute))
			allocated.SourceFileID = &file
			allocated.Kind = "thumbnail"
			for _, asset := range []mediaasset.Asset{newer, allocated, older} {
				insertAsset(t, h, asset)
			}
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error {
				rows, err := s.ReadyAssetsForFiles(ctx, "thumbnail", []string{file})
				require.NoError(t, err)
				require.Len(t, rows, 2)
				require.Equal(t, []string{newer.ID, older.ID}, []string{rows[0].ID, rows[1].ID})
				latest, err := s.ReadyAssetForFile(ctx, file, "thumbnail")
				require.NoError(t, err)
				require.Equal(t, newer.ID, latest.ID)
				return nil
			}))
			require.ErrorIs(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error {
				_, err := s.ReadyAssetForFile(ctx, file, "logo")
				return err
			}), mediaasset.ErrAssetNotFound)
		},
		"bindings compare and swap the expected asset": func(t *testing.T, h Harness) {
			at := time.Now().UTC().Truncate(time.Microsecond)
			a, b := ReadyAsset(at), ReadyAsset(at)
			insertAsset(t, h, a)
			insertAsset(t, h, b)
			owner := uuid.NewString()
			old := binding(a.ID, owner, "image", at)
			insertBinding(t, h, old)
			require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error { return s.InsertBinding(ctx, old) }), mediaasset.ErrBindingConflict)
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error {
				next := binding(b.ID, owner, "image", at.Add(time.Minute))
				changed, err := s.ReplaceBinding(ctx, next, b.ID)
				require.NoError(t, err)
				require.False(t, changed)
				changed, err = s.ReplaceBinding(ctx, next, a.ID)
				require.NoError(t, err)
				require.True(t, changed)
				got, found, err := s.Binding(ctx, "post", owner, "image")
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, b.ID, got.AssetID)
				require.True(t, got.CreatedAt.Equal(at))
				return nil
			}))
		},
		"families match literal underscore separators": func(t *testing.T, h Harness) {
			at := time.Now().UTC().Truncate(time.Microsecond)
			asset := ReadyAsset(at)
			insertAsset(t, h, asset)
			owner := uuid.NewString()
			for _, key := range []string{"og", "og_ko", "ogx", "og-ko", "featured_image", "featured_image_alt", "featuredXimage"} {
				insertBinding(t, h, binding(asset.ID, owner, key, at))
			}
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error {
				keys, err := s.BindingKeys(ctx, "post", owner, "og")
				require.NoError(t, err)
				require.Equal(t, []string{"og", "og_ko"}, keys)
				keys, err = s.BindingKeys(ctx, "post", owner, "featured_image")
				require.NoError(t, err)
				require.Equal(t, []string{"featured_image", "featured_image_alt"}, keys)
				return nil
			}))
		},
		"only unbound generated assets become delete pending": func(t *testing.T, h Harness) {
			at := time.Now().UTC().Truncate(time.Microsecond)
			generated, bound, source := ReadyAsset(at), ReadyAsset(at), ReadyAsset(at)
			file := h.SeedFile(t, nil)
			source.SourceFileID = &file
			for _, asset := range []mediaasset.Asset{generated, bound, source} {
				insertAsset(t, h, asset)
			}
			owner := uuid.NewString()
			insertBinding(t, h, binding(bound.ID, owner, "image", at))
			insertBinding(t, h, binding(generated.ID, owner, "og", at))
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error {
				count, err := s.DeleteBindings(ctx, "post", owner, []string{"og", "absent"})
				require.NoError(t, err)
				require.Equal(t, int64(1), count)
				return s.MarkUnboundAssetsDeletePending(ctx, []string{generated.ID, bound.ID, source.ID}, at)
			}))
			require.Equal(t, mediaasset.AssetDeletePending, readAsset(t, h, generated.ID).Status)
			require.Equal(t, mediaasset.AssetReady, readAsset(t, h, bound.ID).Status)
			require.Equal(t, mediaasset.AssetReady, readAsset(t, h, source.ID).Status)
		},
		"delete transition checks current status": func(t *testing.T, h Harness) {
			at := time.Now().UTC().Truncate(time.Microsecond)
			allocated, ready := AllocatedAsset(at), ReadyAsset(at)
			insertAsset(t, h, allocated)
			insertAsset(t, h, ready)
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error {
				changed, err := s.MarkAssetDeletePending(ctx, allocated.ID, at)
				require.NoError(t, err)
				require.False(t, changed)
				changed, err = s.MarkAssetDeletePending(ctx, ready.ID, at)
				require.NoError(t, err)
				require.True(t, changed)
				changed, err = s.MarkAssetDeletePending(ctx, ready.ID, at)
				require.NoError(t, err)
				require.False(t, changed)
				return nil
			}))
		},
		"generation completion retires the previous generation": func(t *testing.T, h Harness) {
			at := time.Now().UTC().Truncate(time.Microsecond)
			file := h.SeedFile(t, nil)
			oldID, newID := uuid.NewString(), uuid.NewString()
			digest := sha256.Sum256([]byte("manifest"))
			count, size := int32(4), int64(1000)
			h.SeedGeneration(t, mediaasset.Generation{ID: oldID, FileID: file, Kind: "hls", ObjectPrefix: "media/" + file + "/hls/" + oldID, ManifestName: "master.m3u8", ManifestSHA256: digest[:], ObjectCount: &count, TotalSize: &size, Status: mediaasset.GenerationReady, ReadyAt: &at, CreatedAt: at, UpdatedAt: at})
			h.SeedGeneration(t, mediaasset.Generation{ID: newID, FileID: file, Kind: "hls", ObjectPrefix: "media/" + file + "/hls/" + newID, ManifestName: "master.m3u8", Status: mediaasset.GenerationAllocated, CreatedAt: at, UpdatedAt: at})
			deadline := at.Add(7 * time.Hour)
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error {
				if err := s.RetireOtherGenerations(ctx, file, newID, at, deadline); err != nil {
					return err
				}
				return s.CompleteGeneration(ctx, newID, digest[:], count, size, at)
			}))
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error {
				old, err := s.Generation(ctx, oldID)
				require.NoError(t, err)
				require.Equal(t, mediaasset.GenerationRetired, old.Status)
				require.True(t, old.DeleteAfter.Equal(deadline))
				current, err := s.Generation(ctx, newID)
				require.NoError(t, err)
				require.Equal(t, mediaasset.GenerationReady, current.Status)
				require.Equal(t, digest[:], current.ManifestSHA256)
				return nil
			}))
		},
		"failed transaction rolls back asset and binding": func(t *testing.T, h Harness) {
			at := time.Now().UTC().Truncate(time.Microsecond)
			asset := ReadyAsset(at)
			failure := errors.New("abort")
			require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error {
				if err := s.InsertAsset(ctx, asset); err != nil {
					return err
				}
				if err := s.InsertBinding(ctx, binding(asset.ID, uuid.NewString(), "image", at)); err != nil {
					return err
				}
				return failure
			}), failure)
			require.ErrorIs(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error { _, err := s.Asset(ctx, asset.ID); return err }), mediaasset.ErrAssetNotFound)
		},
		"attachable files reject missing and deletion pending": func(t *testing.T, h Harness) {
			now := time.Now().UTC().Truncate(time.Microsecond)
			ready, pending := h.SeedFile(t, nil), h.SeedFile(t, &now)
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error {
				return s.LockAttachableFiles(ctx, []string{" " + ready + " ", ready})
			}))
			require.ErrorContains(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error {
				return s.LockAttachableFiles(ctx, []string{pending})
			}), "pending deletion")
			require.ErrorContains(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error {
				return s.LockAttachableFiles(ctx, []string{uuid.NewString()})
			}), "no longer exist")
		},
	}
	cases["cleanup lists and finishes pending assets with compare and swap"] = func(t *testing.T, h Harness) {
		now := time.Now().UTC().Truncate(time.Microsecond)
		asset := ReadyAsset(now.Add(-time.Hour))
		asset.Status = mediaasset.AssetDeletePending
		asset.DeleteRequestedAt = &now
		insertAsset(t, h, asset)
		require.NoError(t, mutationtest.Read(t, h.Cleanup, func(ctx context.Context, s mediaasset.CleanupStore) error {
			rows, err := s.PendingAssets(ctx)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, asset.ID, rows[0].ID)
			count, err := s.AssetBindingCount(ctx, asset.ID)
			require.NoError(t, err)
			require.Zero(t, count)
			return nil
		}))
		require.NoError(t, mutationtest.Transact(t, h.Cleanup, func(ctx context.Context, s mediaasset.CleanupStore) error {
			changed, err := s.MarkAssetDeleted(ctx, asset.ID, now)
			require.NoError(t, err)
			require.True(t, changed)
			changed, err = s.MarkAssetDeleted(ctx, asset.ID, now)
			require.NoError(t, err)
			require.False(t, changed)
			return nil
		}))
		require.Equal(t, mediaasset.AssetDeleted, readAsset(t, h, asset.ID).Status)
	}
	cases["cleanup selects only expired unready assets and rechecks deletion"] = func(t *testing.T, h Harness) {
		now := time.Now().UTC().Truncate(time.Microsecond)
		expired := AllocatedAsset(now.Add(-time.Hour))
		fresh := AllocatedAsset(now.Add(time.Hour))
		ready := ReadyAsset(now.Add(-time.Hour))
		for _, asset := range []mediaasset.Asset{expired, fresh, ready} {
			insertAsset(t, h, asset)
		}
		require.NoError(t, mutationtest.Read(t, h.Cleanup, func(ctx context.Context, s mediaasset.CleanupStore) error {
			ids, err := s.ExpiredUnreadyAssetIDs(ctx, now)
			require.NoError(t, err)
			require.Equal(t, []string{expired.ID}, ids)
			return nil
		}))
		require.NoError(t, mutationtest.Transact(t, h.Cleanup, func(ctx context.Context, s mediaasset.CleanupStore) error {
			deleted, err := s.DeleteExpiredAsset(ctx, fresh.ID, now)
			require.NoError(t, err)
			require.False(t, deleted)
			deleted, err = s.DeleteExpiredAsset(ctx, ready.ID, now)
			require.NoError(t, err)
			require.False(t, deleted)
			deleted, err = s.DeleteExpiredAsset(ctx, expired.ID, now)
			require.NoError(t, err)
			require.True(t, deleted)
			return nil
		}))
		require.ErrorIs(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s mediaasset.Store) error { _, err := s.Asset(ctx, expired.ID); return err }), mediaasset.ErrAssetNotFound)
	}
	cases["cleanup transaction rolls back the deletion marker"] = func(t *testing.T, h Harness) {
		now := time.Now().UTC().Truncate(time.Microsecond)
		asset := ReadyAsset(now)
		asset.Status = mediaasset.AssetDeletePending
		asset.DeleteRequestedAt = &now
		insertAsset(t, h, asset)
		failure := errors.New("abort cleanup")
		require.ErrorIs(t, mutationtest.Transact(t, h.Cleanup, func(ctx context.Context, s mediaasset.CleanupStore) error {
			_, err := s.MarkAssetDeleted(ctx, asset.ID, now)
			if err != nil {
				return err
			}
			return failure
		}), failure)
		require.Equal(t, mediaasset.AssetDeletePending, readAsset(t, h, asset.ID).Status)
	}
	cases["cleanup identifies due retired generations and removes their rows"] = func(t *testing.T, h Harness) {
		now := time.Now().UTC().Truncate(time.Microsecond)
		file := h.SeedFile(t, nil)
		id := uuid.NewString()
		created, retired := now.Add(-9*time.Hour), now.Add(-8*time.Hour)
		digest := sha256.Sum256([]byte("manifest"))
		count, size := int32(4), int64(1024)
		h.SeedGeneration(t, mediaasset.Generation{ID: id, FileID: file, Kind: "hls", ObjectPrefix: "media/" + file + "/hls/" + id, ManifestName: "master.m3u8", ManifestSHA256: digest[:], ObjectCount: &count, TotalSize: &size, Status: mediaasset.GenerationRetired, ReadyAt: &created, RetiredAt: &retired, DeleteAfter: &now, CreatedAt: created, UpdatedAt: retired})
		require.NoError(t, mutationtest.Read(t, h.Cleanup, func(ctx context.Context, s mediaasset.CleanupStore) error {
			ids, err := s.DueRetiredGenerationIDs(ctx, now)
			require.NoError(t, err)
			require.Equal(t, []string{id}, ids)
			pending, err := s.FileDeletionPending(ctx, file)
			require.NoError(t, err)
			require.False(t, pending)
			referenced, err := s.GenerationReferenced(ctx, id)
			require.NoError(t, err)
			require.False(t, referenced)
			return nil
		}))
		require.NoError(t, h.Cleanup.Units.Transaction(t.Context(), false, func(tx mutation.Tx[mediaasset.CleanupStore]) error {
			deleted, err := tx.Store().DeleteGeneration(t.Context(), id)
			require.NoError(t, err)
			require.True(t, deleted)
			return nil
		}))
	}

	for _, name := range []string{"asset", "asset deletion", "asset set", "binding", "binding set", "asset bindings", "file", "generation"} {
		cases["lock "+name+" waits for the first transaction"] = func(t *testing.T, h Harness) {
			at := time.Now().UTC().Truncate(time.Microsecond)
			asset := ReadyAsset(at)
			insertAsset(t, h, asset)
			owner := uuid.NewString()
			insertBinding(t, h, binding(asset.ID, owner, "image", at))
			file := h.SeedFile(t, nil)
			id := uuid.NewString()
			h.SeedGeneration(t, mediaasset.Generation{ID: id, FileID: file, Kind: "hls", ObjectPrefix: "media/" + file + "/hls/" + id, ManifestName: "master.m3u8", Status: mediaasset.GenerationAllocated, CreatedAt: at, UpdatedAt: at})
			lock := func(ctx context.Context, s mediaasset.Store) error {
				switch name {
				case "asset":
					_, err := s.LockAsset(ctx, asset.ID)
					return err
				case "asset deletion":
					_, err := s.LockAssetForDeletion(ctx, asset.ID)
					return err
				case "asset set":
					_, err := s.LockAssets(ctx, []string{asset.ID})
					return err
				case "binding":
					_, _, err := s.LockBinding(ctx, "post", owner, "image")
					return err
				case "binding set":
					_, err := s.LockBindings(ctx, "post", owner, []string{"image"})
					return err
				case "asset bindings":
					_, err := s.LockAssetBindings(ctx, []string{asset.ID})
					return err
				case "file":
					return s.LockAttachableFiles(ctx, []string{file})
				case "generation":
					_, err := s.LockGeneration(ctx, id)
					return err
				default:
					t.Fatal("unknown lock")
					return nil
				}
			}
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, lock, func(context.Context, mediaasset.Store) error { return nil }, lock))
		}
	}
	for _, kind := range []string{"expired asset", "retired generation"} {
		cases["cleanup lock "+kind+" waits"] = func(t *testing.T, h Harness) {
			now := time.Now().UTC().Truncate(time.Microsecond)
			asset := AllocatedAsset(now)
			insertAsset(t, h, asset)
			file := h.SeedFile(t, nil)
			id := uuid.NewString()
			h.SeedGeneration(t, mediaasset.Generation{ID: id, FileID: file, Kind: "hls", ObjectPrefix: "media/" + file + "/hls/" + id, ManifestName: "master.m3u8", Status: mediaasset.GenerationAllocated, CreatedAt: now, UpdatedAt: now})
			lock := func(ctx context.Context, s mediaasset.CleanupStore) error {
				if kind == "expired asset" {
					_, err := s.LockExpiredAsset(ctx, asset.ID)
					return err
				}
				_, err := s.LockRetiredGeneration(ctx, id)
				return err
			}
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Cleanup, lock, func(context.Context, mediaasset.CleanupStore) error { return nil }, lock))
		}
	}

	for name, run := range cases {
		t.Run(name, func(t *testing.T) { t.Parallel(); run(t, open(t)) })
	}
}
