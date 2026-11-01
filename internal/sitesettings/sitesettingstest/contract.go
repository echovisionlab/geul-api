package sitesettingstest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
)

// Harness is one Site Settings Store a contract case runs against.
type Harness struct {
	mutationtest.Harness[sitesettings.Store]
	// SeedFile stores a File the loader rows may reference and returns its
	// ID.
	SeedFile func(t *testing.T) string
}

// FakeHarness is the Harness of a fresh in-memory Store.
func FakeHarness() Harness {
	store, _ := New()
	return Harness{
		Harness:  mutationtest.FakeHarness[sitesettings.Store](store),
		SeedFile: func(*testing.T) string { return store.SeedFile() },
	}
}

// RunStoreContract runs every Store contract case against a fresh Harness,
// in parallel. Its cases need a File seeder beside the unit of work, so it
// runs them the way mutationtest.RunContract does instead of through it.
func RunStoreContract(t *testing.T, open func(t *testing.T) Harness) {
	t.Helper()
	for name, run := range storeContract {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			run(t, open(t))
		})
	}
}

func read(t *testing.T, h Harness) sitesettings.Settings {
	t.Helper()
	var settings sitesettings.Settings
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store sitesettings.Store) (err error) {
		settings, err = store.Settings(ctx)
		return err
	}))
	return settings
}

func loaders(t *testing.T, h Harness) []string {
	t.Helper()
	var fileIDs []string
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store sitesettings.Store) (err error) {
		fileIDs, err = store.LoaderFileIDs(ctx)
		return err
	}))
	return fileIDs
}

func save(t *testing.T, h Harness, change func(*sitesettings.Settings)) {
	t.Helper()
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store sitesettings.Store) error {
		settings, err := store.LockSettings(ctx)
		if err != nil {
			return err
		}
		change(&settings)
		return store.SaveSettings(ctx, settings)
	}))
}

var storeContract = map[string]func(t *testing.T, h Harness){
	"saved settings read back as stored": func(t *testing.T, h Harness) {
		before := read(t, h)
		analytics := "G-ABC123"
		updatedAt := time.Now().UTC().Truncate(time.Microsecond)
		save(t, h, func(settings *sitesettings.Settings) {
			settings.SiteTitle = "Stored title"
			settings.CompanyAddress = "Line one\nLine two"
			settings.PrimaryColor = "#123456"
			settings.DefaultCommentsEnabled = false
			settings.GoogleAnalyticsID = &analytics
			settings.SocialLinks = []byte(`{"website":"https://example.test"}`)
			settings.OGImageConfig = []byte(`{"home":{"title":"Home"},"content":{}}`)
			settings.UpdatedAt = updatedAt
		})

		stored := read(t, h)
		require.Equal(t, "Stored title", stored.SiteTitle)
		require.Equal(t, "Line one\nLine two", stored.CompanyAddress)
		require.Equal(t, "#123456", stored.PrimaryColor)
		require.False(t, stored.DefaultCommentsEnabled)
		require.Equal(t, &analytics, stored.GoogleAnalyticsID)
		require.JSONEq(t, `{"website":"https://example.test"}`, string(stored.SocialLinks))
		require.JSONEq(t, `{"home":{"title":"Home"},"content":{}}`, string(stored.OGImageConfig))
		require.Equal(t, before.DefaultMapThemeID, stored.DefaultMapThemeID, "every value is saved, the unchanged ones too")
		require.True(t, before.CreatedAt.Equal(stored.CreatedAt))
		require.False(t, stored.UpdatedAt.Before(updatedAt), "saving stamps the update time no earlier than the saved one")

		save(t, h, func(settings *sitesettings.Settings) {
			settings.GoogleAnalyticsID = nil
			settings.OGImageConfig = nil
		})
		cleared := read(t, h)
		require.Nil(t, cleared.GoogleAnalyticsID)
		require.Empty(t, cleared.OGImageConfig)
		require.Equal(t, "Stored title", cleared.SiteTitle)
	},
	"touching the settings moves only their update time": func(t *testing.T, h Harness) {
		before := read(t, h)

		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store sitesettings.Store) error {
			return store.TouchSettings(ctx)
		}))

		after := read(t, h)
		require.True(t, after.UpdatedAt.After(before.UpdatedAt))
		after.UpdatedAt = before.UpdatedAt
		require.Equal(t, before, after)
	},
	"loader files keep the order they were added in and answer whether they changed": func(t *testing.T, h Harness) {
		first, second, third := h.SeedFile(t), h.SeedFile(t), h.SeedFile(t)
		add := func(fileID string) bool {
			var added bool
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store sitesettings.Store) (err error) {
				added, err = store.AddLoaderFile(ctx, fileID)
				return err
			}))
			return added
		}
		remove := func(fileID string) bool {
			var removed bool
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store sitesettings.Store) (err error) {
				removed, err = store.RemoveLoaderFile(ctx, fileID)
				return err
			}))
			return removed
		}
		require.Empty(t, loaders(t, h))

		require.True(t, add(first))
		require.True(t, add(second))
		require.True(t, add(third))
		require.False(t, add(second), "a File is a loader once")
		require.Equal(t, []string{first, second, third}, loaders(t, h))

		require.True(t, remove(second))
		require.False(t, remove(second))
		require.True(t, add(second))
		require.Equal(t, []string{first, third, second}, loaders(t, h), "a File added again goes last")
	},
	"a failed transaction leaves the settings and loader files as they were": func(t *testing.T, h Harness) {
		fileID := h.SeedFile(t)
		before := read(t, h)
		failure := errors.New("later step failed")

		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store sitesettings.Store) error {
			settings, err := store.LockSettings(ctx)
			if err != nil {
				return err
			}
			settings.SiteTitle = "Rolled back"
			if err := store.SaveSettings(ctx, settings); err != nil {
				return err
			}
			if _, err := store.AddLoaderFile(ctx, fileID); err != nil {
				return err
			}
			if err := store.TouchSettings(ctx); err != nil {
				return err
			}
			return failure
		})

		require.ErrorIs(t, err, failure)
		require.Equal(t, before, read(t, h))
		require.Empty(t, loaders(t, h))
	},
	"locked settings make a second writer wait and read the first commit": func(t *testing.T, h Harness) {
		var seen string
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store sitesettings.Store) error {
				_, err := store.LockSettings(ctx)
				return err
			},
			func(ctx context.Context, store sitesettings.Store) error {
				settings, err := store.Settings(ctx)
				if err != nil {
					return err
				}
				settings.OGImageConfig = []byte(`{"home":{"revision":"first"},"content":{}}`)
				return store.SaveSettings(ctx, settings)
			},
			func(ctx context.Context, store sitesettings.Store) error {
				settings, err := store.LockSettings(ctx)
				if err != nil {
					return err
				}
				seen = string(settings.OGImageConfig)
				settings.SiteTitle = "Second writer"
				return store.SaveSettings(ctx, settings)
			},
		)

		require.NoError(t, err)
		require.JSONEq(t, `{"home":{"revision":"first"},"content":{}}`, seen, "the second writer locks after the first commit")
		stored := read(t, h)
		require.Equal(t, "Second writer", stored.SiteTitle)
		require.JSONEq(t, `{"home":{"revision":"first"},"content":{}}`, string(stored.OGImageConfig), "nothing the first wrote is lost")
	},
}
