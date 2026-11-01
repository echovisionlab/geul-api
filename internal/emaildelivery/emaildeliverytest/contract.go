package emaildeliverytest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

// Harness is one Store implementation the contract runs against, with the
// site settings row it reads but does not own.
type Harness struct {
	mutationtest.Harness[emaildelivery.Store]
	// SeedSiteSettings stores the site settings defaults names, without
	// email logos.
	SeedSiteSettings func(t *testing.T, defaults emaildelivery.RenderDefaults)
}

// FakeHarness is the Harness of a fresh in-memory Store.
func FakeHarness() Harness {
	store := NewStore()
	return Harness{
		Harness: mutationtest.FakeHarness[emaildelivery.Store](store),
		SeedSiteSettings: func(_ *testing.T, defaults emaildelivery.RenderDefaults) {
			store.SeedRenderDefaults(defaults)
		},
	}
}

// RunStoreContract runs every Store contract case against a fresh Harness,
// in parallel, as mutationtest.RunContract does.
func RunStoreContract(t *testing.T, open func(t *testing.T) Harness) {
	t.Helper()
	for name, run := range storeContract {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			run(t, open(t))
		})
	}
}

// SESConfigJSON is a stored SES configuration.
const SESConfigJSON = `{"region":"ap-northeast-2","access_key_id":"key","secret_access_key":"secret","from_email":"sender@example.test"}`

// SESAdapter is an SES adapter as a create stores it, at createdAt.
func SESAdapter(name string, priority int, createdAt time.Time) emaildelivery.MailAdapter {
	return emaildelivery.MailAdapter{
		Name: name, Type: emaildelivery.AdapterTypeSES, Priority: priority, CreatedAt: createdAt, Config: []byte(SESConfigJSON),
	}
}

// stamp is a time both stores keep to the microsecond.
func stamp() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

func transact(t *testing.T, h Harness, work func(ctx context.Context, store emaildelivery.Store) error) error {
	t.Helper()
	return mutationtest.Transact(t, h.Harness, work)
}

func insertAdapters(t *testing.T, h Harness, adapters ...emaildelivery.MailAdapter) []emaildelivery.MailAdapter {
	t.Helper()
	stored := make([]emaildelivery.MailAdapter, len(adapters))
	require.NoError(t, transact(t, h, func(ctx context.Context, store emaildelivery.Store) error {
		for i, adapter := range adapters {
			var err error
			if stored[i], err = store.InsertMailAdapter(ctx, adapter); err != nil {
				return err
			}
		}
		return nil
	}))
	return stored
}

func readAdapter(t *testing.T, h Harness, id string) (emaildelivery.MailAdapter, error) {
	t.Helper()
	var adapter emaildelivery.MailAdapter
	err := mutationtest.Read(t, h.Harness, func(ctx context.Context, store emaildelivery.Store) (err error) {
		adapter, err = store.MailAdapter(ctx, id)
		return err
	})
	return adapter, err
}

func listAdapters(t *testing.T, h Harness, listing emaildelivery.MailAdapterListing) ([]string, int64) {
	t.Helper()
	var adapters []emaildelivery.MailAdapter
	var total int64
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store emaildelivery.Store) (err error) {
		adapters, total, err = store.MailAdapters(ctx, listing)
		return err
	}))
	names := make([]string, len(adapters))
	for i, adapter := range adapters {
		names[i] = adapter.Name
	}
	return names, total
}

func suppress(t *testing.T, h Harness, suppressions ...emaildelivery.Suppression) {
	t.Helper()
	require.NoError(t, transact(t, h, func(ctx context.Context, store emaildelivery.Store) error {
		for _, suppression := range suppressions {
			if err := store.InsertSuppression(ctx, suppression); err != nil {
				return err
			}
		}
		return nil
	}))
}

func activeSuppression(t *testing.T, h Harness, email string) (emaildelivery.Suppression, error) {
	t.Helper()
	var suppression emaildelivery.Suppression
	err := mutationtest.Read(t, h.Harness, func(ctx context.Context, store emaildelivery.Store) (err error) {
		suppression, err = store.ActiveSuppression(ctx, email)
		return err
	})
	return suppression, err
}

