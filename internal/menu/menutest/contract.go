package menutest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// RunStoreContract runs the Menu Store contract against the Store open
// returns, a fresh one per case.
func RunStoreContract(t *testing.T, open func(t *testing.T) mutationtest.Harness[menu.Store]) {
	t.Helper()
	mutationtest.RunContract(t, open, storeContract)
}

type contractMenu struct {
	root     menu.Root
	document uuid.UUID
	revision uuid.UUID
}

// insertContractMenu stores a Menu as CreateMenu does: its compact content
// document, then the root.
func insertContractMenu(ctx context.Context, store menu.Store, name string) (contractMenu, error) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	created := contractMenu{document: uuid.New(), revision: uuid.New()}
	if err := store.InsertContentDocument(ctx, created.document, "compact", created.revision, now); err != nil {
		return contractMenu{}, err
	}
	root, err := store.InsertMenu(ctx, menu.Root{
		Name: name, ItemsJSON: []byte(`[{"id":"home","label":"Home","linkType":"custom","url":"/"}]`),
		SourceLocale: "en", ContentDocumentID: created.document.String(), CreatedAt: now, UpdatedAt: now,
	})
	created.root = root
	return created, err
}

func seedContractMenu(t *testing.T, h mutationtest.Harness[menu.Store], name string) contractMenu {
	t.Helper()
	var created contractMenu
	require.NoError(t, mutationtest.Transact(t, h, func(ctx context.Context, store menu.Store) (err error) {
		created, err = insertContractMenu(ctx, store, name)
		return err
	}))
	return created
}

func contractLocale(menuID, locale string, labels map[string]string) menu.LocaleValues {
	return menu.LocaleValues{MenuID: menuID, Locale: locale, Labels: labels, UpdatedAt: time.Now().UTC().Truncate(time.Microsecond)}
}

func seedContractLocale(t *testing.T, h mutationtest.Harness[menu.Store], values menu.LocaleValues) {
	t.Helper()
	require.NoError(t, mutationtest.Transact(t, h, func(ctx context.Context, store menu.Store) error {
		return store.InsertLocale(ctx, values)
	}))
}

func readRoot(t *testing.T, h mutationtest.Harness[menu.Store], id string) (root menu.Root, err error) {
	t.Helper()
	readErr := mutationtest.Read(t, h, func(ctx context.Context, store menu.Store) error {
		root, err = store.Menu(ctx, id)
		return nil
	})
	require.NoError(t, readErr)
	return root, err
}

func readLocale(t *testing.T, h mutationtest.Harness[menu.Store], menuID, locale string) (values menu.LocaleValues, err error) {
	t.Helper()
	readErr := mutationtest.Read(t, h, func(ctx context.Context, store menu.Store) error {
		values, err = store.Locale(ctx, menuID, locale)
		return nil
	})
	require.NoError(t, readErr)
	return values, err
}

