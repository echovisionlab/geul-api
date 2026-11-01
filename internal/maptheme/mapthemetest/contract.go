package mapthemetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/maptheme"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

// Harness is one Map Theme Store a contract case runs against.
type Harness struct {
	mutationtest.Harness[maptheme.Store]
	// SeedContributor makes a live, onboarded Member linked to its Identity
	// and returns both IDs.
	SeedContributor func(t *testing.T) (memberID, identityID string)
}

// FakeHarness is the Harness of a fresh in-memory Store.
func FakeHarness() Harness {
	store, _ := New()
	return Harness{
		Harness: mutationtest.FakeHarness[maptheme.Store](store),
		SeedContributor: func(*testing.T) (string, string) {
			memberID, identityID := uuid.NewString(), uuid.NewString()
			store.SeedContributor(memberID, identityID)
			return memberID, identityID
		},
	}
}

// RunStoreContract runs every Store contract case against a fresh Harness,
// in parallel. Its cases need a contributor seeder beside the unit of work,
// so it runs them the way mutationtest.RunContract does instead of through
// it.
func RunStoreContract(t *testing.T, open func(t *testing.T) Harness) {
	t.Helper()
	for name, run := range storeContract {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			run(t, open(t))
		})
	}
}

func insert(t *testing.T, h Harness, snapshot maptheme.Snapshot) maptheme.Theme {
	t.Helper()
	var theme maptheme.Theme
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store maptheme.Store) (err error) {
		theme, err = store.InsertTheme(ctx, snapshot)
		return err
	}))
	return theme
}

func read(t *testing.T, h Harness, id string) (maptheme.Theme, error) {
	t.Helper()
	var theme maptheme.Theme
	err := mutationtest.Read(t, h.Harness, func(ctx context.Context, store maptheme.Store) (err error) {
		theme, err = store.Theme(ctx, id)
		return err
	})
	return theme, err
}

func defaultThemeID(t *testing.T, h Harness) string {
	t.Helper()
	var id string
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store maptheme.Store) (err error) {
		id, err = store.DefaultThemeID(ctx)
		return err
	}))
	return id
}

func setDefault(t *testing.T, h Harness, id string) {
	t.Helper()
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store maptheme.Store) error {
		return store.SetDefaultThemeID(ctx, id)
	}))
}

func ids(themes []maptheme.Theme) []string {
	ids := make([]string, 0, len(themes))
	for _, theme := range themes {
		ids = append(ids, theme.ID)
	}
	return ids
}

// requireReadSurvivesWriter runs write in a second transaction while read
// holds its snapshot open: PostgreSQL commits the writer under the reader,
// and the in-memory unit of work makes the writer wait for it. Either way
// the reader sees the state before the writer.
func requireReadSurvivesWriter(
	t *testing.T, h Harness,
	before func(ctx context.Context, store maptheme.Store) error,
	write func(ctx context.Context, store maptheme.Store) error,
	after func(ctx context.Context, store maptheme.Store) error,
) {
	t.Helper()
	done := make(chan error, 1)
	finished := false
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store maptheme.Store) error {
		if err := before(ctx, store); err != nil {
			return err
		}
		go func() { done <- mutationtest.Transact(t, h.Harness, write) }()
		deadline := time.Now().Add(10 * time.Second)
		for !finished && h.Waiting(t) == 0 {
			select {
			case err := <-done:
				require.NoError(t, err)
				finished = true
			default:
				if time.Now().After(deadline) {
					t.Fatal("the writer neither committed nor waited for the reader")
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
		return after(ctx, store)
	}))
	if !finished {
		require.NoError(t, <-done)
	}
}

