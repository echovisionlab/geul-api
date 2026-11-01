package legaltest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

// Harness is one legal.Store implementation the contract runs against,
// with the rows of other domains a case needs and a reader of the derived
// content the Store writes but never reads back.
type Harness struct {
	mutationtest.Harness[legal.Store]
	// SeedDocument stores an empty policy content document and returns its
	// ID.
	SeedDocument func(t *testing.T) string
	// Projection reads the derived content of a version, in its source
	// locale when locale is empty and else of that target locale.
	Projection func(t *testing.T, kind legal.PolicyKind, id, locale string) legal.Projection
}

// FakeHarness is the Harness of the fake Store.
func FakeHarness() Harness {
	store := NewStore()
	return Harness{
		Harness: mutationtest.FakeHarness[legal.Store](store),
		SeedDocument: func(*testing.T) string {
			return uuid.NewString()
		},
		Projection: func(t *testing.T, kind legal.PolicyKind, id, locale string) legal.Projection {
			if locale == "" {
				record, ok := store.Record(kind, id)
				require.True(t, ok)
				return record.Projection
			}
			record, ok := store.StoredTranslation(kind, id, locale)
			require.True(t, ok)
			return record.Projection
		},
	}
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

func transact(t *testing.T, h Harness, work func(ctx context.Context, store legal.Store) error) {
	t.Helper()
	require.NoError(t, mutationtest.Transact(t, h.Harness, work))
}

func text(value string) *string { return &value }

// kinds are both policy kinds; every case runs on each.
var kinds = []legal.PolicyKind{legal.Terms, legal.Privacy}

// insert stores a draft version of kind in its own transaction.
func insert(t *testing.T, h Harness, kind legal.PolicyKind, sealedVersion int) legal.Version {
	t.Helper()
	documentID := h.SeedDocument(t)
	var version legal.Version
	transact(t, h, func(ctx context.Context, store legal.Store) error {
		var err error
		version, err = store.InsertDraft(ctx, kind, legal.Draft{
			Title: "Draft " + string(kind), SealedVersion: sealedVersion, ContentDocumentID: documentID,
			SourceLocale: "en", CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		})
		return err
	})
	return version
}

func statusOf(kind legal.PolicyKind, state string) legal.Status {
	stored, _ := legal.PolicyStatus(kind, state)
	return stored
}

var storeContract = map[string]func(t *testing.T, h Harness){
	"a missing version answers its sentinels": func(t *testing.T, h Harness) {
		missing := uuid.NewString()
		for _, kind := range kinds {
			transact(t, h, func(ctx context.Context, store legal.Store) error {
				_, err := store.Version(ctx, kind, missing, legal.UpdateLock)
				require.ErrorIs(t, err, legal.ErrPolicyNotFound)
				_, err = store.Root(ctx, kind, missing, legal.ShareLock)
				require.ErrorIs(t, err, legal.ErrPolicyNotFound)
				_, err = store.LockStatus(ctx, kind, missing)
				require.ErrorIs(t, err, legal.ErrPolicyNotFound)
				_, err = store.ScheduleState(ctx, kind, missing)
				require.ErrorIs(t, err, legal.ErrPolicyNotFound)
				_, found, err := store.LockLifecycle(ctx, kind, missing)
				require.NoError(t, err)
				require.False(t, found)
				_, err = store.TranslationTitle(ctx, kind, missing, "ko")
				require.ErrorIs(t, err, legal.ErrTranslationNotFound)
				_, found, err = store.Translation(ctx, kind, missing, "ko", true)
				require.NoError(t, err)
				require.False(t, found)
				require.ErrorIs(t, store.Activate(ctx, kind, missing, []legal.Status{statusOf(kind, "draft")}, nil, time.Now()), legal.ErrPolicyNotFound)
				return nil
			})
		}
	},
	"a draft is numbered after both the history and the sealed notices": func(t *testing.T, h Harness) {
		for _, kind := range kinds {
			first := insert(t, h, kind, 0)
			require.Equal(t, 1, first.Version)
			require.Equal(t, statusOf(kind, "draft"), first.Status)
			require.Equal(t, "Draft "+string(kind), first.Title)
			require.NotNil(t, first.ContentDocumentID)
			require.Equal(t, 2, insert(t, h, kind, 0).Version)
			require.Equal(t, 8, insert(t, h, kind, 7).Version, "a sealed notice of a deleted version 7 is never renumbered")
			require.Equal(t, 9, insert(t, h, kind, 3).Version)
			transact(t, h, func(ctx context.Context, store legal.Store) error {
				root, err := store.Root(ctx, kind, first.ID, legal.UpdateLock)
				require.NoError(t, err)
				require.Equal(t, "en", root.SourceLocale)
				require.Equal(t, *first.ContentDocumentID, root.ContentDocumentID.String())
				require.Equal(t, 1, root.Version)
				read, err := store.Version(ctx, kind, first.ID, legal.NoLock)
				require.NoError(t, err)
				require.Equal(t, first.ID, read.ID)
				require.True(t, first.CreatedAt.Equal(read.CreatedAt))
				return nil
			})
		}
	},
	"a version moves through its lifecycle": func(t *testing.T, h Harness) {
		for _, kind := range kinds {
			version := insert(t, h, kind, 0)
			other := insert(t, h, kind, 0)
			effectiveFrom := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Microsecond)
			at := time.Now().UTC().Truncate(time.Microsecond)
			transact(t, h, func(ctx context.Context, store legal.Store) error {
				require.NoError(t, store.Schedule(ctx, kind, version.ID, effectiveFrom, at))
				state, err := store.ScheduleState(ctx, kind, version.ID)
				require.NoError(t, err)
				require.Equal(t, statusOf(kind, "scheduled"), state.Status)
				require.True(t, effectiveFrom.Equal(*state.EffectiveFrom))
				ids, err := store.LockScheduledIDs(ctx, kind, other.ID)
				require.NoError(t, err)
				require.Equal(t, []string{version.ID}, ids)
				ids, err = store.LockScheduledIDs(ctx, kind, version.ID)
				require.NoError(t, err)
				require.Empty(t, ids)
				lifecycle, found, err := store.LockLifecycle(ctx, kind, version.ID)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, version.Version, lifecycle.Version)
				require.True(t, effectiveFrom.Equal(*lifecycle.EffectiveFrom))

				require.NoError(t, store.Unschedule(ctx, kind, version.ID, at))
				read, err := store.Version(ctx, kind, version.ID, legal.NoLock)
				require.NoError(t, err)
				require.Equal(t, statusOf(kind, "draft"), read.Status)
				require.Nil(t, read.EffectiveFrom)
				require.True(t, at.Equal(read.UpdatedAt))

				require.ErrorIs(t, store.Activate(ctx, kind, version.ID, []legal.Status{statusOf(kind, "scheduled")}, nil, at),
					legal.ErrPolicyNotFound, "a version in another status does not activate")
				require.NoError(t, store.Activate(ctx, kind, other.ID, []legal.Status{statusOf(kind, "draft")}, &at, at))
				require.NoError(t, store.Activate(ctx, kind, version.ID, []legal.Status{statusOf(kind, "draft"), statusOf(kind, "scheduled")}, &at, at))
				active, err := store.LockActiveVersions(ctx, kind, version.ID)
				require.NoError(t, err)
				require.Equal(t, []legal.VersionRef{{ID: other.ID, Version: other.Version}}, active)
				require.NoError(t, store.Archive(ctx, kind, other.ID, at))
				archived, err := store.Version(ctx, kind, other.ID, legal.ShareLock)
				require.NoError(t, err)
				require.Equal(t, statusOf(kind, "archived"), archived.Status)
				require.True(t, at.Equal(*archived.EffectiveUntil))
				status, err := store.LockStatus(ctx, kind, version.ID)
				require.NoError(t, err)
				require.Equal(t, statusOf(kind, "active"), status)
				return nil
			})
		}
	},
	"versions list newest first and count every match": func(t *testing.T, h Harness) {
		for _, kind := range kinds {
			first, second, third := insert(t, h, kind, 0), insert(t, h, kind, 0), insert(t, h, kind, 0)
			transact(t, h, func(ctx context.Context, store legal.Store) error {
				require.NoError(t, store.Schedule(ctx, kind, second.ID, time.Now().Add(time.Hour), time.Now()))
				versions, total, err := store.ListVersions(ctx, kind, nil, 2, 0)
				require.NoError(t, err)
				require.EqualValues(t, 3, total)
				require.Equal(t, []string{third.ID, second.ID}, []string{versions[0].ID, versions[1].ID})
				versions, total, err = store.ListVersions(ctx, kind, nil, 2, 2)
				require.NoError(t, err)
				require.EqualValues(t, 3, total)
				require.Len(t, versions, 1)
				require.Equal(t, first.ID, versions[0].ID)
				draft := statusOf(kind, "draft")
				versions, total, err = store.ListVersions(ctx, kind, &draft, 20, 0)
				require.NoError(t, err)
				require.EqualValues(t, 2, total)
				require.Equal(t, []string{third.ID, first.ID}, []string{versions[0].ID, versions[1].ID})
				return nil
			})
		}
	},
	"a title is replaced only when it differs": func(t *testing.T, h Harness) {
		for _, kind := range kinds {
			version := insert(t, h, kind, 0)
			at := time.Now().UTC().Truncate(time.Microsecond)
			transact(t, h, func(ctx context.Context, store legal.Store) error {
				changed, err := store.ReplaceTitle(ctx, kind, version.ID, version.Title, at)
				require.NoError(t, err)
				require.False(t, changed)
				changed, err = store.ReplaceTitle(ctx, kind, version.ID, "Replaced", at)
				require.NoError(t, err)
				require.True(t, changed)
				require.NoError(t, store.SetTitle(ctx, kind, version.ID, "Set", at))
				root, err := store.Root(ctx, kind, version.ID, legal.ShareLock)
				require.NoError(t, err)
				require.Equal(t, "Set", root.Title)
				return nil
			})
		}
	},
	"target locale rows report what they changed and cascade with their version": func(t *testing.T, h Harness) {
		for _, kind := range kinds {
			version := insert(t, h, kind, 0)
			at := time.Now().UTC().Truncate(time.Microsecond)
			transact(t, h, func(ctx context.Context, store legal.Store) error {
				inserted, err := store.InsertTranslation(ctx, kind, version.ID, "ko", text("Korean"), at)
				require.NoError(t, err)
				require.EqualValues(t, 1, inserted)
				_, err = store.InsertTranslation(ctx, kind, version.ID, "ja", nil, at)
				require.NoError(t, err)
				count, err := store.CountTranslations(ctx, kind, version.ID, "ko")
				require.NoError(t, err)
				require.EqualValues(t, 1, count)
				row, found, err := store.Translation(ctx, kind, version.ID, "ko", true)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, legal.Translation{Locale: "ko", Title: text("Korean"), UpdatedAt: row.UpdatedAt}, row)
				require.True(t, at.Equal(row.UpdatedAt))
				later := at.Add(time.Second)
				updated, err := store.UpdateTranslation(ctx, kind, version.ID, "ko", nil, later)
				require.NoError(t, err)
				require.EqualValues(t, 1, updated)
				title, err := store.TranslationTitle(ctx, kind, version.ID, "ko")
				require.NoError(t, err)
				require.Nil(t, title)
				updated, err = store.UpdateTranslation(ctx, kind, version.ID, "fr", nil, later)
				require.NoError(t, err)
				require.Zero(t, updated)
				locales, err := store.TranslationLocales(ctx, kind, version.ID)
				require.NoError(t, err)
				require.ElementsMatch(t, []string{"ja", "ko"}, locales)
				deleted, err := store.DeleteTranslation(ctx, kind, version.ID, "ja")
				require.NoError(t, err)
				require.EqualValues(t, 1, deleted)
				deleted, err = store.DeleteTranslation(ctx, kind, version.ID, "ja")
				require.NoError(t, err)
				require.Zero(t, deleted)
				require.NoError(t, store.DeleteVersion(ctx, kind, version.ID))
				count, err = store.CountTranslations(ctx, kind, version.ID, "ko")
				require.NoError(t, err)
				require.Zero(t, count)
				return nil
			})
		}
	},
	"derived content is stored with its snapshot digest": func(t *testing.T, h Harness) {
		for _, kind := range kinds {
			version := insert(t, h, kind, 0)
			at := time.Now().UTC().Truncate(time.Microsecond)
			source := legal.Projection{HTML: "<p>source</p>", Text: "source", Digest: "digest-1"}
			target := legal.Projection{HTML: "<p>target</p>", Text: "target"}
			transact(t, h, func(ctx context.Context, store legal.Store) error {
				_, err := store.InsertTranslation(ctx, kind, version.ID, "ko", text("Korean"), at)
				require.NoError(t, err)
				require.NoError(t, store.StoreSourceProjection(ctx, kind, version.ID, source, at))
				return store.StoreTargetProjection(ctx, kind, version.ID, "ko", target)
			})
			require.Equal(t, source, h.Projection(t, kind, version.ID, ""))
			require.Equal(t, target, h.Projection(t, kind, version.ID, "ko"))
		}
	},
	"a failed transaction stores nothing": func(t *testing.T, h Harness) {
		failure := errors.New("rolled back")
		documentID := h.SeedDocument(t)
		var id string
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store legal.Store) error {
			version, err := store.InsertDraft(ctx, legal.Terms, legal.Draft{
				Title: "Rolled back", ContentDocumentID: documentID, SourceLocale: "en", CreatedAt: time.Now().UTC(),
			})
			require.NoError(t, err)
			id = version.ID
			return failure
		})
		require.ErrorIs(t, err, failure)
		transact(t, h, func(ctx context.Context, store legal.Store) error {
			_, err := store.Version(ctx, legal.Terms, id, legal.NoLock)
			require.ErrorIs(t, err, legal.ErrPolicyNotFound)
			return nil
		})
	},
	"a version locked for update makes a second writer wait for the first commit": func(t *testing.T, h Harness) {
		for _, kind := range kinds {
			version := insert(t, h, kind, 0)
			effectiveFrom := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
			err := mutationtest.RequireSecondWaits(t, h.Harness,
				func(ctx context.Context, store legal.Store) error {
					_, err := store.Version(ctx, kind, version.ID, legal.UpdateLock)
					return err
				},
				func(ctx context.Context, store legal.Store) error {
					return store.Schedule(ctx, kind, version.ID, effectiveFrom, time.Now())
				},
				func(ctx context.Context, store legal.Store) error {
					status, err := store.LockStatus(ctx, kind, version.ID)
					require.Equal(t, statusOf(kind, "scheduled"), status, "the second writer reads the committed status")
					return err
				},
			)
			require.NoError(t, err)
		}
	},
	"scheduled versions locked for update make a second scheduler wait": func(t *testing.T, h Harness) {
		for _, kind := range kinds {
			scheduled, waiting := insert(t, h, kind, 0), insert(t, h, kind, 0)
			transact(t, h, func(ctx context.Context, store legal.Store) error {
				return store.Schedule(ctx, kind, scheduled.ID, time.Now().Add(time.Hour), time.Now())
			})
			err := mutationtest.RequireSecondWaits(t, h.Harness,
				func(ctx context.Context, store legal.Store) error {
					_, err := store.LockScheduledIDs(ctx, kind, waiting.ID)
					return err
				},
				func(ctx context.Context, store legal.Store) error {
					return store.Unschedule(ctx, kind, scheduled.ID, time.Now())
				},
				func(ctx context.Context, store legal.Store) error {
					ids, err := store.LockScheduledIDs(ctx, kind, waiting.ID)
					require.Empty(t, ids, "the second scheduler sees the slot the first one freed")
					return err
				},
			)
			require.NoError(t, err)
		}
	},
	"version numbering waits for the version being assigned": func(t *testing.T, h Harness) {
		for _, kind := range kinds {
			documentID := h.SeedDocument(t)
			err := mutationtest.RequireSecondWaits(t, h.Harness,
				func(ctx context.Context, store legal.Store) error {
					return store.LockVersionAssignment(ctx, kind)
				},
				func(ctx context.Context, store legal.Store) error {
					_, err := store.InsertDraft(ctx, kind, legal.Draft{
						Title: "First", ContentDocumentID: documentID, SourceLocale: "en", CreatedAt: time.Now().UTC(),
					})
					return err
				},
				func(ctx context.Context, store legal.Store) error {
					return store.LockVersionAssignment(ctx, kind)
				},
			)
			require.NoError(t, err)
		}
	},
	"a target locale locked for update makes a second writer wait": func(t *testing.T, h Harness) {
		for _, kind := range kinds {
			version := insert(t, h, kind, 0)
			at := time.Now().UTC().Truncate(time.Microsecond)
			transact(t, h, func(ctx context.Context, store legal.Store) error {
				_, err := store.InsertTranslation(ctx, kind, version.ID, "ko", text("Before"), at)
				return err
			})
			err := mutationtest.RequireSecondWaits(t, h.Harness,
				func(ctx context.Context, store legal.Store) error {
					_, _, err := store.Translation(ctx, kind, version.ID, "ko", true)
					return err
				},
				func(ctx context.Context, store legal.Store) error {
					_, err := store.UpdateTranslation(ctx, kind, version.ID, "ko", text("After"), at.Add(time.Second))
					return err
				},
				func(ctx context.Context, store legal.Store) error {
					row, _, err := store.Translation(ctx, kind, version.ID, "ko", true)
					require.Equal(t, text("After"), row.Title)
					return err
				},
			)
			require.NoError(t, err)
		}
	},
}
