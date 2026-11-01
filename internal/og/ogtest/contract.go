package ogtest

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/og"
)

// Harness is one Store implementation the contract runs against, with the
// rows OG reads but other domains own.
type Harness struct {
	mutationtest.Harness[og.Store]
	// SeedFile stores a source file and returns its ID.
	SeedFile func(t *testing.T) string
	// SeedReadyAsset stores a ready public asset of kind, made from
	// sourceFileID unless it is empty, created and ready at at.
	SeedReadyAsset func(t *testing.T, kind, sourceFileID string, at time.Time) og.Asset
	// SeedBinding binds assetID to an owner outside OG.
	SeedBinding func(t *testing.T, assetID string)
	// SeedReference makes an entity row point at assetID.
	SeedReference func(t *testing.T, assetID string)
}

// FakeHarness is the Harness of a fresh in-memory Store.
func FakeHarness() Harness {
	store := NewStore()
	return Harness{
		Harness:  mutationtest.FakeHarness[og.Store](store),
		SeedFile: func(*testing.T) string { return uuid.NewString() },
		SeedReadyAsset: func(_ *testing.T, kind, sourceFileID string, at time.Time) og.Asset {
			asset := ReadyAsset(kind, at)
			if sourceFileID != "" {
				asset.SourceFileID = &sourceFileID
			}
			store.SeedAsset(asset)
			return asset
		},
		SeedBinding:   func(_ *testing.T, assetID string) { store.SeedBinding(assetID) },
		SeedReference: func(_ *testing.T, assetID string) { store.SeedReference(assetID) },
	}
}

// ReadyAsset is a ready inline public asset of kind.
func ReadyAsset(kind string, at time.Time) og.Asset {
	id := uuid.NewString()
	digest := sha256.Sum256([]byte(id))
	return og.Asset{
		ID: id, Kind: kind, Extension: "webp", MimeType: "image/webp", Disposition: "inline",
		FileSize: new(int64(1024)), SHA256: digest[:], Status: og.AssetReady, ReadyAt: new(at), CreatedAt: at,
	}
}

// Revision is the render config revision of a snapshot.
func Revision(snapshot []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(snapshot))
}

// RunStoreContract runs every Store contract case against a fresh Harness,
// in parallel.
func RunStoreContract(t *testing.T, open func(t *testing.T) Harness) {
	t.Helper()
	for name, run := range storeContract {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			run(t, open(t))
		})
	}
}

// now is a time both stores keep to the microsecond.
func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

func transact(t *testing.T, h Harness, work func(ctx context.Context, store og.Store) error) error {
	t.Helper()
	return mutationtest.Transact(t, h.Harness, work)
}

func localeTarget(entityID, locale string, at time.Time) og.GenerationTarget {
	return og.GenerationTarget{
		ID: uuid.NewString(), EntityType: "work", EntityID: entityID, TargetKind: og.KindLocale,
		Locale: &locale, CreatedAt: at, UpdatedAt: at,
	}
}

func newRun(at time.Time) og.Run {
	snapshot := []byte(`{"site_title":"Contract"}`)
	return og.Run{
		ID: uuid.NewString(), TriggerKind: "manual", Reason: "contract", RenderConfigSnapshot: snapshot,
		ConfigRevision: Revision(snapshot), Status: og.RunQueued, CreatedAt: at, UpdatedAt: at,
	}
}

func newGeneration(runID, targetID string, at time.Time) og.NewGeneration {
	id := uuid.NewString()
	return og.NewGeneration{
		Generation: og.Generation{
			ID: id, RunID: runID, TargetID: targetID, Status: og.StatusQueued,
			EntitySnapshot: []byte(`{"entity_type":"work"}`), DeadlineAt: at.Add(30 * time.Minute),
			CreatedAt: at, UpdatedAt: at,
		},
		Output: og.Output{AssetID: id, ObjectKey: "asset/" + id + ".webp", Extension: "webp", MimeType: "image/webp"},
	}
}

// planned is a run with one queued generation per target, each pointed at
// by its target.
type planned struct {
	run         og.Run
	targets     []og.GenerationTarget
	generations []og.Generation
}

