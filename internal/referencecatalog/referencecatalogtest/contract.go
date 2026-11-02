package referencecatalogtest

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	optional "github.com/echovisionlab/geul-api/internal/optional"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// Harness is one Reference Catalog Store a contract case runs against, with
// hooks that seed the rows of other domains the Store reads.
type Harness struct {
	mutationtest.Harness[referencecatalog.Store]
	// SeedReferenceUse adds one row of another domain that points at the
	// reference.
	SeedReferenceUse func(t *testing.T, kind referencecatalog.ReferenceKind, id string)
	// SeedClientWork adds a Work that names the Client.
	SeedClientWork func(t *testing.T, clientID string)
	// SeedMapPlaceUse adds a Work at the Map Place.
	SeedMapPlaceUse func(t *testing.T, placeID string)
	// SeedMember adds an onboarded Member and returns its ID.
	SeedMember func(t *testing.T, nickname string) string
	// SeedReadyFile adds an attachable file with a ready asset of kind and
	// returns both IDs.
	SeedReadyFile func(t *testing.T, kind string) (fileID, assetID string)
}

// FakeHarness is the Harness of a fresh in-memory Store.
func FakeHarness() Harness {
	store, _ := New()
	return Harness{
		Harness: mutationtest.FakeHarness[referencecatalog.Store](store),
		SeedReferenceUse: func(_ *testing.T, kind referencecatalog.ReferenceKind, id string) {
			store.SeedReferenceUse(kind, id, 1)
		},
		SeedClientWork:  func(_ *testing.T, clientID string) { store.SeedClientWork(clientID) },
		SeedMapPlaceUse: func(_ *testing.T, placeID string) { store.SeedMapPlaceUse(placeID) },
		SeedMember:      func(_ *testing.T, nickname string) string { return store.SeedMember(nickname) },
		SeedReadyFile: func(_ *testing.T, kind string) (string, string) {
			fileID, asset := store.SeedReadyFile(kind)
			return fileID, optional.Value(asset).AssetID
		},
	}
}

// RunStoreContract runs every Store contract case against a fresh Harness,
// in parallel. Its cases need seeders beside the unit of work, so it runs
// them the way mutationtest.RunContract does instead of through it.
func RunStoreContract(t *testing.T, open func(t *testing.T) Harness) {
	t.Helper()
	for name, run := range storeContract {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			run(t, open(t))
		})
	}
}

var errRolledBack = errors.New("roll the transaction back")

func transact(t *testing.T, h Harness, work func(ctx context.Context, store referencecatalog.Store) error) error {
	t.Helper()
	return mutationtest.Transact(t, h.Harness, work)
}

func read(t *testing.T, h Harness, read func(ctx context.Context, store referencecatalog.Store) error) {
	t.Helper()
	require.NoError(t, mutationtest.Read(t, h.Harness, read))
}

func insertReference(t *testing.T, h Harness, kind referencecatalog.ReferenceKind, name, slug string) referencecatalog.Reference {
	t.Helper()
	var stored referencecatalog.Reference
	description := "about " + name
	require.NoError(t, transact(t, h, func(ctx context.Context, store referencecatalog.Store) (err error) {
		stored, err = store.InsertReference(ctx, kind, referencecatalog.Reference{Name: name, Slug: slug, Description: &description})
		return err
	}))
	return stored
}

func insertClient(t *testing.T, h Harness, name string) referencecatalog.Client {
	t.Helper()
	var client referencecatalog.Client
	require.NoError(t, transact(t, h, func(ctx context.Context, store referencecatalog.Store) (err error) {
		client, err = store.InsertClient(ctx, name, nil)
		return err
	}))
	return client
}

func insertMapPlace(t *testing.T, h Harness, place referencecatalog.MapPlace) referencecatalog.MapPlace {
	t.Helper()
	var stored referencecatalog.MapPlace
	require.NoError(t, transact(t, h, func(ctx context.Context, store referencecatalog.Store) (err error) {
		stored, err = store.InsertMapPlace(ctx, place)
		return err
	}))
	return stored
}