var storeContract = map[string]func(t *testing.T, h Harness){
	"the site default exists and is the one listed theme it names": func(t *testing.T, h Harness) {
		defaultID := defaultThemeID(t, h)
		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store maptheme.Store) error {
			themes, err := store.Themes(ctx)
			require.NoError(t, err)
			require.Contains(t, ids(themes), defaultID)
			byID, err := store.ThemesByID(ctx, []string{defaultID, uuid.NewString(), defaultID})
			require.NoError(t, err)
			require.Equal(t, []string{defaultID}, ids(byID))
			return nil
		}))
	},
	"an inserted theme reads back at revision one with its stored snapshot": func(t *testing.T, h Harness) {
		snapshot := Snapshot("Contract insert")
		snapshot.Settings.CalloutScale = 1.1 // Not exactly representable in the float32 column.
		snapshot.Settings.ShowAreaLabels = false // The column defaults to true.
		snapshot.LightVariant.BuildingStrokeEnabled = false

		inserted := insert(t, h, snapshot)

		require.NoError(t, uuid.Validate(inserted.ID))
		require.EqualValues(t, 1, inserted.Revision)
		want := snapshot
		want.Settings.CalloutScale = float64(float32(1.1))
		require.Equal(t, want, inserted.Snapshot)
		require.False(t, inserted.CreatedAt.IsZero())
		require.Equal(t, inserted.CreatedAt, inserted.UpdatedAt)
		got, err := read(t, h, inserted.ID)
		require.NoError(t, err)
		require.Equal(t, inserted.Snapshot, got.Snapshot)
		require.Equal(t, inserted.Revision, got.Revision)
		require.True(t, inserted.CreatedAt.Equal(got.CreatedAt))
	},
	"themes are listed by name, then ID": func(t *testing.T, h Harness) {
		second := insert(t, h, Snapshot("Contract order B"))
		firstA := insert(t, h, Snapshot("Contract order A"))
		secondA := insert(t, h, Snapshot("Contract order A"))
		if secondA.ID < firstA.ID {
			firstA, secondA = secondA, firstA
		}
		var listed []string
		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store maptheme.Store) error {
			themes, err := store.Themes(ctx)
			for _, id := range ids(themes) {
				if id == firstA.ID || id == secondA.ID || id == second.ID {
					listed = append(listed, id)
				}
			}
			return err
		}))
		require.Equal(t, []string{firstA.ID, secondA.ID, second.ID}, listed)
	},
	"a missing theme is not found to read, lock or delete": func(t *testing.T, h Harness) {
		missing := uuid.NewString()
		_, err := read(t, h, missing)
		require.ErrorIs(t, err, maptheme.ErrThemeNotFound)
		err = mutationtest.Transact(t, h.Harness, func(ctx context.Context, store maptheme.Store) error {
			_, err := store.LockTheme(ctx, missing)
			return err
		})
		require.ErrorIs(t, err, maptheme.ErrThemeNotFound)
		err = mutationtest.Transact(t, h.Harness, func(ctx context.Context, store maptheme.Store) error {
			return store.DeleteTheme(ctx, missing)
		})
		require.ErrorIs(t, err, maptheme.ErrThemeNotFound)
	},
	"a snapshot update applies only at the expected revision": func(t *testing.T, h Harness) {
		theme := insert(t, h, Snapshot("Contract revision"))
		changed := Snapshot("Contract revision changed")
		changed.Settings.CalloutScale = 1.1
		changed.DarkVariant.BackgroundColor = "#202020"

		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store maptheme.Store) error {
			return store.UpdateSnapshot(ctx, theme.ID, 1, changed)
		}))
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store maptheme.Store) error {
			return store.UpdateSnapshot(ctx, theme.ID, 1, Snapshot("Contract stale"))
		})
		require.ErrorIs(t, err, maptheme.ErrRevisionChanged)
		err = mutationtest.Transact(t, h.Harness, func(ctx context.Context, store maptheme.Store) error {
			return store.UpdateSnapshot(ctx, uuid.NewString(), 1, Snapshot("Contract gone"))
		})
		require.ErrorIs(t, err, maptheme.ErrRevisionChanged)

		got, err := read(t, h, theme.ID)
		require.NoError(t, err)
		require.EqualValues(t, 2, got.Revision)
		want := changed
		want.Settings.CalloutScale = float64(float32(1.1))
		require.Equal(t, want, got.Snapshot)
		require.True(t, got.CreatedAt.Equal(theme.CreatedAt))
		require.False(t, got.UpdatedAt.Before(theme.UpdatedAt))
	},
	"the site default cannot be deleted until another theme is the default": func(t *testing.T, h Harness) {
		current := insert(t, h, Snapshot("Contract current default"))
		replacement := insert(t, h, Snapshot("Contract replacement default"))
		setDefault(t, h, current.ID)
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store maptheme.Store) error {
			return store.DeleteTheme(ctx, current.ID)
		})
		require.ErrorIs(t, err, maptheme.ErrDefaultTheme)

		setDefault(t, h, replacement.ID)
		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store maptheme.Store) error {
			locked, err := store.LockDefaultThemeID(ctx)
			require.Equal(t, replacement.ID, locked)
			return err
		}))
		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store maptheme.Store) error {
			return store.DeleteTheme(ctx, current.ID)
		}))
		require.Equal(t, replacement.ID, defaultThemeID(t, h))
		_, err = read(t, h, current.ID)
		require.ErrorIs(t, err, maptheme.ErrThemeNotFound)
	},
	"a missing theme cannot become the site default": func(t *testing.T, h Harness) {
		original := defaultThemeID(t, h)
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store maptheme.Store) error {
			return store.SetDefaultThemeID(ctx, uuid.NewString())
		})
		require.Error(t, err)
		require.Equal(t, original, defaultThemeID(t, h))
	},
	"a failed transaction leaves nothing behind": func(t *testing.T, h Harness) {
		original := defaultThemeID(t, h)
		replacement := insert(t, h, Snapshot("Contract rollback default"))
		failed := errors.New("later step failed")
		var inserted maptheme.Theme
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store maptheme.Store) (err error) {
			if inserted, err = store.InsertTheme(ctx, Snapshot("Contract rolled back")); err != nil {
				return err
			}
			if err := store.UpdateSnapshot(ctx, replacement.ID, 1, Snapshot("Contract rolled back update")); err != nil {
				return err
			}
			if err := store.SetDefaultThemeID(ctx, replacement.ID); err != nil {
				return err
			}
			return failed
		})
		require.ErrorIs(t, err, failed)

		_, err = read(t, h, inserted.ID)
		require.ErrorIs(t, err, maptheme.ErrThemeNotFound)
		kept, err := read(t, h, replacement.ID)
		require.NoError(t, err)
		require.EqualValues(t, 1, kept.Revision)
		require.Equal(t, "Contract rollback default", kept.Snapshot.Name)
		require.Equal(t, original, defaultThemeID(t, h))
	},
	"a locked theme makes a second writer wait for the first commit": func(t *testing.T, h Harness) {
		theme := insert(t, h, Snapshot("Contract theme lock"))
		var seen maptheme.Theme
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store maptheme.Store) error {
				_, err := store.LockTheme(ctx, theme.ID)
				return err
			},
			func(ctx context.Context, store maptheme.Store) error {
				return store.UpdateSnapshot(ctx, theme.ID, 1, Snapshot("Contract first writer"))
			},
			func(ctx context.Context, store maptheme.Store) (err error) {
				seen, err = store.LockTheme(ctx, theme.ID)
				return err
			},
		)
		require.NoError(t, err)
		require.EqualValues(t, 2, seen.Revision, "the second writer reads the first writer's commit")
		require.Equal(t, "Contract first writer", seen.Snapshot.Name)
	},
	"locked site settings make a second default switch wait for the first commit": func(t *testing.T, h Harness) {
		replacement := insert(t, h, Snapshot("Contract settings lock"))
		var seen string
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store maptheme.Store) error {
				_, err := store.LockDefaultThemeID(ctx)
				return err
			},
			func(ctx context.Context, store maptheme.Store) error {
				return store.SetDefaultThemeID(ctx, replacement.ID)
			},
			func(ctx context.Context, store maptheme.Store) (err error) {
				seen, err = store.LockDefaultThemeID(ctx)
				return err
			},
		)
		require.NoError(t, err)
		require.Equal(t, replacement.ID, seen)
	},
	"a read keeps one snapshot across a concurrent default switch and delete": func(t *testing.T, h Harness) {
		first := insert(t, h, Snapshot("Contract snapshot A"))
		second := insert(t, h, Snapshot("Contract snapshot B"))
		setDefault(t, h, first.ID)
		var defaultID string
		var resolved []maptheme.Theme
		requireReadSurvivesWriter(t, h,
			func(ctx context.Context, store maptheme.Store) (err error) {
				defaultID, err = store.DefaultThemeID(ctx)
				return err
			},
			func(ctx context.Context, store maptheme.Store) error {
				if err := store.SetDefaultThemeID(ctx, second.ID); err != nil {
					return err
				}
				return store.DeleteTheme(ctx, first.ID)
			},
			func(ctx context.Context, store maptheme.Store) (err error) {
				resolved, err = store.ThemesByID(ctx, []string{defaultID})
				return err
			},
		)
		require.Equal(t, first.ID, defaultID)
		require.Equal(t, []string{first.ID}, ids(resolved))
		require.Equal(t, second.ID, defaultThemeID(t, h))
		_, err := read(t, h, first.ID)
		require.ErrorIs(t, err, maptheme.ErrThemeNotFound)
	},
	"a contributor resolves to its Identity and an unknown Member is no failure": func(t *testing.T, h Harness) {
		memberID, identityID := h.SeedContributor(t)
		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store maptheme.Store) error {
			locked, err := store.LockContributor(ctx, memberID)
			require.Equal(t, identityID, locked)
			return err
		}))
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store maptheme.Store) error {
			_, err := store.LockContributor(ctx, uuid.NewString())
			return err
		})
		require.Error(t, err)
		require.False(t, authorizationtarget.FailedToDecide(err))
	},
}
