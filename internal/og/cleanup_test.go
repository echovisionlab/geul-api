package og_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/og/ogtest"
)

func assetStatus(t *testing.T, store *ogtest.Store, id string) og.AssetStatus {
	t.Helper()
	asset, ok := store.StoredAsset(id)
	require.True(t, ok)
	return asset.Status
}

func TestMarkUnboundReadyAssetsRequiresThirtyDaysAndNoBindingOrPointer(t *testing.T) {
	store, units := ogtest.New()
	now := time.Now().UTC()
	old := now.Add(-31 * 24 * time.Hour)
	seed := func(kind string, at time.Time) og.Asset {
		asset := ogtest.ReadyAsset(kind, at)
		store.SeedAsset(asset)
		return asset
	}
	unbound, bound, young, image, referenced := seed("og", old), seed("og", old), seed("og", now.Add(-29*24*time.Hour)), seed("image", old), seed("og", old)
	store.SeedBinding(bound.ID)
	store.SeedReference(referenced.ID)

	require.NoError(t, og.NewCleanup(units).MarkUnboundReadyAssets(t.Context(), now.Add(-og.UnboundAssetRetention), now))
	require.Equal(t, og.AssetDeletePending, assetStatus(t, store, unbound.ID))
	for _, kept := range []og.Asset{bound, young, image, referenced} {
		require.Equal(t, og.AssetReady, assetStatus(t, store, kept.ID))
	}
}

func TestMarkExpiredTerminalGenerationAssetsKeepsActiveAndReferencedOutputs(t *testing.T) {
	store, units := ogtest.New()
	cancelledID, activeID, referencedID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	plan := mustPlan(t, units,
		localeRequest("work", cancelledID, "Cancelled", "en"),
		localeRequest("work", activeID, "Active", "en"),
		localeRequest("work", referencedID, "Referenced", "en"),
	)
	require.NoError(t, cancelEntity(t, units, "work", cancelledID))
	require.NoError(t, cancelEntity(t, units, "work", referencedID))
	store.SeedReference(plan.GenerationIDs[2])
	now := time.Now().UTC().Add(48 * time.Hour)

	require.NoError(t, og.NewCleanup(units).MarkExpiredTerminalGenerationAssets(t.Context(), now.Add(-og.TerminalAssetRetention), now))
	require.Equal(t, og.AssetDeletePending, assetStatus(t, store, plan.GenerationIDs[0]))
	require.Equal(t, og.AssetAllocated, assetStatus(t, store, plan.GenerationIDs[1]))
	require.Equal(t, og.AssetAllocated, assetStatus(t, store, plan.GenerationIDs[2]))
}

func TestMarkUnboundReadyAssetsKeepsGoingPastAFailedAsset(t *testing.T) {
	store, units := ogtest.New()
	now := time.Now().UTC()
	first, second := ogtest.ReadyAsset("og", now.Add(-32*24*time.Hour)), ogtest.ReadyAsset("og", now.Add(-31*24*time.Hour))
	store.SeedAsset(first)
	store.SeedAsset(second)
	store.Err["AssetReferenced"] = errStoreFailed

	err := og.NewCleanup(units).MarkUnboundReadyAssets(t.Context(), now.Add(-og.UnboundAssetRetention), now)
	require.ErrorIs(t, err, errStoreFailed)
	require.ErrorContains(t, err, first.ID)
	require.ErrorContains(t, err, second.ID)
	require.Equal(t, og.AssetReady, assetStatus(t, store, first.ID))

	store.Err["UnboundReadyAssetIDs"] = errStoreFailed
	err = og.NewCleanup(units).MarkUnboundReadyAssets(t.Context(), now.Add(-og.UnboundAssetRetention), now)
	require.ErrorContains(t, err, "list unbound OG assets")
}

func TestAssetProtectedOnlyGuardsOGAssets(t *testing.T) {
	store, _ := ogtest.New()
	image := ogtest.ReadyAsset("image", time.Now())
	store.SeedReference(image.ID)
	protected, err := og.AssetProtected(t.Context(), store, image)
	require.NoError(t, err)
	require.False(t, protected)
	asset := ogtest.ReadyAsset("og", time.Now())
	store.SeedReference(asset.ID)
	protected, err = og.AssetProtected(t.Context(), store, asset)
	require.NoError(t, err)
	require.True(t, protected)
}
