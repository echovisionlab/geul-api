package mediaasset_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mediaasset/mediaassettest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func lifecycleFixture(t *testing.T) (*mediaasset.Lifecycle, *mediaassettest.Store, time.Time) {
	t.Helper()
	store, units := mediaassettest.New()
	service := mediaasset.NewLifecycle(units, "https://cdn.example.com/")
	now := time.Date(2026, time.July, 10, 8, 0, 0, 0, time.UTC)
	mediaasset.SetLifecycleClock(service, func() time.Time { return now })
	return service, store, now
}
func allocation() mediaasset.Allocation {
	return mediaasset.Allocation{Kind: "image", Extension: "webp", MimeType: "image/webp", Disposition: mediaasset.DispositionInline}
}
func writtenAsset(id string) *mediaasset.WrittenAsset {
	digest := sha256.Sum256([]byte(id))
	return &mediaasset.WrittenAsset{AssetID: id, FileSize: 128, SHA256: digest[:]}
}
func allocateReady(t *testing.T, service *mediaasset.Lifecycle, input mediaasset.Allocation) *mediaasset.Asset {
	t.Helper()
	asset, _, err := service.AllocatePublicAsset(t.Context(), input)
	require.NoError(t, err)
	asset, err = service.CompletePublicAsset(t.Context(), writtenAsset(asset.ID))
	require.NoError(t, err)
	return asset
}
func TestLifecycleNormalizesAllocationAndCompletesImmutableAsset(t *testing.T) {
	service, store, now := lifecycleFixture(t)
	source := uuid.NewString()
	store.PutFile(source, nil)
	input := allocation()
	input.SourceFileID = new(" " + source + " ")
	input.Kind = " image "
	input.Extension = " WEBP "
	input.MimeType = " image/webp; charset=binary "
	asset, target, err := service.AllocatePublicAsset(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, mediaasset.AssetAllocated, asset.Status)
	require.Equal(t, "asset/"+asset.ID+".webp", target.ObjectKey)
	require.Equal(t, source, *asset.SourceFileID)
	require.Equal(t, now, asset.CreatedAt)
	result := writtenAsset(asset.ID)
	ready, err := service.CompletePublicAsset(t.Context(), result)
	require.NoError(t, err)
	require.Equal(t, mediaasset.AssetReady, ready.Status)
	require.Equal(t, result.SHA256, ready.SHA256)
	again, err := service.CompletePublicAsset(t.Context(), result)
	require.NoError(t, err)
	require.Equal(t, ready, again)
	ref, err := service.ReadyAssetRef(t.Context(), asset.ID)
	require.NoError(t, err)
	require.Equal(t, "https://cdn.example.com/asset/"+asset.ID+"/image.webp", ref.URL)
	require.Equal(t, result.SHA256, ref.SHA256)
	changed := *result
	changed.FileSize++
	_, err = service.CompletePublicAsset(t.Context(), &changed)
	require.Equal(t, connect.CodeFailedPrecondition, errs.Wrap(err).Code())
	require.Equal(t, result.SHA256, ready.SHA256)
}
func TestLifecycleBindingReplacementRetainsSourcesAndDeletesOnlyLastGeneratedBinding(t *testing.T) {
	for _, sourceBacked := range []bool{false, true} {
		t.Run(map[bool]string{false: "generated", true: "source backed"}[sourceBacked], func(t *testing.T) {
			service, store, _ := lifecycleFixture(t)
			input := allocation()
			if sourceBacked {
				id := uuid.NewString()
				store.PutFile(id, nil)
				input.SourceFileID = &id
			}
			first := allocateReady(t, service, input)
			second := allocateReady(t, service, allocation())
			owner := uuid.NewString()
			bind := func(assetID, key string) error {
				return service.BindPublicAsset(t.Context(), mediaasset.Binding{AssetID: assetID, OwnerType: "post", OwnerID: owner, BindingKey: key})
			}
			require.NoError(t, bind(first.ID, "image"))
			require.NoError(t, bind(first.ID, "image_alt"))
			require.NoError(t, bind(second.ID, "image"))
			current, err := store.Asset(t.Context(), first.ID)
			require.NoError(t, err)
			require.Equal(t, mediaasset.AssetReady, current.Status)
			require.NoError(t, service.ReleaseExactPublicAssetBindings(t.Context(), "post", owner, []string{"image_alt"}))
			current, err = store.Asset(t.Context(), first.ID)
			require.NoError(t, err)
			if sourceBacked {
				require.Equal(t, mediaasset.AssetReady, current.Status)
			} else {
				require.Equal(t, mediaasset.AssetDeletePending, current.Status)
				require.NotNil(t, current.DeleteRequestedAt)
			}
			require.NoError(t, service.ReleasePublicAssetBindings(t.Context(), "post", owner, "image"))
			current, err = store.Asset(t.Context(), second.ID)
			require.NoError(t, err)
			require.Equal(t, mediaasset.AssetDeletePending, current.Status)
		})
	}
}
func TestLifecycleAttachmentDecisionsFailClosed(t *testing.T) {
	service, store, now := lifecycleFixture(t)
	source := uuid.NewString()
	store.PutFile(source, &now)
	input := allocation()
	input.SourceFileID = &source
	_, _, err := service.AllocatePublicAsset(t.Context(), input)
	require.ErrorContains(t, err, "pending deletion")
	allocated, _, err := service.AllocatePublicAsset(t.Context(), allocation())
	require.NoError(t, err)
	require.Error(t, service.BindPublicAsset(t.Context(), mediaasset.Binding{AssetID: allocated.ID, OwnerType: "post", OwnerID: uuid.NewString(), BindingKey: "image"}))
	require.Error(t, service.RequestPublicAssetDeletion(t.Context(), allocated.ID))
	ready := allocateReady(t, service, allocation())
	require.NoError(t, service.BindPublicAsset(t.Context(), mediaasset.Binding{AssetID: ready.ID, OwnerType: "post", OwnerID: uuid.NewString(), BindingKey: "image"}))
	require.ErrorContains(t, service.RequestPublicAssetDeletion(t.Context(), ready.ID), "still has bindings")
}
func TestLifecycleBindSourceUsesOneTransactionAndReadyReference(t *testing.T) {
	service, store, _ := lifecycleFixture(t)
	source := uuid.NewString()
	store.PutFile(source, nil)
	input := allocation()
	input.SourceFileID = &source
	asset := allocateReady(t, service, input)
	owner := uuid.NewString()
	ref, err := service.BindReadyAssetForSourceFile(t.Context(), source, "post", owner, "image", "image")
	require.NoError(t, err)
	require.Equal(t, asset.ID, ref.AssetID)
	binding, found, err := store.Binding(t.Context(), "post", owner, "image")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, source, *binding.SourceFileID)
}
func TestLifecycleRequestDeletionIsIdempotentAndPreservesFailedStateUntilRequested(t *testing.T) {
	service, store, now := lifecycleFixture(t)
	for _, status := range []mediaasset.AssetStatus{mediaasset.AssetReady, mediaasset.AssetFailed, mediaasset.AssetDeletePending, mediaasset.AssetDeleted} {
		asset := mediaassettest.ReadyAsset(now)
		asset.Status = status
		store.PutAsset(asset)
		require.NoError(t, service.RequestPublicAssetDeletion(t.Context(), asset.ID))
		require.NoError(t, service.RequestPublicAssetDeletion(t.Context(), asset.ID))
		got, err := store.Asset(t.Context(), asset.ID)
		require.NoError(t, err)
		if status == mediaasset.AssetDeleted {
			require.Equal(t, status, got.Status)
		} else {
			require.Equal(t, mediaasset.AssetDeletePending, got.Status)
		}
	}
	require.Equal(t, connect.CodeNotFound, errs.Wrap(service.RequestPublicAssetDeletion(t.Context(), uuid.NewString())).Code())
}
func TestLifecycleReadyReferencesUseNewestValidAssetAndBrandingFallback(t *testing.T) {
	_, store, now := lifecycleFixture(t)
	source := uuid.NewString()
	store.PutFile(source, nil)
	valid := mediaassettest.ReadyAsset(now)
	valid.SourceFileID = &source
	store.PutAsset(valid)
	corrupt := mediaassettest.ReadyAsset(now.Add(time.Minute))
	corrupt.SourceFileID = &source
	corrupt.FileSize = nil
	store.PutAsset(corrupt)
	// Batch resolution skips a corrupt projection and keeps the newest usable one.
	all := mutationtest.New[mediaasset.Store](store)
	refs, err := mediaasset.LoadReadyPublicAssetRefsForSourceFiles(t.Context(), all, "https://cdn.example.com", "image", []string{source, source})
	require.NoError(t, err)
	require.Equal(t, valid.ID, refs[source].AssetID)
	for _, tc := range []struct {
		mime, extension, kind string
		allowed               bool
	}{{"image/png", "png", mediaasset.BrandingKindEmailImage, true}, {"image/svg+xml", "svg", mediaasset.BrandingKindLogo, true}, {"image/svg+xml", "svg", mediaasset.BrandingKindEmailImage, false}, {"image/webp", "webp", mediaasset.BrandingKindLogo, false}} {
		t.Run(tc.mime+tc.kind, func(t *testing.T) {
			service, store, now := lifecycleFixture(t)
			source := uuid.NewString()
			asset := mediaassettest.ReadyAsset(now)
			asset.SourceFileID = &source
			asset.MimeType = tc.mime
			asset.Extension = tc.extension
			store.PutAsset(asset)
			ref, kind, err := service.ReadyBrandingAssetRef(t.Context(), source, tc.kind)
			if tc.allowed {
				require.NoError(t, err)
				require.Equal(t, asset.ID, ref.AssetID)
				require.Equal(t, "image", kind)
			} else {
				require.Error(t, err)
			}
		})
	}
}
func TestLifecycleGenerationSwitchRollsBackWhenCompletionFails(t *testing.T) {
	service, store, now := lifecycleFixture(t)
	file := uuid.NewString()
	oldID, newID := uuid.NewString(), uuid.NewString()
	digest := sha256.Sum256([]byte("manifest"))
	count, size := int32(4), int64(1024)
	store.PutGeneration(mediaasset.Generation{ID: oldID, FileID: file, Status: mediaasset.GenerationReady, ManifestSHA256: digest[:], ObjectCount: &count, TotalSize: &size})
	store.PutGeneration(mediaasset.Generation{ID: newID, FileID: file, Status: mediaasset.GenerationAllocated})
	failure := errors.New("completion failed")
	broken := generationFailure{Store: store, failure: failure}
	units := mutationtest.New[mediaasset.Store](broken)
	failing := mediaasset.NewLifecycle(units, "")
	_, err := failing.CompleteMediaGeneration(t.Context(), &mediaasset.WrittenGeneration{GenerationID: newID, ManifestSHA256: digest[:], ObjectCount: count, TotalSize: size})
	require.ErrorContains(t, err, "completion failed")
	old, err := store.Generation(t.Context(), oldID)
	require.NoError(t, err)
	require.Equal(t, mediaasset.GenerationReady, old.Status)
	result := &mediaasset.WrittenGeneration{GenerationID: newID, ManifestSHA256: digest[:], ObjectCount: count, TotalSize: size}
	ready, err := service.CompleteMediaGeneration(t.Context(), result)
	require.NoError(t, err)
	require.Equal(t, mediaasset.GenerationReady, ready.Status)
	old, err = store.Generation(t.Context(), oldID)
	require.NoError(t, err)
	require.Equal(t, mediaasset.GenerationRetired, old.Status)
	require.Equal(t, now.Add(mediaasset.RetiredMediaGenerationRetention), *old.DeleteAfter)
	_, err = service.CompleteMediaGeneration(t.Context(), result)
	require.NoError(t, err)
	changed := *result
	changed.TotalSize++
	_, err = service.CompleteMediaGeneration(t.Context(), &changed)
	require.ErrorContains(t, err, "conflicts with ready metadata")
}

type generationFailure struct {
	*mediaassettest.Store
	failure error
}

func (s generationFailure) CompleteGeneration(context.Context, string, []byte, int32, int64, time.Time) error {
	return s.failure
}