func plan(t *testing.T, h Harness, at time.Time, targets ...og.GenerationTarget) planned {
	t.Helper()
	result := planned{run: newRun(at)}
	require.NoError(t, transact(t, h, func(ctx context.Context, store og.Store) error {
		locked, err := store.LockTargets(ctx, targets)
		if err != nil {
			return err
		}
		if err := store.InsertRun(ctx, result.run); err != nil {
			return err
		}
		generations := make([]og.NewGeneration, len(locked))
		pointers := make([]og.TargetPointer, len(locked))
		for i, target := range locked {
			generations[i] = newGeneration(result.run.ID, target.ID, at)
			pointers[i] = og.TargetPointer{TargetID: target.ID, GenerationID: generations[i].ID}
			result.generations = append(result.generations, generations[i].Generation)
		}
		if err := store.InsertGenerations(ctx, generations); err != nil {
			return err
		}
		updated, err := store.PointTargets(ctx, pointers, at)
		if err != nil {
			return err
		}
		require.EqualValues(t, len(pointers), updated)
		result.targets = locked
		return nil
	}))
	return result
}

func readGeneration(t *testing.T, h Harness, id string) og.Generation {
	t.Helper()
	var generation og.Generation
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store og.Store) (err error) {
		generation, err = store.Generation(ctx, id)
		return err
	}))
	return generation
}

func readRun(t *testing.T, h Harness, id string) og.Run {
	t.Helper()
	var run og.Run
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store og.Store) (err error) {
		run, err = store.Run(ctx, id)
		return err
	}))
	return run
}

func readTarget(t *testing.T, h Harness, id string) og.GenerationTarget {
	t.Helper()
	var target og.GenerationTarget
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store og.Store) (err error) {
		target, err = store.Target(ctx, id)
		return err
	}))
	return target
}

func change(t *testing.T, h Harness, apply func(ctx context.Context, store og.Store) (bool, error)) bool {
	t.Helper()
	var changed bool
	require.NoError(t, transact(t, h, func(ctx context.Context, store og.Store) (err error) {
		changed, err = apply(ctx, store)
		return err
	}))
	return changed
}

func start(t *testing.T, h Harness, generationID string, at time.Time) string {
	t.Helper()
	lease := uuid.NewString()
	require.NoError(t, transact(t, h, func(ctx context.Context, store og.Store) error {
		return store.StartGeneration(ctx, generationID, lease, at.Add(10*time.Minute), at)
	}))
	return lease
}