func search(value string) []*commonv1.FilterSpec {
	return []*commonv1.FilterSpec{{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: value}}
}

func names[T any](values []T, name func(T) string) []string {
	result := make([]string, len(values))
	for i := range values {
		result[i] = name(values[i])
	}
	return result
}

func referenceName(value referencecatalog.Reference) string { return value.Name }
func clientName(value referencecatalog.Client) string       { return value.Name }
func placeName(value referencecatalog.MapPlace) string      { return value.Name }

func codeOf(err error) connect.Code {
	return errs.Wrap(err).Code()
}

var storeContract = map[string]func(t *testing.T, h Harness){
	"an inserted reference of each kind locks with its stored fields": func(t *testing.T, h Harness) {
		for _, kind := range referencecatalog.ReferenceKinds {
			suffix := uuid.NewString()[:8]
			inserted := insertReference(t, h, kind, "Ambient "+suffix, "ambient-"+suffix)
			require.NotEmpty(t, inserted.ID)
			require.Equal(t, "Ambient "+suffix, inserted.Name)
			require.Equal(t, "ambient-"+suffix, inserted.Slug)
			require.NoError(t, transact(t, h, func(ctx context.Context, store referencecatalog.Store) error {
				locked, err := store.LockReference(ctx, kind, inserted.ID)
				require.NoError(t, err)
				require.Equal(t, inserted, locked)
				return nil
			}), kind)
			switch kind {
			case referencecatalog.KindFormat:
				require.Zero(t, inserted.CreatedAt, "format keeps no creation time")
				require.Nil(t, inserted.Description)
			case referencecatalog.KindTag:
				require.NotZero(t, inserted.CreatedAt)
				require.Nil(t, inserted.Description, "tag keeps no description")
			default:
				require.NotZero(t, inserted.CreatedAt, kind)
				require.Equal(t, "about Ambient "+suffix, *inserted.Description, kind)
			}
		}
	},
	"a missing reference, Client or Map Place answers its not-found sentinel": func(t *testing.T, h Harness) {
		missing := uuid.NewString()
		require.NoError(t, transact(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			_, err := store.LockReference(ctx, referencecatalog.KindGenre, missing)
			require.ErrorIs(t, err, referencecatalog.ErrReferenceNotFound)
			_, err = store.LockClient(ctx, missing)
			require.ErrorIs(t, err, referencecatalog.ErrClientNotFound)
			_, err = store.LockClientLogo(ctx, missing, referencecatalog.ClientLogoLight)
			require.ErrorIs(t, err, referencecatalog.ErrClientNotFound)
			require.ErrorIs(t, store.SetClientLogo(ctx, missing, referencecatalog.ClientLogoDark, nil), referencecatalog.ErrClientNotFound)
			_, err = store.LockMapPlace(ctx, missing)
			require.ErrorIs(t, err, referencecatalog.ErrMapPlaceNotFound)
			return nil
		}))
		read(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			_, err := store.Client(ctx, missing)
			require.ErrorIs(t, err, referencecatalog.ErrClientNotFound)
			_, _, err = store.ClientLogoFiles(ctx, missing)
			require.ErrorIs(t, err, referencecatalog.ErrClientNotFound)
			_, err = store.MapPlace(ctx, missing)
			require.ErrorIs(t, err, referencecatalog.ErrMapPlaceNotFound)
			return nil
		})
	},
	"a taken name or slug of the kind is refused, and other kinds do not conflict": func(t *testing.T, h Harness) {
		for _, kind := range referencecatalog.ReferenceKinds {
			suffix := uuid.NewString()[:8]
			first := insertReference(t, h, kind, "First "+suffix, "first-"+suffix)
			second := insertReference(t, h, kind, "Second "+suffix, "second-"+suffix)
			attempts := []struct {
				want  error
				write func(ctx context.Context, store referencecatalog.Store) error
			}{
				{referencecatalog.ErrNameTaken, func(ctx context.Context, store referencecatalog.Store) error {
					_, err := store.InsertReference(ctx, kind, referencecatalog.Reference{Name: first.Name, Slug: "other-" + suffix})
					return err
				}},
				{referencecatalog.ErrSlugTaken, func(ctx context.Context, store referencecatalog.Store) error {
					_, err := store.InsertReference(ctx, kind, referencecatalog.Reference{Name: "Other " + suffix, Slug: first.Slug})
					return err
				}},
				{referencecatalog.ErrNameTaken, func(ctx context.Context, store referencecatalog.Store) error {
					_, err := store.UpdateReference(ctx, kind, second.ID, referencecatalog.ReferenceChanges{Name: &first.Name})
					return err
				}},
				{referencecatalog.ErrSlugTaken, func(ctx context.Context, store referencecatalog.Store) error {
					_, err := store.UpdateReference(ctx, kind, second.ID, referencecatalog.ReferenceChanges{Slug: &first.Slug})
					return err
				}},
			}
			for _, attempt := range attempts {
				require.ErrorIs(t, transact(t, h, attempt.write), attempt.want, kind)
			}
		}
		suffix := uuid.NewString()[:8]
		insertReference(t, h, referencecatalog.KindCategory, "Shared "+suffix, "shared-"+suffix)
		insertReference(t, h, referencecatalog.KindTag, "Shared "+suffix, "shared-"+suffix)
	},
	"an update stores the changed fields, keeps the others and stamps a category again": func(t *testing.T, h Harness) {
		suffix := uuid.NewString()[:8]
		category := insertReference(t, h, referencecatalog.KindCategory, "Old "+suffix, "old-"+suffix)
		require.NotNil(t, category.UpdatedAt, "a new category stamps its update time")
		name, description := "New "+suffix, "renamed"
		var updated referencecatalog.Reference
		require.NoError(t, transact(t, h, func(ctx context.Context, store referencecatalog.Store) (err error) {
			updated, err = store.UpdateReference(ctx, referencecatalog.KindCategory, category.ID, referencecatalog.ReferenceChanges{Name: &name, Description: &description})
			return err
		}))
		require.Equal(t, name, updated.Name)
		require.Equal(t, category.Slug, updated.Slug)
		require.Equal(t, description, *updated.Description)
		require.Equal(t, category.CreatedAt, updated.CreatedAt)
		require.False(t, updated.UpdatedAt.Before(*category.UpdatedAt))

		style := insertReference(t, h, referencecatalog.KindStyle, "Style "+suffix, "style-"+suffix)
		slug := "style-renamed-" + suffix
		require.NoError(t, transact(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			stored, err := store.UpdateReference(ctx, referencecatalog.KindStyle, style.ID, referencecatalog.ReferenceChanges{Slug: &slug})
			require.NoError(t, err)
			require.Equal(t, referencecatalog.Reference{
				ID: style.ID, Name: style.Name, Slug: slug, Description: style.Description, CreatedAt: style.CreatedAt,
			}, stored)
			return nil
		}))
	},
	"references list by name with search, sort, paging and the matching total": func(t *testing.T, h Harness) {
		suffix := uuid.NewString()[:8]
		for _, name := range []string{"Gamma", "Alpha", "Beta"} {
			insertReference(t, h, referencecatalog.KindFormat, name+" "+suffix, "format-"+name+"-"+suffix)
		}
		insertReference(t, h, referencecatalog.KindFormat, "Delta other", "format-delta-"+suffix)
		read(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			listed, total, err := store.ListReferences(ctx, referencecatalog.KindFormat, referencecatalog.ListPage{Filters: search(suffix), Limit: 10})
			require.NoError(t, err)
			require.EqualValues(t, 3, total)
			require.Equal(t, []string{"Alpha " + suffix, "Beta " + suffix, "Gamma " + suffix}, names(listed, referenceName))

			listed, total, err = store.ListReferences(ctx, referencecatalog.KindFormat, referencecatalog.ListPage{
				Filters: search(suffix), Sorts: []*commonv1.SortSpec{{Field: "name", Order: commonv1.SortOrder_SORT_ORDER_DESC}}, Limit: 1, Offset: 1,
			})
			require.NoError(t, err)
			require.EqualValues(t, 3, total)
			require.Equal(t, []string{"Beta " + suffix}, names(listed, referenceName))
			return nil
		})
	},
	"an unknown filter or sort field is invalid input": func(t *testing.T, h Harness) {
		unknownFilter := []*commonv1.FilterSpec{{Field: "id", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: uuid.NewString()}}
		unknownSort := []*commonv1.SortSpec{{Field: "unknown", Order: commonv1.SortOrder_SORT_ORDER_ASC}}
		for _, list := range []func(ctx context.Context, store referencecatalog.Store, page referencecatalog.ListPage) error{
			func(ctx context.Context, store referencecatalog.Store, page referencecatalog.ListPage) error {
				_, _, err := store.ListReferences(ctx, referencecatalog.KindCategory, page)
				return err
			},
			func(ctx context.Context, store referencecatalog.Store, page referencecatalog.ListPage) error {
				_, _, err := store.ListClients(ctx, page)
				return err
			},
			func(ctx context.Context, store referencecatalog.Store, page referencecatalog.ListPage) error {
				_, _, err := store.ListMapPlaces(ctx, page)
				return err
			},
		} {
			for _, page := range []referencecatalog.ListPage{{Filters: unknownFilter, Limit: 10}, {Sorts: unknownSort, Limit: 10}} {
				err := mutationtest.Read(t, h.Harness, func(ctx context.Context, store referencecatalog.Store) error {
					return list(ctx, store, page)
				})
				require.Equal(t, connect.CodeInvalidArgument, codeOf(err))
			}
		}
	},
	"usage and in-use follow the rows that point at a reference, until it is deleted": func(t *testing.T, h Harness) {
		for _, kind := range referencecatalog.ReferenceKinds {
			suffix := uuid.NewString()[:8]
			used := insertReference(t, h, kind, "Used "+suffix, "used-"+suffix)
			unused := insertReference(t, h, kind, "Unused "+suffix, "unused-"+suffix)
			h.SeedReferenceUse(t, kind, used.ID)
			h.SeedReferenceUse(t, kind, used.ID)
			read(t, h, func(ctx context.Context, store referencecatalog.Store) error {
				usage, err := store.ReferenceUsage(ctx, kind, []string{used.ID, unused.ID})
				require.NoError(t, err)
				require.Equal(t, map[string]int32{used.ID: 2}, usage, kind)
				usage, err = store.ReferenceUsage(ctx, kind, nil)
				require.NoError(t, err)
				require.Empty(t, usage)
				return nil
			})
			require.NoError(t, transact(t, h, func(ctx context.Context, store referencecatalog.Store) error {
				inUse, err := store.ReferenceInUse(ctx, kind, used.ID)
				require.NoError(t, err)
				require.True(t, inUse, kind)
				inUse, err = store.ReferenceInUse(ctx, kind, unused.ID)
				require.NoError(t, err)
				require.False(t, inUse, kind)
				return store.DeleteReference(ctx, kind, unused.ID)
			}))
			require.ErrorIs(t, transact(t, h, func(ctx context.Context, store referencecatalog.Store) error {
				_, err := store.LockReference(ctx, kind, unused.ID)
				return err
			}), referencecatalog.ErrReferenceNotFound)
		}
	},
	"a failed transaction stores nothing": func(t *testing.T, h Harness) {
		suffix := uuid.NewString()[:8]
		require.ErrorIs(t, transact(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			if _, err := store.InsertReference(ctx, referencecatalog.KindTag, referencecatalog.Reference{Name: "Rolled " + suffix, Slug: "rolled-" + suffix}); err != nil {
				return err
			}
			if _, err := store.InsertClient(ctx, "Rolled "+suffix, nil); err != nil {
				return err
			}
			if _, err := store.InsertMapPlace(ctx, referencecatalog.MapPlace{Name: "Rolled " + suffix, Address: "Nowhere"}); err != nil {
				return err
			}
			return errRolledBack
		}), errRolledBack)
		read(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			_, total, err := store.ListReferences(ctx, referencecatalog.KindTag, referencecatalog.ListPage{Filters: search(suffix), Limit: 10})
			require.NoError(t, err)
			require.Zero(t, total)
			_, total, err = store.ListClients(ctx, referencecatalog.ListPage{Filters: search(suffix), Limit: 10})
			require.NoError(t, err)
			require.Zero(t, total)
			_, total, err = store.ListMapPlaces(ctx, referencecatalog.ListPage{Filters: search(suffix), Limit: 10})
			require.NoError(t, err)
			require.Zero(t, total)
			return nil
		})
	},
	"a locked reference makes a second writer wait for the first commit": func(t *testing.T, h Harness) {
		suffix := uuid.NewString()[:8]
		genre := insertReference(t, h, referencecatalog.KindGenre, "Locked "+suffix, "locked-"+suffix)
		renamed := "Renamed " + suffix
		var seen referencecatalog.Reference
		require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store referencecatalog.Store) error {
				_, err := store.LockReference(ctx, referencecatalog.KindGenre, genre.ID)
				return err
			},
			func(ctx context.Context, store referencecatalog.Store) error {
				_, err := store.UpdateReference(ctx, referencecatalog.KindGenre, genre.ID, referencecatalog.ReferenceChanges{Name: &renamed})
				return err
			},
			func(ctx context.Context, store referencecatalog.Store) (err error) {
				seen, err = store.LockReference(ctx, referencecatalog.KindGenre, genre.ID)
				return err
			},
		))
		require.Equal(t, renamed, seen.Name)
	},
	"an inserted Client reads back, and a taken name is refused": func(t *testing.T, h Harness) {
		suffix := uuid.NewString()[:8]
		website := "https://client.example.test"
		var client referencecatalog.Client
		require.NoError(t, transact(t, h, func(ctx context.Context, store referencecatalog.Store) (err error) {
			client, err = store.InsertClient(ctx, "Client "+suffix, &website)
			return err
		}))
		require.NotEmpty(t, client.ID)
		require.NotZero(t, client.CreatedAt)
		read(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			stored, err := store.Client(ctx, client.ID)
			require.NoError(t, err)
			require.Equal(t, client, stored)
			require.Equal(t, website, *stored.Website)
			return nil
		})
		other := insertClient(t, h, "Other "+suffix)
		require.ErrorIs(t, transact(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			_, err := store.InsertClient(ctx, client.Name, nil)
			return err
		}), referencecatalog.ErrClientNameTaken)
		require.ErrorIs(t, transact(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			_, err := store.UpdateClient(ctx, other.ID, referencecatalog.ClientChanges{Name: &client.Name})
			return err
		}), referencecatalog.ErrClientNameTaken)

		renamed, site := "Renamed "+suffix, "https://renamed.example.test"
		require.NoError(t, transact(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			updated, err := store.UpdateClient(ctx, other.ID, referencecatalog.ClientChanges{Name: &renamed, Website: &site})
			require.NoError(t, err)
			require.Equal(t, renamed, updated.Name)
			require.Equal(t, site, *updated.Website)
			require.Equal(t, other.CreatedAt, updated.CreatedAt)
			return nil
		}))
	},
	"Clients list and search by name": func(t *testing.T, h Harness) {
		suffix := uuid.NewString()[:8]
		for _, name := range []string{"Charlie", "Alpha", "Bravo"} {
			insertClient(t, h, name+" "+suffix)
		}
		read(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			listed, total, err := store.ListClients(ctx, referencecatalog.ListPage{Filters: search(suffix), Limit: 2, Offset: 1})
			require.NoError(t, err)
			require.EqualValues(t, 3, total)
			require.Equal(t, []string{"Bravo " + suffix, "Charlie " + suffix}, names(listed, clientName))
			found, err := store.SearchClients(ctx, suffix, 2)
			require.NoError(t, err)
			require.Equal(t, []string{"Alpha " + suffix, "Bravo " + suffix}, names(found, clientName))
			found, err = store.SearchClients(ctx, "", 50)
			require.NoError(t, err)
			require.Len(t, found, 3)
			return nil
		})
	},
	"a Client search matches its query literally": func(t *testing.T, h Harness) {
		suffix := uuid.NewString()[:8]
		literal := insertClient(t, h, "100% "+suffix)
		insertClient(t, h, "1000 "+suffix)
		insertClient(t, h, "1_0 "+suffix)
		read(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			found, err := store.SearchClients(ctx, "100%", 10)
			require.NoError(t, err)
			require.Equal(t, []string{literal.Name}, names(found, clientName))
			found, err = store.SearchClients(ctx, "1_", 10)
			require.NoError(t, err)
			require.Equal(t, []string{"1_0 " + suffix}, names(found, clientName))
			return nil
		})
	},
	"a Client's logo slots hold their own files": func(t *testing.T, h Harness) {
		client := insertClient(t, h, "Logo "+uuid.NewString()[:8])
		lightFileID, _ := h.SeedReadyFile(t, "logo")
		darkFileID, _ := h.SeedReadyFile(t, "logo")
		require.NoError(t, transact(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			selected, err := store.LockClientLogo(ctx, client.ID, referencecatalog.ClientLogoLight)
			require.NoError(t, err)
			require.Nil(t, selected)
			require.NoError(t, store.SetClientLogo(ctx, client.ID, referencecatalog.ClientLogoLight, &lightFileID))
			return store.SetClientLogo(ctx, client.ID, referencecatalog.ClientLogoDark, &darkFileID)
		}))
		require.NoError(t, transact(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			selected, err := store.LockClientLogo(ctx, client.ID, referencecatalog.ClientLogoDark)
			require.NoError(t, err)
			require.Equal(t, darkFileID, *selected)
			return store.SetClientLogo(ctx, client.ID, referencecatalog.ClientLogoDark, nil)
		}))
		read(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			light, dark, err := store.ClientLogoFiles(ctx, client.ID)
			require.NoError(t, err)
			require.Equal(t, lightFileID, *light)
			require.Nil(t, dark)
			stored, err := store.Client(ctx, client.ID)
			require.NoError(t, err)
			require.Equal(t, lightFileID, *stored.LogoLightFileID)
			require.Nil(t, stored.LogoDarkFileID)
			return nil
		})
	},
	"a Client is in use while a Work names it, and Works are counted per Client": func(t *testing.T, h Harness) {
		suffix := uuid.NewString()[:8]
		used := insertClient(t, h, "Used "+suffix)
		unused := insertClient(t, h, "Unused "+suffix)
		h.SeedClientWork(t, used.ID)
		h.SeedClientWork(t, used.ID)
		require.NoError(t, transact(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			counts, err := store.CountClientWorks(ctx, []string{used.ID, unused.ID})
			require.NoError(t, err)
			require.Equal(t, map[string]int32{used.ID: 2}, counts)
			inUse, err := store.ClientInUse(ctx, used.ID)
			require.NoError(t, err)
			require.True(t, inUse)
			inUse, err = store.ClientInUse(ctx, unused.ID)
			require.NoError(t, err)
			require.False(t, inUse)
			return store.DeleteClient(ctx, unused.ID)
		}))
		read(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			_, err := store.Client(ctx, unused.ID)
			require.ErrorIs(t, err, referencecatalog.ErrClientNotFound)
			return nil
		})
	},
	"a locked Client or logo slot makes a second writer wait for the first commit": func(t *testing.T, h Harness) {
		client := insertClient(t, h, "Locked "+uuid.NewString()[:8])
		fileID, _ := h.SeedReadyFile(t, "logo")
		var seen *string
		require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store referencecatalog.Store) error {
				_, err := store.LockClient(ctx, client.ID)
				return err
			},
			func(ctx context.Context, store referencecatalog.Store) error {
				return store.SetClientLogo(ctx, client.ID, referencecatalog.ClientLogoLight, &fileID)
			},
			func(ctx context.Context, store referencecatalog.Store) (err error) {
				seen, err = store.LockClientLogo(ctx, client.ID, referencecatalog.ClientLogoLight)
				return err
			},
		))
		require.Equal(t, fileID, *seen)
	},
	"an inserted Map Place reads back and saves every field": func(t *testing.T, h Harness) {
		suffix := uuid.NewString()[:8]
		creator := h.SeedMember(t, "Creator "+suffix)
		city, googleID := "Seoul", "google-"+suffix
		inserted := insertMapPlace(t, h, referencecatalog.MapPlace{
			Name: "Hall " + suffix, Address: "1 Hall Road", Lat: 37.5, Lng: 127.25,
			AddressComponents: &referencecatalog.AddressComponents{City: &city}, GooglePlaceID: &googleID,
			CreatedByMemberID: &creator, UpdatedByMemberID: &creator,
		})
		require.NotEmpty(t, inserted.ID)
		require.NotZero(t, inserted.CreatedAt)
		require.NotZero(t, inserted.UpdatedAt)
		read(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			stored, err := store.MapPlace(ctx, inserted.ID)
			require.NoError(t, err)
			require.Equal(t, inserted, stored)
			byID, err := store.MapPlacesByID(ctx, []string{inserted.ID, uuid.NewString()})
			require.NoError(t, err)
			require.Equal(t, []referencecatalog.MapPlace{inserted}, byID)
			return nil
		})

		changed := inserted
		changed.Name, changed.Lat, changed.AddressComponents, changed.GooglePlaceID = "Moved "+suffix, -12.5, nil, nil
		require.NoError(t, transact(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			_, err := store.SaveMapPlace(ctx, changed)
			return err
		}))
		read(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			stored, err := store.MapPlace(ctx, inserted.ID)
			require.NoError(t, err)
			require.False(t, stored.UpdatedAt.Before(inserted.UpdatedAt))
			changed.UpdatedAt = stored.UpdatedAt
			require.Equal(t, changed, stored)
			return nil
		})
	},
	"Map Places search by name and list by search, newest update first": func(t *testing.T, h Harness) {
		suffix := uuid.NewString()[:8]
		first := insertMapPlace(t, h, referencecatalog.MapPlace{Name: "Zulu" + suffix, Address: "Main Street"})
		second := insertMapPlace(t, h, referencecatalog.MapPlace{Name: "Yankee", Address: "Road " + suffix})
		read(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			found, err := store.SearchMapPlaces(ctx, "Zulu"+suffix, 10)
			require.NoError(t, err)
			require.Equal(t, []string{first.Name}, names(found, placeName))
			listed, total, err := store.ListMapPlaces(ctx, referencecatalog.ListPage{Filters: search(suffix), Limit: 10})
			require.NoError(t, err)
			require.EqualValues(t, 2, total)
			require.Equal(t, []string{second.Name, first.Name}, names(listed, placeName))
			return nil
		})
	},
	"a Map Place is in use while a Work is at it": func(t *testing.T, h Harness) {
		used := insertMapPlace(t, h, referencecatalog.MapPlace{Name: "Used " + uuid.NewString()[:8], Address: "Used Road"})
		unused := insertMapPlace(t, h, referencecatalog.MapPlace{Name: "Unused " + uuid.NewString()[:8], Address: "Unused Road"})
		h.SeedMapPlaceUse(t, used.ID)
		require.NoError(t, transact(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			inUse, err := store.MapPlaceInUse(ctx, used.ID)
			require.NoError(t, err)
			require.True(t, inUse)
			inUse, err = store.MapPlaceInUse(ctx, unused.ID)
			require.NoError(t, err)
			require.False(t, inUse)
			return store.DeleteMapPlace(ctx, unused.ID)
		}))
		read(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			_, err := store.MapPlace(ctx, unused.ID)
			require.ErrorIs(t, err, referencecatalog.ErrMapPlaceNotFound)
			return nil
		})
	},
	"a locked Map Place makes a second writer wait for the first commit": func(t *testing.T, h Harness) {
		place := insertMapPlace(t, h, referencecatalog.MapPlace{Name: "Locked " + uuid.NewString()[:8], Address: "Lock Road"})
		var seen referencecatalog.MapPlace
		require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store referencecatalog.Store) error {
				_, err := store.LockMapPlace(ctx, place.ID)
				return err
			},
			func(ctx context.Context, store referencecatalog.Store) error {
				moved := place
				moved.Address = "Moved Road"
				_, err := store.SaveMapPlace(ctx, moved)
				return err
			},
			func(ctx context.Context, store referencecatalog.Store) (err error) {
				seen, err = store.LockMapPlace(ctx, place.ID)
				return err
			},
		))
		require.Equal(t, "Moved Road", seen.Address)
	},
	"member summaries resolve onboarded Members only": func(t *testing.T, h Harness) {
		memberID := h.SeedMember(t, "Summarized")
		unknown := uuid.NewString()
		read(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			members, err := store.MemberSummaries(ctx, []string{memberID, unknown})
			require.NoError(t, err)
			require.Len(t, members, 1)
			require.Equal(t, memberID, members[memberID].ID)
			require.Equal(t, "Summarized", members[memberID].Nickname)
			require.False(t, members[memberID].Deleted)
			return nil
		})
	},
	"a ready file binds and resolves to its asset; a missing one does not": func(t *testing.T, h Harness) {
		fileID, assetID := h.SeedReadyFile(t, "map_image")
		place := insertMapPlace(t, h, referencecatalog.MapPlace{Name: "Imaged " + uuid.NewString()[:8], Address: "Image Road"})
		require.NoError(t, transact(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			require.NoError(t, store.LockFilesForAttachment(ctx, []string{fileID, ""}))
			bound, err := store.BindReadyAsset(ctx, referencecatalog.AssetBinding{
				SourceFileID: fileID, Owner: referencecatalog.AssetOwner{Type: "map_place", ID: place.ID}, Key: "image", Kind: "map_image",
			})
			require.NoError(t, err)
			require.Equal(t, assetID, optional.Value(bound).AssetID)
			return store.ReleaseAssets(ctx, referencecatalog.AssetRelease{Owner: referencecatalog.AssetOwner{Type: "map_place", ID: place.ID}, BindingPrefix: "image"})
		}))
		read(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			ready, err := store.ReadyAsset(ctx, referencecatalog.AssetSource{FileID: fileID, Kind: "map_image"})
			require.NoError(t, err)
			require.Equal(t, assetID, optional.Value(ready).AssetID)
			_, err = store.ReadyAsset(ctx, referencecatalog.AssetSource{FileID: uuid.NewString(), Kind: "map_image"})
			require.Equal(t, connect.CodeNotFound, codeOf(err))
			return nil
		})
		err := transact(t, h, func(ctx context.Context, store referencecatalog.Store) error {
			return store.LockFilesForAttachment(ctx, []string{uuid.NewString()})
		})
		require.Equal(t, connect.CodeFailedPrecondition, codeOf(err))
	},
}