func complaint(email, referenceID string, at time.Time) emaildelivery.Suppression {
	return emaildelivery.Suppression{
		Email: email, Reason: emaildelivery.EmailSuppressionReasonSESComplaint, Source: emaildelivery.EmailSuppressionSourceSESCallback,
		ReferenceID: &referenceID, SuppressedAt: at,
	}
}

func uniqueAddress() string { return "suppressed-" + uuid.NewString() + "@example.test" }

var errRollback = errors.New("roll back")

var storeContract = map[string]func(t *testing.T, h Harness){
	"an inserted adapter reads back as stored with a new ID": func(t *testing.T, h Harness) {
		createdAt := stamp()
		inserted := insertAdapters(t, h, SESAdapter("Primary", 3, createdAt))[0]
		require.NotEmpty(t, inserted.ID)
		require.NotNil(t, inserted.UpdatedAt, "the column defaults the update time")

		stored, err := readAdapter(t, h, inserted.ID)
		require.NoError(t, err)
		require.Equal(t, "Primary", stored.Name)
		require.Equal(t, emaildelivery.AdapterTypeSES, stored.Type)
		require.Equal(t, 3, stored.Priority)
		require.False(t, stored.IsActive)
		require.True(t, createdAt.Equal(stored.CreatedAt), "created at %v, want %v", stored.CreatedAt, createdAt)
		require.JSONEq(t, SESConfigJSON, string(stored.Config))
	},
	"a missing adapter is not found to a read and a lock": func(t *testing.T, h Harness) {
		missing := uuid.NewString()
		_, err := readAdapter(t, h, missing)
		require.ErrorIs(t, err, emaildelivery.ErrMailAdapterNotFound)
		err = transact(t, h, func(ctx context.Context, store emaildelivery.Store) error {
			_, err := store.LockMailAdapter(ctx, missing)
			return err
		})
		require.ErrorIs(t, err, emaildelivery.ErrMailAdapterNotFound)
	},
	"an update writes only the changed columns": func(t *testing.T, h Harness) {
		original := insertAdapters(t, h, SESAdapter("Original", 1, stamp()))[0]
		name, updatedAt := "Renamed", stamp().Add(time.Minute)
		require.NoError(t, transact(t, h, func(ctx context.Context, store emaildelivery.Store) error {
			return store.UpdateMailAdapter(ctx, original.ID, emaildelivery.MailAdapterChanges{Name: &name, UpdatedAt: updatedAt})
		}))
		stored, err := readAdapter(t, h, original.ID)
		require.NoError(t, err)
		require.Equal(t, "Renamed", stored.Name)
		require.Equal(t, 1, stored.Priority)
		require.False(t, stored.IsActive)
		require.Equal(t, emaildelivery.AdapterTypeSES, stored.Type)
		require.JSONEq(t, string(original.Config), string(stored.Config))
		require.True(t, updatedAt.Equal(*stored.UpdatedAt))

		active, priority, smtp := true, int32(9), emaildelivery.AdapterTypeSMTP
		config := []byte(`{"host":"smtp.example.test","port":587,"secure":false,"user":"","password":"","from_email":"smtp@example.test","provider_hint":"kept"}`)
		require.NoError(t, transact(t, h, func(ctx context.Context, store emaildelivery.Store) error {
			return store.UpdateMailAdapter(ctx, original.ID, emaildelivery.MailAdapterChanges{
				IsActive: &active, Priority: &priority, Type: &smtp, Config: config, UpdatedAt: updatedAt,
			})
		}))
		stored, err = readAdapter(t, h, original.ID)
		require.NoError(t, err)
		require.Equal(t, "Renamed", stored.Name)
		require.True(t, stored.IsActive)
		require.Equal(t, 9, stored.Priority)
		require.Equal(t, emaildelivery.AdapterTypeSMTP, stored.Type)
		require.JSONEq(t, string(config), string(stored.Config))
	},
	"a deleted adapter is gone": func(t *testing.T, h Harness) {
		adapter := insertAdapters(t, h, SESAdapter("Deleted", 1, stamp()))[0]
		require.NoError(t, transact(t, h, func(ctx context.Context, store emaildelivery.Store) error {
			return store.DeleteMailAdapter(ctx, adapter.ID)
		}))
		_, err := readAdapter(t, h, adapter.ID)
		require.ErrorIs(t, err, emaildelivery.ErrMailAdapterNotFound)
	},
	"a failed transaction stores nothing": func(t *testing.T, h Harness) {
		var id string
		err := transact(t, h, func(ctx context.Context, store emaildelivery.Store) error {
			adapter, err := store.InsertMailAdapter(ctx, SESAdapter("Rolled back", 1, stamp()))
			if err != nil {
				return err
			}
			id = adapter.ID
			return errRollback
		})
		require.ErrorIs(t, err, errRollback)
		_, err = readAdapter(t, h, id)
		require.ErrorIs(t, err, emaildelivery.ErrMailAdapterNotFound)
	},
	"adapters list in priority order, then oldest first, one page at a time": func(t *testing.T, h Harness) {
		base := stamp()
		inactive := insertAdapters(t, h,
			SESAdapter("Second", 2, base.Add(time.Second)),
			SESAdapter("Third", 2, base.Add(2*time.Second)),
			SESAdapter("First", 1, base.Add(3*time.Second)),
		)
		active := true
		require.NoError(t, transact(t, h, func(ctx context.Context, store emaildelivery.Store) error {
			for _, adapter := range inactive[1:] {
				if err := store.UpdateMailAdapter(ctx, adapter.ID, emaildelivery.MailAdapterChanges{IsActive: &active, UpdatedAt: base}); err != nil {
					return err
				}
			}
			return nil
		}))

		names, total := listAdapters(t, h, emaildelivery.MailAdapterListing{Limit: 20})
		require.Equal(t, []string{"First", "Second", "Third"}, names)
		require.EqualValues(t, 3, total)
		names, total = listAdapters(t, h, emaildelivery.MailAdapterListing{Limit: 1, Offset: 1})
		require.Equal(t, []string{"Second"}, names)
		require.EqualValues(t, 3, total)
		names, total = listAdapters(t, h, emaildelivery.MailAdapterListing{ActiveOnly: true, Limit: 20})
		require.Equal(t, []string{"First", "Third"}, names)
		require.EqualValues(t, 2, total)
		names, total = listAdapters(t, h, emaildelivery.MailAdapterListing{Limit: 20, Offset: 3})
		require.Empty(t, names)
		require.EqualValues(t, 3, total)
	},
	"a locked adapter makes a second writer wait for the first commit": func(t *testing.T, h Harness) {
		adapter := insertAdapters(t, h, SESAdapter("Locked", 1, stamp()))[0]
		name := "Committed first"
		var seen emaildelivery.MailAdapter
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store emaildelivery.Store) error {
				_, err := store.LockMailAdapter(ctx, adapter.ID)
				return err
			},
			func(ctx context.Context, store emaildelivery.Store) error {
				return store.UpdateMailAdapter(ctx, adapter.ID, emaildelivery.MailAdapterChanges{Name: &name, UpdatedAt: stamp()})
			},
			func(ctx context.Context, store emaildelivery.Store) (err error) {
				seen, err = store.LockMailAdapter(ctx, adapter.ID)
				return err
			},
		)
		require.NoError(t, err)
		require.Equal(t, name, seen.Name, "the second writer reads the first one's commit")
	},
	"an address's active suppression matches it in any case": func(t *testing.T, h Harness) {
		address := uniqueAddress()
		suppress(t, h, complaint("Mixed."+address, "reference", stamp()))
		found, err := activeSuppression(t, h, "mixed."+address)
		require.NoError(t, err)
		require.Equal(t, "Mixed."+address, found.Email)
		require.Equal(t, emaildelivery.EmailSuppressionReasonSESComplaint, found.Reason)
		require.Equal(t, "reference", *found.ReferenceID)
		require.Nil(t, found.ReleasedAt)

		_, err = activeSuppression(t, h, uniqueAddress())
		require.ErrorIs(t, err, emaildelivery.ErrSuppressionNotFound)
	},
	"an address has at most one active suppression": func(t *testing.T, h Harness) {
		address := uniqueAddress()
		suppress(t, h, complaint(address, "first", stamp()))
		err := transact(t, h, func(ctx context.Context, store emaildelivery.Store) error {
			return store.InsertSuppression(ctx, complaint(strings.ToUpper(address), "second", stamp()))
		})
		require.Error(t, err, "the unique index refuses a second active suppression")

		first, err := activeSuppression(t, h, address)
		require.NoError(t, err)
		require.NoError(t, transact(t, h, func(ctx context.Context, store emaildelivery.Store) error {
			return store.ReleaseSuppression(ctx, first.ID, stamp(), nil)
		}))
		suppress(t, h, complaint(address, "after release", stamp()))
		second, err := activeSuppression(t, h, address)
		require.NoError(t, err)
		require.Equal(t, "after release", *second.ReferenceID)
	},
	"a released suppression is no longer active and records who released it": func(t *testing.T, h Harness) {
		address := uniqueAddress()
		suppress(t, h, complaint(address, "reference", stamp()))
		suppression, err := activeSuppression(t, h, address)
		require.NoError(t, err)
		releasedAt, releasedBy := stamp(), uuid.NewString()
		var locked emaildelivery.Suppression
		require.NoError(t, transact(t, h, func(ctx context.Context, store emaildelivery.Store) (err error) {
			if locked, err = store.LockActiveSuppression(ctx, address); err != nil {
				return err
			}
			return store.ReleaseSuppression(ctx, locked.ID, releasedAt, &releasedBy)
		}))
		require.Equal(t, suppression.ID, locked.ID)
		_, err = activeSuppression(t, h, address)
		require.ErrorIs(t, err, emaildelivery.ErrSuppressionNotFound)
		err = transact(t, h, func(ctx context.Context, store emaildelivery.Store) error {
			_, err := store.LockActiveSuppression(ctx, address)
			return err
		})
		require.ErrorIs(t, err, emaildelivery.ErrSuppressionNotFound)
	},
	"active suppressions are listed only for the asked addresses": func(t *testing.T, h Harness) {
		asked, released, other := uniqueAddress(), uniqueAddress(), uniqueAddress()
		suppress(t, h, complaint(asked, "asked", stamp()), complaint(released, "released", stamp()), complaint(other, "other", stamp()))
		require.NoError(t, transact(t, h, func(ctx context.Context, store emaildelivery.Store) error {
			suppression, err := store.LockActiveSuppression(ctx, released)
			if err != nil {
				return err
			}
			return store.ReleaseSuppression(ctx, suppression.ID, stamp(), nil)
		}))
		var found []emaildelivery.Suppression
		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store emaildelivery.Store) (err error) {
			found, err = store.ActiveSuppressions(ctx, []string{asked, released, uniqueAddress()})
			return err
		}))
		require.Len(t, found, 1)
		require.Equal(t, asked, found[0].Email)
	},
	"a recorded suppression is found by its exact source, reason and reference, released or not": func(t *testing.T, h Harness) {
		address := uniqueAddress()
		suppress(t, h, complaint(address, "message-1", stamp()))
		require.NoError(t, transact(t, h, func(ctx context.Context, store emaildelivery.Store) error {
			suppression, err := store.LockActiveSuppression(ctx, address)
			if err != nil {
				return err
			}
			return store.ReleaseSuppression(ctx, suppression.ID, stamp(), nil)
		}))
		recorded := func(email, source, reason, referenceID string) bool {
			var found bool
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store emaildelivery.Store) (err error) {
				found, err = store.SuppressionRecorded(ctx, email, source, reason, referenceID)
				return err
			}))
			return found
		}
		ses, complained := emaildelivery.EmailSuppressionSourceSESCallback, emaildelivery.EmailSuppressionReasonSESComplaint
		require.True(t, recorded(address, ses, complained, "message-1"))
		require.False(t, recorded(address, ses, complained, "message-2"))
		require.False(t, recorded(address, ses, emaildelivery.EmailSuppressionReasonSESBounce, "message-1"))
		require.False(t, recorded(address, emaildelivery.EmailSuppressionSourceEmailWorker, complained, "message-1"))
		require.False(t, recorded(uniqueAddress(), ses, complained, "message-1"))
	},
	"a replaced suppression keeps its address and identity": func(t *testing.T, h Harness) {
		address := uniqueAddress()
		suppress(t, h, complaint(address, "reference", stamp()))
		original, err := activeSuppression(t, h, address)
		require.NoError(t, err)
		suppressedAt, lastError := stamp().Add(time.Minute), "550 user unknown"
		require.NoError(t, transact(t, h, func(ctx context.Context, store emaildelivery.Store) error {
			return store.ReplaceSuppression(ctx, original.ID, emaildelivery.Suppression{
				Reason: emaildelivery.EmailSuppressionReasonInvalidRecipient, Source: emaildelivery.EmailSuppressionSourceEmailWorker,
				LastError: &lastError, SuppressedAt: suppressedAt,
			})
		}))
		replaced, err := activeSuppression(t, h, address)
		require.NoError(t, err)
		require.Equal(t, original.ID, replaced.ID)
		require.Equal(t, address, replaced.Email)
		require.Equal(t, emaildelivery.EmailSuppressionReasonInvalidRecipient, replaced.Reason)
		require.Equal(t, emaildelivery.EmailSuppressionSourceEmailWorker, replaced.Source)
		require.Nil(t, replaced.ReferenceID)
		require.Equal(t, lastError, *replaced.LastError)
		require.True(t, suppressedAt.Equal(replaced.SuppressedAt))
	},
	"a locked suppression makes a second writer wait for the first commit": func(t *testing.T, h Harness) {
		address := uniqueAddress()
		suppress(t, h, complaint(address, "reference", stamp()))
		var seen emaildelivery.Suppression
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store emaildelivery.Store) error {
				_, err := store.LockActiveSuppression(ctx, address)
				return err
			},
			func(ctx context.Context, store emaildelivery.Store) error {
				locked, err := store.ActiveSuppression(ctx, address)
				if err != nil {
					return err
				}
				return store.ReplaceSuppression(ctx, locked.ID, emaildelivery.Suppression{
					Reason: emaildelivery.EmailSuppressionReasonManual, Source: emaildelivery.EmailSuppressionSourceAdmin, SuppressedAt: stamp(),
				})
			},
			func(ctx context.Context, store emaildelivery.Store) (err error) {
				seen, err = store.LockActiveSuppression(ctx, address)
				return err
			},
		)
		require.NoError(t, err)
		require.Equal(t, emaildelivery.EmailSuppressionReasonManual, seen.Reason, "the second writer reads the first one's commit")
	},
	"render defaults are the site settings": func(t *testing.T, h Harness) {
		h.SeedSiteSettings(t, emaildelivery.RenderDefaults{
			SiteTitle: "Example Studio", CompanyName: "Example Company",
			LegalEmail: "legal@example.test", ContactEmail: "contact@example.test",
		})
		var defaults emaildelivery.RenderDefaults
		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store emaildelivery.Store) (err error) {
			defaults, err = store.EmailRenderDefaults(ctx, "https://cdn.example.test")
			return err
		}))
		require.Equal(t, emaildelivery.RenderDefaults{
			SiteTitle: "Example Studio", CompanyName: "Example Company",
			LegalEmail: "legal@example.test", ContactEmail: "contact@example.test",
		}, defaults)
	},
}