var storeContract = map[string]func(t *testing.T, h Harness){
	"locked targets are created once and keep their identity": func(t *testing.T, h Harness) {
		at, entityID := now(), uuid.NewString()
		label := og.GenerationTarget{
			ID: uuid.NewString(), EntityType: "label", EntityID: entityID, TargetKind: og.KindEntity, CreatedAt: at, UpdatedAt: at,
		}
		first := []og.GenerationTarget{localeTarget(entityID, "en", at), localeTarget(entityID, "ko", at), label}
		var locked []og.GenerationTarget
		require.NoError(t, transact(t, h, func(ctx context.Context, store og.Store) (err error) {
			locked, err = store.LockTargets(ctx, first)
			return err
		}))
		require.ElementsMatch(t, targetIDs(first), targetIDs(locked))

		second := []og.GenerationTarget{localeTarget(entityID, "en", at), localeTarget(entityID, "ko", at), label}
		second[2].ID = uuid.NewString()
		require.NoError(t, transact(t, h, func(ctx context.Context, store og.Store) (err error) {
			locked, err = store.LockTargets(ctx, second)
			return err
		}))
		require.ElementsMatch(t, targetIDs(first), targetIDs(locked))
		for _, target := range locked {
			if target.Locale == nil {
				require.Equal(t, og.KindEntity, target.TargetKind)
				require.Equal(t, label.ID, target.ID)
			}
		}
	},
	"a run of thousands of targets plans in one transaction": func(t *testing.T, h Harness) {
		at := now()
		targets := make([]og.GenerationTarget, 3200)
		for i := range targets {
			targets[i] = localeTarget(uuid.NewString(), "en", at)
		}
		planned := plan(t, h, at, targets...)
		require.Len(t, planned.generations, len(targets))
		var counts map[og.GenerationStatus]int64
		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store og.Store) (err error) {
			counts, err = store.RunStatusCounts(ctx, planned.run.ID)
			return err
		}))
		require.Equal(t, map[og.GenerationStatus]int64{og.StatusQueued: int64(len(targets))}, counts)
	},
	"pointing targets moves only existing targets": func(t *testing.T, h Harness) {
		at := now()
		first := plan(t, h, at, localeTarget(uuid.NewString(), "en", at))
		second := plan(t, h, at, localeTarget(first.targets[0].EntityID, "ko", at))
		updated := int64(0)
		require.NoError(t, transact(t, h, func(ctx context.Context, store og.Store) (err error) {
			updated, err = store.PointTargets(ctx, []og.TargetPointer{
				{TargetID: first.targets[0].ID, GenerationID: second.generations[0].ID},
				{TargetID: uuid.NewString(), GenerationID: second.generations[0].ID},
			}, at)
			return err
		}))
		require.EqualValues(t, 1, updated)
		require.Equal(t, second.generations[0].ID, *readTarget(t, h, first.targets[0].ID).LatestGenerationID)

		require.NoError(t, transact(t, h, func(ctx context.Context, store og.Store) error {
			return store.ClearTargetPointer(ctx, first.targets[0].ID, at)
		}))
		require.Nil(t, readTarget(t, h, first.targets[0].ID).LatestGenerationID)
	},
	"claiming the latest target matches only its latest generation": func(t *testing.T, h Harness) {
		at := now()
		planned := plan(t, h, at, localeTarget(uuid.NewString(), "en", at))
		target := planned.targets[0]
		require.True(t, change(t, h, func(ctx context.Context, store og.Store) (bool, error) {
			return store.ClaimLatestTarget(ctx, target.ID, planned.generations[0].ID, at)
		}))
		require.False(t, change(t, h, func(ctx context.Context, store og.Store) (bool, error) {
			return store.ClaimLatestTarget(ctx, target.ID, uuid.NewString(), at)
		}))
	},
	"a target is found by its entity, kind and locale": func(t *testing.T, h Harness) {
		at, entityID := now(), uuid.NewString()
		planned := plan(t, h, at, localeTarget(entityID, "en", at))
		find := func(key og.TargetKey) (og.GenerationTarget, error) {
			var target og.GenerationTarget
			err := mutationtest.Read(t, h.Harness, func(ctx context.Context, store og.Store) (err error) {
				target, err = store.FindTarget(ctx, key)
				return err
			})
			return target, err
		}
		found, err := find(og.TargetKey{EntityType: "work", EntityID: entityID, Kind: og.KindLocale, Locale: new("en")})
		require.NoError(t, err)
		require.Equal(t, planned.targets[0].ID, found.ID)
		_, err = find(og.TargetKey{EntityType: "work", EntityID: entityID, Kind: og.KindLocale, Locale: new("ko")})
		require.ErrorIs(t, err, og.ErrTargetNotFound)
		_, err = find(og.TargetKey{EntityType: "work", EntityID: entityID, Kind: og.KindEntity})
		require.ErrorIs(t, err, og.ErrTargetNotFound)
		err = mutationtest.Read(t, h.Harness, func(ctx context.Context, store og.Store) error {
			_, err := store.Target(ctx, uuid.NewString())
			return err
		})
		require.ErrorIs(t, err, og.ErrTargetNotFound)
	},
	"a generation locks with its target and a missing one is not found": func(t *testing.T, h Harness) {
		at := now()
		planned := plan(t, h, at, localeTarget(uuid.NewString(), "en", at))
		require.NoError(t, transact(t, h, func(ctx context.Context, store og.Store) error {
			generation, target, err := store.LockGeneration(ctx, planned.generations[0].ID)
			require.NoError(t, err)
			require.Equal(t, planned.targets[0].ID, target.ID)
			require.Equal(t, target.ID, generation.TargetID)
			require.Equal(t, og.StatusQueued, generation.Status)
			require.Equal(t, planned.run.ID, generation.RunID)
			require.True(t, generation.DeadlineAt.Equal(at.Add(30*time.Minute)))
			require.Positive(t, generation.RequestSequence)
			return nil
		}))
		err := transact(t, h, func(ctx context.Context, store og.Store) error {
			_, _, err := store.LockGeneration(ctx, uuid.NewString())
			return err
		})
		require.ErrorIs(t, err, og.ErrGenerationNotFound)
		err = mutationtest.Read(t, h.Harness, func(ctx context.Context, store og.Store) error {
			_, err := store.Generation(ctx, uuid.NewString())
			return err
		})
		require.ErrorIs(t, err, og.ErrGenerationNotFound)
	},
	"a locked target makes LockGeneration wait for its commit": func(t *testing.T, h Harness) {
		at := now()
		planned := plan(t, h, at, localeTarget(uuid.NewString(), "en", at))
		target := planned.targets[0]
		var seen og.GenerationTarget
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store og.Store) error {
				_, err := store.LockTargets(ctx, []og.GenerationTarget{localeTarget(target.EntityID, "en", at)})
				return err
			},
			func(ctx context.Context, store og.Store) error { return store.ClearTargetPointer(ctx, target.ID, at) },
			func(ctx context.Context, store og.Store) (err error) {
				_, seen, err = store.LockGeneration(ctx, planned.generations[0].ID)
				return err
			})
		require.NoError(t, err)
		require.Nil(t, seen.LatestGenerationID)
	},
	"a locked generation makes LockGeneration wait for its commit": func(t *testing.T, h Harness) {
		at := now()
		planned := plan(t, h, at, localeTarget(uuid.NewString(), "en", at))
		var seen og.Generation
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store og.Store) error {
				_, err := store.LockActiveGenerations(ctx, planned.targets[0].ID)
				return err
			},
			func(ctx context.Context, store og.Store) error {
				cancelled, err := store.MarkGenerationCancelled(ctx, planned.generations[0].ID, og.StatusQueued, at)
				require.True(t, cancelled)
				return err
			},
			func(ctx context.Context, store og.Store) (err error) {
				seen, _, err = store.LockGeneration(ctx, planned.generations[0].ID)
				return err
			})
		require.NoError(t, err)
		require.Equal(t, og.StatusCancelled, seen.Status)
	},
	"a generation's lock makes a planner locking its target wait": func(t *testing.T, h Harness) {
		at := now()
		planned := plan(t, h, at, localeTarget(uuid.NewString(), "en", at))
		target := planned.targets[0]
		var seen []og.GenerationTarget
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store og.Store) error {
				_, _, err := store.LockGeneration(ctx, planned.generations[0].ID)
				return err
			},
			func(ctx context.Context, store og.Store) error { return store.ClearTargetPointer(ctx, target.ID, at) },
			func(ctx context.Context, store og.Store) (err error) {
				seen, err = store.LockTargets(ctx, []og.GenerationTarget{localeTarget(target.EntityID, "en", at)})
				return err
			})
		require.NoError(t, err)
		require.Len(t, seen, 1)
		require.Nil(t, seen[0].LatestGenerationID)
	},
	"a processing generation becomes ready only with its lease": func(t *testing.T, h Harness) {
		at := now()
		id := plan(t, h, at, localeTarget(uuid.NewString(), "en", at)).generations[0].ID
		lease := start(t, h, id, at)
		started := readGeneration(t, h, id)
		require.Equal(t, og.StatusProcessing, started.Status)
		require.Equal(t, lease, *started.LeaseToken)
		require.True(t, started.LeaseExpiresAt.Equal(at.Add(10*time.Minute)))
		require.Nil(t, started.LastErrorCode)

		require.False(t, change(t, h, func(ctx context.Context, store og.Store) (bool, error) {
			return store.MarkGenerationReady(ctx, id, uuid.NewString(), at)
		}))
		require.True(t, change(t, h, func(ctx context.Context, store og.Store) (bool, error) {
			return store.MarkGenerationReady(ctx, id, lease, at)
		}))
		ready := readGeneration(t, h, id)
		require.Equal(t, og.StatusReady, ready.Status)
		require.True(t, ready.ReadyAt.Equal(at))
		require.True(t, ready.CompletedAt.Equal(at))
		require.Equal(t, lease, *ready.LeaseToken)
		require.False(t, change(t, h, func(ctx context.Context, store og.Store) (bool, error) {
			return store.MarkGenerationReady(ctx, id, lease, at)
		}))
	},
	"a generation fails or cancels only from the status it was read in": func(t *testing.T, h Harness) {
		at := now()
		planned := plan(t, h, at, localeTarget(uuid.NewString(), "en", at), localeTarget(uuid.NewString(), "en", at))
		failing, cancelling := planned.generations[0].ID, planned.generations[1].ID
		start(t, h, failing, at)
		require.False(t, change(t, h, func(ctx context.Context, store og.Store) (bool, error) {
			return store.MarkGenerationFailed(ctx, failing, og.StatusQueued, og.FailureCodeProcessingFailed, at)
		}))
		require.True(t, change(t, h, func(ctx context.Context, store og.Store) (bool, error) {
			return store.MarkGenerationFailed(ctx, failing, og.StatusProcessing, og.FailureCodeProcessingFailed, at)
		}))
		failed := readGeneration(t, h, failing)
		require.Equal(t, og.StatusFailed, failed.Status)
		require.Equal(t, og.FailureCodeProcessingFailed, *failed.LastErrorCode)
		require.Nil(t, failed.LeaseToken)
		require.Nil(t, failed.LeaseExpiresAt)
		require.True(t, failed.FailedAt.Equal(at))

		require.False(t, change(t, h, func(ctx context.Context, store og.Store) (bool, error) {
			return store.MarkGenerationCancelled(ctx, cancelling, og.StatusProcessing, at)
		}))
		require.True(t, change(t, h, func(ctx context.Context, store og.Store) (bool, error) {
			return store.MarkGenerationCancelled(ctx, cancelling, og.StatusQueued, at)
		}))
		cancelled := readGeneration(t, h, cancelling)
		require.Equal(t, og.StatusCancelled, cancelled.Status)
		require.True(t, cancelled.CancelledAt.Equal(at))
		require.True(t, cancelled.CompletedAt.Equal(at))
	},
	"superseding keeps a processing lease and names the replacement": func(t *testing.T, h Harness) {
		at := now()
		target := localeTarget(uuid.NewString(), "en", at)
		first := plan(t, h, at, target)
		second := plan(t, h, at, localeTarget(target.EntityID, "en", at))
		lease := start(t, h, first.generations[0].ID, at)
		replacement := second.generations[0].ID
		require.True(t, change(t, h, func(ctx context.Context, store og.Store) (bool, error) {
			return store.MarkGenerationSuperseded(ctx, first.generations[0].ID, &replacement, false, at)
		}))
		superseded := readGeneration(t, h, first.generations[0].ID)
		require.Equal(t, og.StatusSuperseded, superseded.Status)
		require.Equal(t, lease, *superseded.LeaseToken)
		require.Equal(t, replacement, *superseded.SupersededByID)
		require.True(t, superseded.SupersededAt.Equal(at))
		require.False(t, change(t, h, func(ctx context.Context, store og.Store) (bool, error) {
			return store.MarkGenerationSuperseded(ctx, first.generations[0].ID, nil, true, at)
		}))

		require.True(t, change(t, h, func(ctx context.Context, store og.Store) (bool, error) {
			return store.MarkGenerationSuperseded(ctx, replacement, nil, true, at)
		}))
		queued := readGeneration(t, h, replacement)
		require.Nil(t, queued.SupersededByID)
		require.Nil(t, queued.LeaseToken)
	},
	"a replaced generation is superseded while queued or processing on its own target": func(t *testing.T, h Harness) {
		at := now()
		target := localeTarget(uuid.NewString(), "en", at)
		queued := plan(t, h, at, target)
		processing := plan(t, h, at, localeTarget(target.EntityID, "en", at))
		replacement := plan(t, h, at, localeTarget(target.EntityID, "en", at)).generations[0].ID
		targetID := queued.targets[0].ID
		lease := start(t, h, processing.generations[0].ID, at)

		require.False(t, change(t, h, func(ctx context.Context, store og.Store) (bool, error) {
			return store.SupersedeReplacedGeneration(ctx, uuid.NewString(), queued.generations[0].ID, replacement, at)
		}))
		for _, generationID := range []string{queued.generations[0].ID, processing.generations[0].ID} {
			require.True(t, change(t, h, func(ctx context.Context, store og.Store) (bool, error) {
				return store.SupersedeReplacedGeneration(ctx, targetID, generationID, replacement, at)
			}))
			superseded := readGeneration(t, h, generationID)
			require.Equal(t, og.StatusSuperseded, superseded.Status)
			require.Equal(t, replacement, *superseded.SupersededByID)
		}
		require.Equal(t, lease, *readGeneration(t, h, processing.generations[0].ID).LeaseToken)
		require.False(t, change(t, h, func(ctx context.Context, store og.Store) (bool, error) {
			return store.SupersedeReplacedGeneration(ctx, targetID, queued.generations[0].ID, replacement, at)
		}))
	},
	"a run starts once and settles from created_at when it never started": func(t *testing.T, h Harness) {
		at := now()
		started := plan(t, h, at, localeTarget(uuid.NewString(), "en", at)).run
		later := at.Add(time.Minute)
		require.NoError(t, transact(t, h, func(ctx context.Context, store og.Store) error {
			return store.StartRun(ctx, started.ID, at)
		}))
		require.NoError(t, transact(t, h, func(ctx context.Context, store og.Store) error {
			return store.StartRun(ctx, started.ID, later)
		}))
		run := readRun(t, h, started.ID)
		require.Equal(t, og.RunRunning, run.Status)
		require.True(t, run.StartedAt.Equal(at))
		require.Equal(t, started.ConfigRevision, run.ConfigRevision)
		require.JSONEq(t, string(started.RenderConfigSnapshot), string(run.RenderConfigSnapshot))

		require.NoError(t, transact(t, h, func(ctx context.Context, store og.Store) error {
			return store.SettleRun(ctx, started.ID, og.RunReady, &later, later)
		}))
		run = readRun(t, h, started.ID)
		require.Equal(t, og.RunReady, run.Status)
		require.True(t, run.StartedAt.Equal(at))
		require.True(t, run.CompletedAt.Equal(later))

		queued := plan(t, h, at, localeTarget(uuid.NewString(), "en", at)).run
		require.NoError(t, transact(t, h, func(ctx context.Context, store og.Store) error {
			return store.SettleRun(ctx, queued.ID, og.RunRunning, nil, later)
		}))
		run = readRun(t, h, queued.ID)
		require.Equal(t, og.RunRunning, run.Status)
		require.True(t, run.StartedAt.Equal(queued.CreatedAt))
		require.Nil(t, run.CompletedAt)

		err := transact(t, h, func(ctx context.Context, store og.Store) error { return store.LockRun(ctx, uuid.NewString()) })
		require.ErrorIs(t, err, og.ErrRunNotFound)
		err = mutationtest.Read(t, h.Harness, func(ctx context.Context, store og.Store) error {
			_, err := store.Run(ctx, uuid.NewString())
			return err
		})
		require.ErrorIs(t, err, og.ErrRunNotFound)
	},
	"a locked run makes a second refresh wait and count the first commit": func(t *testing.T, h Harness) {
		at := now()
		planned := plan(t, h, at, localeTarget(uuid.NewString(), "en", at), localeTarget(uuid.NewString(), "en", at))
		var counts map[og.GenerationStatus]int64
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store og.Store) error { return store.LockRun(ctx, planned.run.ID) },
			func(ctx context.Context, store og.Store) error {
				_, err := store.MarkGenerationCancelled(ctx, planned.generations[0].ID, og.StatusQueued, at)
				return err
			},
			func(ctx context.Context, store og.Store) (err error) {
				if err := store.LockRun(ctx, planned.run.ID); err != nil {
					return err
				}
				counts, err = store.RunStatusCounts(ctx, planned.run.ID)
				return err
			})
		require.NoError(t, err)
		require.Equal(t, map[og.GenerationStatus]int64{og.StatusCancelled: 1, og.StatusQueued: 1}, counts)
	},
	"an entity's targets lock in locale order and active generations in request order": func(t *testing.T, h Harness) {
		at, entityID := now(), uuid.NewString()
		first := plan(t, h, at, localeTarget(entityID, "ko", at), localeTarget(entityID, "en", at))
		second := plan(t, h, at, localeTarget(entityID, "en", at))
		plan(t, h, at, localeTarget(uuid.NewString(), "en", at))
		enTarget := second.targets[0].ID
		cancelled := first.generations[0].ID
		if first.targets[0].ID == enTarget {
			cancelled = first.generations[1].ID
		}
		require.True(t, change(t, h, func(ctx context.Context, store og.Store) (bool, error) {
			return store.MarkGenerationCancelled(ctx, cancelled, og.StatusQueued, at)
		}))
		require.NoError(t, transact(t, h, func(ctx context.Context, store og.Store) error {
			targets, err := store.LockEntityTargets(ctx, "work", entityID)
			require.NoError(t, err)
			require.Len(t, targets, 2)
			require.Equal(t, "en", *targets[0].Locale)
			require.Equal(t, "ko", *targets[1].Locale)
			active, err := store.LockActiveGenerations(ctx, enTarget)
			require.NoError(t, err)
			var ids []string
			for _, generation := range active {
				ids = append(ids, generation.ID)
			}
			var want []string
			for _, generation := range append(slices.Clone(first.generations), second.generations...) {
				if generation.TargetID == enTarget && generation.ID != cancelled {
					want = append(want, generation.ID)
				}
			}
			require.Equal(t, want, ids)
			return nil
		}))
	},
	"an OG output completes once and renders a CDN reference": func(t *testing.T, h Harness) {
		at := now()
		id := plan(t, h, at, localeTarget(uuid.NewString(), "en", at)).generations[0].ID
		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store og.Store) error {
			asset, found, err := store.Asset(ctx, id)
			require.True(t, found)
			require.Equal(t, og.AssetAllocated, asset.Status)
			require.Equal(t, "og", asset.Kind)
			return err
		}))
		digest := sha256.Sum256([]byte(id))
		written := og.WrittenAsset{AssetID: id, FileSize: 2048, SHA256: digest[:]}
		var ref og.AssetRef
		require.NoError(t, transact(t, h, func(ctx context.Context, store og.Store) error {
			asset, err := store.CompleteAsset(ctx, written)
			if err != nil {
				return err
			}
			require.Equal(t, og.AssetReady, asset.Status)
			ref, err = store.AssetRef(asset)
			return err
		}))
		require.Equal(t, id, ref.AssetID)
		require.True(t, strings.HasPrefix(ref.URL, "https://cdn.example.com/"), ref.URL)
		require.Contains(t, ref.URL, id)
		require.EqualValues(t, 2048, ref.FileSize)
		require.Equal(t, digest[:], ref.SHA256)
		require.NoError(t, transact(t, h, func(ctx context.Context, store og.Store) error {
			_, err := store.CompleteAsset(ctx, written)
			return err
		}))
		err := transact(t, h, func(ctx context.Context, store og.Store) error {
			_, err := store.CompleteAsset(ctx, og.WrittenAsset{AssetID: id, FileSize: 4096, SHA256: digest[:]})
			return err
		})
		require.Error(t, err)
	},
	"a source file resolves to its newest ready asset": func(t *testing.T, h Harness) {
		at := now()
		fileID, other := h.SeedFile(t), h.SeedFile(t)
		h.SeedReadyAsset(t, "image", fileID, at.Add(-time.Hour))
		newest := h.SeedReadyAsset(t, "image", fileID, at)
		var refs map[string]og.AssetRef
		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store og.Store) (err error) {
			refs, err = store.ReadyAssetsForFiles(ctx, []string{fileID, other})
			return err
		}))
		require.Len(t, refs, 1)
		require.Equal(t, newest.ID, refs[fileID].AssetID)
	},
	"cleanup lists ready OG assets past the cutoff that nothing binds": func(t *testing.T, h Harness) {
		at := now()
		cutoff := at.Add(-30 * 24 * time.Hour)
		old := at.Add(-31 * 24 * time.Hour)
		unbound, bound := h.SeedReadyAsset(t, "og", "", old), h.SeedReadyAsset(t, "og", "", old)
		young, image := h.SeedReadyAsset(t, "og", "", at.Add(-29*24*time.Hour)), h.SeedReadyAsset(t, "image", "", old)
		h.SeedBinding(t, bound.ID)
		var listed []string
		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store og.Store) (err error) {
			listed, err = store.UnboundReadyAssetIDs(ctx, cutoff)
			return err
		}))
		require.Contains(t, listed, unbound.ID)
		require.NotContains(t, listed, bound.ID)
		require.NotContains(t, listed, young.ID)
		require.NotContains(t, listed, image.ID)

		require.NoError(t, transact(t, h, func(ctx context.Context, store og.Store) error {
			isBound, err := store.AssetBound(ctx, bound.ID)
			require.True(t, isBound)
			return err
		}))
		require.False(t, change(t, h, func(ctx context.Context, store og.Store) (bool, error) {
			return store.MarkUnboundAssetDeletePending(ctx, young.ID, cutoff, at)
		}))
		require.True(t, change(t, h, func(ctx context.Context, store og.Store) (bool, error) {
			return store.MarkUnboundAssetDeletePending(ctx, unbound.ID, cutoff, at)
		}))
		require.False(t, change(t, h, func(ctx context.Context, store og.Store) (bool, error) {
			return store.MarkUnboundAssetDeletePending(ctx, unbound.ID, cutoff, at)
		}))
		require.NoError(t, transact(t, h, func(ctx context.Context, store og.Store) error {
			asset, found, err := store.LockAsset(ctx, unbound.ID)
			require.True(t, found)
			require.Equal(t, og.AssetDeletePending, asset.Status)
			_, found, _ = store.LockAsset(ctx, uuid.NewString())
			require.False(t, found)
			return err
		}))
	},
	"cleanup lists the expired outputs of terminal generations": func(t *testing.T, h Harness) {
		at := now()
		planned := plan(t, h, at, localeTarget(uuid.NewString(), "en", at), localeTarget(uuid.NewString(), "en", at))
		cancelled, queued := planned.generations[0].ID, planned.generations[1].ID
		require.True(t, change(t, h, func(ctx context.Context, store og.Store) (bool, error) {
			return store.MarkGenerationCancelled(ctx, cancelled, og.StatusQueued, at)
		}))
		cutoff := at.Add(time.Hour)
		var listed []string
		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store og.Store) (err error) {
			listed, err = store.ExpiredTerminalAssetIDs(ctx, cutoff)
			return err
		}))
		require.Equal(t, []string{cancelled}, listed)
		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store og.Store) (err error) {
			listed, err = store.ExpiredTerminalAssetIDs(ctx, at)
			return err
		}))
		require.Empty(t, listed)

		require.False(t, change(t, h, func(ctx context.Context, store og.Store) (bool, error) {
			return store.MarkTerminalAssetDeletePending(ctx, cancelled, at, at)
		}))
		require.True(t, change(t, h, func(ctx context.Context, store og.Store) (bool, error) {
			return store.MarkTerminalAssetDeletePending(ctx, cancelled, cutoff, at)
		}))
		require.False(t, change(t, h, func(ctx context.Context, store og.Store) (bool, error) {
			return store.MarkTerminalAssetDeletePending(ctx, cancelled, cutoff, at)
		}))
		require.NotEmpty(t, queued)
	},
	"an asset an entity row points at is referenced": func(t *testing.T, h Harness) {
		at := now()
		referenced, free := h.SeedReadyAsset(t, "og", "", at), h.SeedReadyAsset(t, "og", "", at)
		h.SeedReference(t, referenced.ID)
		require.NoError(t, transact(t, h, func(ctx context.Context, store og.Store) error {
			yes, err := store.AssetReferenced(ctx, referenced.ID)
			require.NoError(t, err)
			require.True(t, yes)
			no, err := store.AssetReferenced(ctx, free.ID)
			require.False(t, no)
			return err
		}))
	},
}

func targetIDs(targets []og.GenerationTarget) []string {
	ids := make([]string, len(targets))
	for i, target := range targets {
		ids[i] = target.ID
	}
	return ids
}