var storeContract = map[string]func(t *testing.T, h mutationtest.Harness[menu.Store]){
	"an inserted root reads back under a new ID and a missing one is not found": func(t *testing.T, h mutationtest.Harness[menu.Store]) {
		created := seedContractMenu(t, h, "Main "+uuid.NewString()[:8])
		require.NotEmpty(t, created.root.ID)
		stored, err := readRoot(t, h, created.root.ID)
		require.NoError(t, err)
		require.Equal(t, created.root.Name, stored.Name)
		require.Equal(t, "en", stored.SourceLocale)
		require.Equal(t, created.document.String(), stored.ContentDocumentID)
		require.JSONEq(t, `[{"id":"home","label":"Home","linkType":"custom","url":"/"}]`, string(stored.ItemsJSON))
		require.True(t, created.root.UpdatedAt.Equal(stored.UpdatedAt))

		missing := uuid.NewString()
		_, err = readRoot(t, h, missing)
		require.ErrorIs(t, err, menu.ErrMenuNotFound)
		require.NoError(t, mutationtest.Read(t, h, func(ctx context.Context, store menu.Store) error {
			exists, err := store.MenuExists(ctx, created.root.ID)
			require.NoError(t, err)
			require.True(t, exists)
			exists, err = store.MenuExists(ctx, missing)
			require.NoError(t, err)
			require.False(t, exists)
			return nil
		}))
		err = mutationtest.Transact(t, h, func(ctx context.Context, store menu.Store) error {
			_, err := store.LockMenu(ctx, missing)
			return err
		})
		require.ErrorIs(t, err, menu.ErrMenuNotFound)
		err = mutationtest.Transact(t, h, func(ctx context.Context, store menu.Store) error {
			return store.UpdateMenu(ctx, missing, menu.RootChange{UpdatedAt: time.Now().UTC()})
		})
		require.ErrorIs(t, err, menu.ErrMenuNotFound)
	},
	"a Menu name is unique": func(t *testing.T, h mutationtest.Harness[menu.Store]) {
		first := seedContractMenu(t, h, "Taken "+uuid.NewString()[:8])
		second := seedContractMenu(t, h, "Free "+uuid.NewString()[:8])
		err := mutationtest.Transact(t, h, func(ctx context.Context, store menu.Store) error {
			_, err := insertContractMenu(ctx, store, first.root.Name)
			return err
		})
		require.ErrorIs(t, err, menu.ErrMenuNameTaken)
		err = mutationtest.Transact(t, h, func(ctx context.Context, store menu.Store) error {
			return store.UpdateMenu(ctx, second.root.ID, menu.RootChange{Name: &first.root.Name, UpdatedAt: time.Now().UTC()})
		})
		require.ErrorIs(t, err, menu.ErrMenuNameTaken)
		require.NoError(t, mutationtest.Read(t, h, func(ctx context.Context, store menu.Store) error {
			taken, err := store.MenuNameTaken(ctx, first.root.Name, "")
			require.NoError(t, err)
			require.True(t, taken)
			taken, err = store.MenuNameTaken(ctx, first.root.Name, first.root.ID)
			require.NoError(t, err)
			require.False(t, taken, "a Menu does not take its own name")
			return nil
		}))
	},
	"an update writes only the columns it names": func(t *testing.T, h mutationtest.Harness[menu.Store]) {
		created := seedContractMenu(t, h, "Before "+uuid.NewString()[:8])
		at := time.Now().UTC().Truncate(time.Microsecond).Add(time.Second)
		require.NoError(t, mutationtest.Transact(t, h, func(ctx context.Context, store menu.Store) error {
			return store.UpdateMenu(ctx, created.root.ID, menu.RootChange{ItemsJSON: []byte(`[]`), UpdatedAt: at})
		}))
		stored, err := readRoot(t, h, created.root.ID)
		require.NoError(t, err)
		require.Equal(t, created.root.Name, stored.Name)
		require.JSONEq(t, `[]`, string(stored.ItemsJSON))
		require.True(t, at.Equal(stored.UpdatedAt))
	},
	"Menus list by name, one page at a time, with the total": func(t *testing.T, h mutationtest.Harness[menu.Store]) {
		for _, name := range []string{"c-menu", "a-menu", "b-menu"} {
			seedContractMenu(t, h, name)
		}
		require.NoError(t, mutationtest.Read(t, h, func(ctx context.Context, store menu.Store) error {
			roots, total, err := store.ListMenus(ctx, 2, 1)
			require.NoError(t, err)
			require.EqualValues(t, 3, total)
			require.Len(t, roots, 2)
			require.Equal(t, "b-menu", roots[0].Name)
			require.Equal(t, "c-menu", roots[1].Name)
			locked, err := store.Menu(ctx, roots[0].ID)
			require.NoError(t, err)
			require.Equal(t, roots[0].ID, locked.ID)
			return nil
		}))
		require.NoError(t, mutationtest.Transact(t, h, func(ctx context.Context, store menu.Store) error {
			roots, err := store.LockMenus(ctx)
			require.NoError(t, err)
			require.Len(t, roots, 3)
			require.Less(t, roots[0].ID, roots[1].ID)
			require.Less(t, roots[1].ID, roots[2].ID)
			return nil
		}))
	},
	"the content document advances only from the revision its Menu holds": func(t *testing.T, h mutationtest.Harness[menu.Store]) {
		created := seedContractMenu(t, h, "Document "+uuid.NewString()[:8])
		other := seedContractMenu(t, h, "Other "+uuid.NewString()[:8])
		next := uuid.New()
		advance := func(menuID string, expected uuid.UUID) error {
			return mutationtest.Transact(t, h, func(ctx context.Context, store menu.Store) error {
				return store.AdvanceContentDocument(ctx, menuID, created.document, expected, next, time.Now().UTC())
			})
		}
		require.ErrorIs(t, advance(created.root.ID, uuid.New()), menu.ErrRevisionChanged)
		require.ErrorIs(t, advance(other.root.ID, created.revision), menu.ErrRevisionChanged, "another Menu's document never advances")
		require.NoError(t, advance(created.root.ID, created.revision))
		require.ErrorIs(t, advance(created.root.ID, created.revision), menu.ErrRevisionChanged)
		require.NoError(t, mutationtest.Read(t, h, func(ctx context.Context, store menu.Store) error {
			document, err := store.ContentDocument(ctx, created.document)
			require.NoError(t, err)
			require.Equal(t, "compact", document.Profile)
			require.Equal(t, next.String(), document.Revision)
			_, err = store.ContentDocument(ctx, uuid.New())
			require.ErrorIs(t, err, menu.ErrContentDocumentNotFound)
			return nil
		}))
	},
	"deleting a Menu deletes its locales, and then its document": func(t *testing.T, h mutationtest.Harness[menu.Store]) {
		created := seedContractMenu(t, h, "Deleted "+uuid.NewString()[:8])
		seedContractLocale(t, h, contractLocale(created.root.ID, "en", map[string]string{"home": "Home"}))
		require.NoError(t, mutationtest.Transact(t, h, func(ctx context.Context, store menu.Store) error {
			if err := store.DeleteMenu(ctx, created.root.ID); err != nil {
				return err
			}
			return store.DeleteContentDocument(ctx, created.document)
		}))
		_, err := readRoot(t, h, created.root.ID)
		require.ErrorIs(t, err, menu.ErrMenuNotFound)
		_, err = readLocale(t, h, created.root.ID, "en")
		require.ErrorIs(t, err, menu.ErrLocaleNotFound)
		err = mutationtest.Transact(t, h, func(ctx context.Context, store menu.Store) error {
			return store.DeleteContentDocument(ctx, created.document)
		})
		require.ErrorIs(t, err, menu.ErrContentDocumentNotFound)
	},
	"locale values keep explicit empty labels and answer a missing row": func(t *testing.T, h mutationtest.Harness[menu.Store]) {
		created := seedContractMenu(t, h, "Locales "+uuid.NewString()[:8])
		seedContractLocale(t, h, contractLocale(created.root.ID, "en", map[string]string{"home": "Home", "about": ""}))
		values, err := readLocale(t, h, created.root.ID, "en")
		require.NoError(t, err)
		require.Equal(t, map[string]string{"home": "Home", "about": ""}, values.Labels)
		_, err = readLocale(t, h, created.root.ID, "ko")
		require.ErrorIs(t, err, menu.ErrLocaleNotFound)

		err = mutationtest.Transact(t, h, func(ctx context.Context, store menu.Store) error {
			return store.UpdateLocale(ctx, contractLocale(created.root.ID, "ko", map[string]string{}))
		})
		require.ErrorIs(t, err, menu.ErrLocaleNotFound)
		err = mutationtest.Transact(t, h, func(ctx context.Context, store menu.Store) error {
			return store.DeleteLocale(ctx, created.root.ID, "ko")
		})
		require.ErrorIs(t, err, menu.ErrLocaleNotFound)
		err = mutationtest.Transact(t, h, func(ctx context.Context, store menu.Store) error {
			return store.InsertLocale(ctx, contractLocale(created.root.ID, "en", map[string]string{}))
		})
		require.Error(t, err, "a locale row is created once")

		at := time.Now().UTC().Truncate(time.Microsecond).Add(time.Second)
		require.NoError(t, mutationtest.Transact(t, h, func(ctx context.Context, store menu.Store) error {
			if err := store.UpsertLocale(ctx, menu.LocaleValues{MenuID: created.root.ID, Locale: "ko", Labels: map[string]string{"home": "홈"}, UpdatedAt: at}); err != nil {
				return err
			}
			if err := store.UpsertLocale(ctx, menu.LocaleValues{MenuID: created.root.ID, Locale: "en", Labels: map[string]string{"home": "Start"}, UpdatedAt: at}); err != nil {
				return err
			}
			return store.UpdateLocale(ctx, menu.LocaleValues{MenuID: created.root.ID, Locale: "ko", Labels: map[string]string{}, UpdatedAt: at})
		}))
		require.NoError(t, mutationtest.Read(t, h, func(ctx context.Context, store menu.Store) error {
			locales, err := store.Locales(ctx, created.root.ID)
			require.NoError(t, err)
			labels := map[string]map[string]string{}
			for _, values := range locales {
				labels[values.Locale] = values.Labels
				require.True(t, at.Equal(values.UpdatedAt))
			}
			require.Equal(t, map[string]map[string]string{"en": {"home": "Start"}, "ko": {}}, labels)
			return nil
		}))
		require.NoError(t, mutationtest.Transact(t, h, func(ctx context.Context, store menu.Store) error {
			return store.DeleteLocale(ctx, created.root.ID, "ko")
		}))
		_, err = readLocale(t, h, created.root.ID, "ko")
		require.ErrorIs(t, err, menu.ErrLocaleNotFound)
	},
	"a failed transaction leaves nothing behind": func(t *testing.T, h mutationtest.Harness[menu.Store]) {
		failed := errors.New("a later step failed")
		var created contractMenu
		err := mutationtest.Transact(t, h, func(ctx context.Context, store menu.Store) (err error) {
			if created, err = insertContractMenu(ctx, store, "Rolled back "+uuid.NewString()[:8]); err != nil {
				return err
			}
			if err := store.InsertLocale(ctx, contractLocale(created.root.ID, "en", map[string]string{})); err != nil {
				return err
			}
			return failed
		})
		require.ErrorIs(t, err, failed)
		_, err = readRoot(t, h, created.root.ID)
		require.ErrorIs(t, err, menu.ErrMenuNotFound)
		require.NoError(t, mutationtest.Read(t, h, func(ctx context.Context, store menu.Store) error {
			_, err := store.ContentDocument(ctx, created.document)
			require.ErrorIs(t, err, menu.ErrContentDocumentNotFound)
			return nil
		}))
	},
	"a locked Menu makes a second writer wait for the first commit": func(t *testing.T, h mutationtest.Harness[menu.Store]) {
		created := seedContractMenu(t, h, "Locked "+uuid.NewString()[:8])
		renamed := created.root.Name + " renamed"
		var seen menu.Root
		err := mutationtest.RequireSecondWaits(t, h,
			func(ctx context.Context, store menu.Store) error {
				_, err := store.LockMenu(ctx, created.root.ID)
				return err
			},
			func(ctx context.Context, store menu.Store) error {
				return store.UpdateMenu(ctx, created.root.ID, menu.RootChange{Name: &renamed, UpdatedAt: time.Now().UTC()})
			},
			func(ctx context.Context, store menu.Store) (err error) {
				seen, err = store.LockMenu(ctx, created.root.ID)
				return err
			},
		)
		require.NoError(t, err)
		require.Equal(t, renamed, seen.Name, "the second writer reads the first writer's commit")
	},
	"a target rewrite waits for every locked Menu": func(t *testing.T, h mutationtest.Harness[menu.Store]) {
		created := seedContractMenu(t, h, "Rewrite "+uuid.NewString()[:8])
		var seen []menu.Root
		err := mutationtest.RequireSecondWaits(t, h,
			func(ctx context.Context, store menu.Store) error {
				_, err := store.LockMenu(ctx, created.root.ID)
				return err
			},
			func(ctx context.Context, store menu.Store) error {
				return store.UpdateMenu(ctx, created.root.ID, menu.RootChange{ItemsJSON: []byte(`[]`), UpdatedAt: time.Now().UTC()})
			},
			func(ctx context.Context, store menu.Store) (err error) { seen, err = store.LockMenus(ctx); return err },
		)
		require.NoError(t, err)
		require.Len(t, seen, 1)
		require.JSONEq(t, `[]`, string(seen[0].ItemsJSON))
	},
	"a locked content document makes a second writer wait": func(t *testing.T, h mutationtest.Harness[menu.Store]) {
		created := seedContractMenu(t, h, "Locked document "+uuid.NewString()[:8])
		next := uuid.New()
		var seen menu.ContentDocument
		err := mutationtest.RequireSecondWaits(t, h,
			func(ctx context.Context, store menu.Store) error {
				_, err := store.LockContentDocument(ctx, created.document)
				return err
			},
			func(ctx context.Context, store menu.Store) error {
				return store.AdvanceContentDocument(ctx, created.root.ID, created.document, created.revision, next, time.Now().UTC())
			},
			func(ctx context.Context, store menu.Store) (err error) {
				seen, err = store.LockContentDocument(ctx, created.document)
				return err
			},
		)
		require.NoError(t, err)
		require.Equal(t, next.String(), seen.Revision)
	},
	"a locked locale makes a second writer wait": func(t *testing.T, h mutationtest.Harness[menu.Store]) {
		created := seedContractMenu(t, h, "Locked locale "+uuid.NewString()[:8])
		seedContractLocale(t, h, contractLocale(created.root.ID, "en", map[string]string{"home": "Home"}))
		var seen menu.LocaleValues
		err := mutationtest.RequireSecondWaits(t, h,
			func(ctx context.Context, store menu.Store) error {
				_, err := store.LockLocale(ctx, created.root.ID, "en")
				return err
			},
			func(ctx context.Context, store menu.Store) error {
				return store.UpdateLocale(ctx, contractLocale(created.root.ID, "en", map[string]string{"home": "Start"}))
			},
			func(ctx context.Context, store menu.Store) (err error) {
				seen, err = store.LockLocale(ctx, created.root.ID, "en")
				return err
			},
		)
		require.NoError(t, err)
		require.Equal(t, map[string]string{"home": "Start"}, seen.Labels)
	},
}
