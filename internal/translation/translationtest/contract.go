package translationtest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
)

// Harness is one Translation Store a contract case runs against.
type Harness struct {
	mutationtest.Harness[application.Store]
}

// FakeHarness is the Harness of a fresh in-memory Store.
func FakeHarness() Harness {
	return Harness{
		Harness: mutationtest.FakeHarness[application.Store](NewStore()),
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

func contractNow() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

var storeContract = map[string]func(t *testing.T, h Harness){

	"settings read the singleton and store the default locale": func(t *testing.T, h Harness) {
		var settings translation.RuntimeSettings
		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store application.Store) (err error) {
			settings, err = store.RuntimeSettings(ctx)
			return err
		}))
		require.NotEmpty(t, settings.DefaultLocale)

		locale := "ko"
		var updatedAt time.Time
		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store application.Store) (err error) {
			if _, err = store.LockRuntimeSettings(ctx); err != nil {
				return err
			}
			updatedAt, err = store.UpdateRuntimeSettings(ctx, application.SettingsChange{DefaultLocale: &locale})
			return err
		}))
		require.False(t, updatedAt.IsZero())
		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store application.Store) (err error) {
			settings, err = store.LockRuntimeSettings(ctx)
			return err
		}))
		require.Equal(t, "ko", settings.DefaultLocale)
		require.True(t, updatedAt.Equal(*settings.UpdatedAt))
	},

	"an unknown locale answers not found": func(t *testing.T, h Harness) {
		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store application.Store) error {
			if _, err := store.Locale(ctx, "en"); err != nil {
				return err
			}
			_, err := store.Locale(ctx, "xx-unknown")
			require.ErrorIs(t, err, application.ErrLocaleNotFound)
			return nil
		}))
	},
}
