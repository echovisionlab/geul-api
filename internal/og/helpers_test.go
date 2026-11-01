package og_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/og/ogtest"
)

type entitySnapshot struct {
	EntityType    string  `json:"entity_type"`
	EntityID      string  `json:"entity_id"`
	Title         string  `json:"title"`
	Locale        *string `json:"locale"`
	FeaturedImage *struct {
		AssetID string `json:"asset_id"`
		URL     string `json:"url"`
	} `json:"featured_image"`
	Output struct {
		AssetID   string `json:"asset_id"`
		ObjectKey string `json:"object_key"`
	} `json:"output"`
}

func snapshotOf(t *testing.T, generation og.Generation) entitySnapshot {
	t.Helper()
	var snapshot entitySnapshot
	require.NoError(t, json.Unmarshal(generation.EntitySnapshot, &snapshot))
	return snapshot
}

func localeRequest(entityType, entityID, title, locale string) og.Request {
	return og.Request{
		Target: og.Target{EntityType: entityType, EntityID: entityID, Locale: &locale, Kind: og.KindLocale}, Title: title,
	}
}

func entityRequest(entityType, entityID, title string) og.Request {
	return og.Request{Target: og.Target{EntityType: entityType, EntityID: entityID, Kind: og.KindEntity}, Title: title}
}

func sameRequests(requests []og.Request) og.Reload {
	return func(context.Context, og.Store) ([]og.Request, error) { return requests, nil }
}

func requestBulk(t *testing.T, units *mutationtest.UnitOfWork[og.Store], reason string, requests ...og.Request) (*og.Plan, error) {
	t.Helper()
	var plan *og.Plan
	err := units.Transaction(t.Context(), false, func(tx mutation.Tx[og.Store]) (err error) {
		plan, err = og.RequestBulk(t.Context(), tx.Store(), dbtime.Now(), "automatic", reason, requests, sameRequests(requests))
		return err
	})
	return plan, err
}

func mustPlan(t *testing.T, units *mutationtest.UnitOfWork[og.Store], requests ...og.Request) *og.Plan {
	t.Helper()
	plan, err := requestBulk(t, units, "test", requests...)
	require.NoError(t, err)
	return plan
}

func cancelEntity(t *testing.T, units *mutationtest.UnitOfWork[og.Store], entityType, entityID string) error {
	t.Helper()
	return units.Transaction(t.Context(), false, func(tx mutation.Tx[og.Store]) error {
		return og.CancelEntity(t.Context(), tx.Store(), dbtime.Now(), entityType, entityID)
	})
}

// leased puts a queued generation into processing as a renderer's claim
// would, with a lease expiring at leaseExpiresAt.
func leased(store *ogtest.Store, generationID string, leaseExpiresAt time.Time) string {
	lease := uuid.NewString()
	now := time.Now().UTC()
	store.UpdateGeneration(generationID, func(generation *og.Generation) {
		generation.Status = og.StatusProcessing
		generation.ProcessingAt, generation.LeaseToken, generation.LeaseExpiresAt = &now, &lease, &leaseExpiresAt
	})
	return lease
}

func written(generationID string) *og.WrittenAsset {
	digest := make([]byte, 32)
	digest[0] = 7
	return &og.WrittenAsset{AssetID: generationID, FileSize: 123, SHA256: digest}
}

func generation(t *testing.T, store *ogtest.Store, id string) og.Generation {
	t.Helper()
	stored, ok := store.StoredGeneration(id)
	require.True(t, ok, id)
	return stored
}

func run(t *testing.T, store *ogtest.Store, id string) og.Run {
	t.Helper()
	stored, ok := store.StoredRun(id)
	require.True(t, ok, id)
	return stored
}

func statuses(events []og.LifecycleEvent) []og.GenerationStatus {
	result := make([]og.GenerationStatus, len(events))
	for i, event := range events {
		result[i] = event.Status
	}
	return result
}

func txClear(t *testing.T, targetID string) func(mutation.Tx[og.Store]) error {
	return func(tx mutation.Tx[og.Store]) error {
		return tx.Store().ClearTargetPointer(t.Context(), targetID, time.Now().UTC())
	}
}

var errStoreFailed = errors.New("store failed")
