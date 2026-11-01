package translationtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
)

// Harness is one Translation Store a contract case runs against.
type Harness struct {
	mutationtest.Harness[application.Store]
	// SeedMember makes a Member a job can name as its requester.
	SeedMember func(t *testing.T) string
}

// FakeHarness is the Harness of a fresh in-memory Store.
func FakeHarness() Harness {
	return Harness{
		Harness:    mutationtest.FakeHarness[application.Store](NewStore()),
		SeedMember: func(*testing.T) string { return uuid.NewString() },
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

// NewJob returns a queued job of a post with a valid request artifact.
func NewJob(requestedBy string, now time.Time) application.Job {
	manifest, err := translation.CanonicalizeRequestManifest([]byte(`{"version":1}`))
	if err != nil {
		panic(err)
	}
	xliff := []byte(`<xliff version="2.2"/>`)
	return application.Job{
		ID: uuid.NewString(), EntityType: "post", EntityID: uuid.NewString(),
		SourceLocale: "en", TargetLocale: "ko",
		RequestArtifactDigest: translation.RequestArtifactDigest(xliff, manifest),
		OperationID:           uuid.NewString(), Status: application.JobQueued,
		RequestedByMemberID: requestedBy, RequestXLIFF: xliff, RequestManifest: manifest,
		RequestedAt: now, CreatedAt: now, UpdatedAt: now,
	}
}

func contractNow() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

func insertJob(t *testing.T, h Harness, job application.Job) {
	t.Helper()
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store application.Store) error {
		inserted, err := store.InsertJob(ctx, job)
		if err == nil && !inserted {
			err = errors.New("job was not inserted")
		}
		return err
	}))
}

func readJob(t *testing.T, h Harness, jobID string) (application.Job, bool) {
	t.Helper()
	var job application.Job
	var found bool
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store application.Store) (err error) {
		job, found, err = store.Job(ctx, jobID)
		return err
	}))
	return job, found
}

func transactBool(t *testing.T, h Harness, work func(ctx context.Context, store application.Store) (bool, error)) bool {
	t.Helper()
	var result bool
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store application.Store) (err error) {
		result, err = work(ctx, store)
		return err
	}))
	return result
}

func otherDigest(job application.Job) string {
	sum := sha256.Sum256([]byte(job.RequestArtifactDigest))
	return hex.EncodeToString(sum[:])
}

var storeContract = map[string]func(t *testing.T, h Harness){
	"a queued job is claimed once and keeps its first start": func(t *testing.T, h Harness) {
		now := contractNow()
		job := NewJob(h.SeedMember(t), now)
		insertJob(t, h, job)

		claimedAt := now.Add(time.Second)
		require.True(t, transactBool(t, h, func(ctx context.Context, store application.Store) (bool, error) {
			return store.ClaimJob(ctx, job.ID, "deepl", "default", claimedAt)
		}))
		require.False(t, transactBool(t, h, func(ctx context.Context, store application.Store) (bool, error) {
			return store.ClaimJob(ctx, job.ID, "deepl", "default", claimedAt.Add(time.Second))
		}))
		stored, found := readJob(t, h, job.ID)
		require.True(t, found)
		require.Equal(t, application.JobRunning, stored.Status)
		require.Equal(t, "deepl", *stored.Provider)
		require.Equal(t, "default", *stored.Model)
		require.True(t, claimedAt.Equal(*stored.StartedAt))
		require.Empty(t, stored.RequestXLIFF, "a state read leaves the request artifact out")

		require.True(t, transactBool(t, h, func(ctx context.Context, store application.Store) (bool, error) {
			return store.ResumeJob(ctx, job.ID, "llm", "gemini", claimedAt.Add(2*time.Second))
		}))
		resumed, _ := readJob(t, h, job.ID)
		require.Equal(t, "llm", *resumed.Provider)
		require.True(t, claimedAt.Equal(*resumed.StartedAt))
	},
	"a stored provider document pins claims and resumes to its provider": func(t *testing.T, h Harness) {
		now := contractNow()
		job := NewJob(h.SeedMember(t), now)
		insertJob(t, h, job)
		require.True(t, transactBool(t, h, func(ctx context.Context, store application.Store) (bool, error) {
			return store.ClaimJob(ctx, job.ID, "deepl", "default", now)
		}))
		document := application.ProviderDocument{
			Provider: "deepl", Model: "default", DocumentID: "document-a", DocumentKey: "key-a", SubmittedAt: now,
		}
		mismatched := document
		mismatched.Provider = "llm"
		require.False(t, transactBool(t, h, func(ctx context.Context, store application.Store) (bool, error) {
			return store.SetJobProviderDocument(ctx, job.ID, mismatched, now)
		}), "a document of another provider is not stored")
		require.True(t, transactBool(t, h, func(ctx context.Context, store application.Store) (bool, error) {
			return store.SetJobProviderDocument(ctx, job.ID, document, now)
		}))

		require.False(t, transactBool(t, h, func(ctx context.Context, store application.Store) (bool, error) {
			return store.ResumeJob(ctx, job.ID, "llm", "gemini", now)
		}))
		require.True(t, transactBool(t, h, func(ctx context.Context, store application.Store) (bool, error) {
			return store.ResumeJob(ctx, job.ID, "deepl", "default", now)
		}))
		var locked application.Job
		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store application.Store) (err error) {
			locked, err = store.LockJobProviderDocument(ctx, job.ID)
			return err
		}))
		require.Equal(t, "document-a", *locked.ProviderDocumentID)
		require.Equal(t, "key-a", *locked.ProviderDocumentKey)
		require.True(t, now.Equal(*locked.ProviderDocumentSubmittedAt))

		stale := document
		stale.DocumentKey = "key-b"
		require.False(t, transactBool(t, h, func(ctx context.Context, store application.Store) (bool, error) {
			return store.ClearJobProviderDocument(ctx, job.ID, stale, now)
		}), "only the exact stored document is cleared")
		require.True(t, transactBool(t, h, func(ctx context.Context, store application.Store) (bool, error) {
			return store.ClearJobProviderDocument(ctx, job.ID, document, now)
		}))
		cleared, _ := readJob(t, h, job.ID)
		require.Nil(t, cleared.ProviderDocumentID)
		require.Nil(t, cleared.ProviderDocumentKey)
		require.Nil(t, cleared.ProviderDocumentSubmittedAt)
	},
	"a job is deleted only in the statuses named": func(t *testing.T, h Harness) {
		job := NewJob(h.SeedMember(t), contractNow())
		insertJob(t, h, job)
		var deleted int64
		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store application.Store) (err error) {
			deleted, err = store.DeleteJob(ctx, job.ID, application.JobRunning)
			return err
		}))
		require.Zero(t, deleted)
		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store application.Store) (err error) {
			deleted, err = store.DeleteJob(ctx, job.ID, application.JobQueued, application.JobRunning)
			return err
		}))
		require.EqualValues(t, 1, deleted)
		_, found := readJob(t, h, job.ID)
		require.False(t, found)
	},
	"a second job for the same request or operation is not inserted": func(t *testing.T, h Harness) {
		member := h.SeedMember(t)
		now := contractNow()
		first := NewJob(member, now)
		insertJob(t, h, first)

		sameRequest := first
		sameRequest.ID, sameRequest.OperationID = uuid.NewString(), uuid.NewString()
		sameOperation := first
		sameOperation.ID, sameOperation.RequestArtifactDigest = uuid.NewString(), otherDigest(first)
		for _, duplicate := range []application.Job{sameRequest, sameOperation} {
			require.False(t, transactBool(t, h, func(ctx context.Context, store application.Store) (bool, error) {
				return store.InsertJob(ctx, duplicate)
			}))
		}

		otherLocale := sameRequest
		otherLocale.TargetLocale = "ja"
		require.True(t, transactBool(t, h, func(ctx context.Context, store application.Store) (bool, error) {
			return store.InsertJob(ctx, otherLocale)
		}))
		var active []application.Job
		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store application.Store) (err error) {
			active, err = store.LockActiveJobs(ctx, application.ActiveJobKey{
				EntityType: first.EntityType, EntityID: first.EntityID, TargetLocale: first.TargetLocale,
				RequestArtifactDigest: first.RequestArtifactDigest,
			})
			return err
		}))
		require.Len(t, active, 1)
		require.Equal(t, first.ID, active[0].ID)
	},
	"locked active jobs make a second locker wait for the first commit": func(t *testing.T, h Harness) {
		job := NewJob(h.SeedMember(t), contractNow())
		insertJob(t, h, job)
		key := application.ActiveJobKey{
			EntityType: job.EntityType, EntityID: job.EntityID, TargetLocale: job.TargetLocale,
			RequestArtifactDigest: job.RequestArtifactDigest,
		}
		var seen []application.Job
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store application.Store) error {
				active, err := store.LockActiveJobs(ctx, key)
				require.Len(t, active, 1)
				return err
			},
			func(ctx context.Context, store application.Store) error {
				_, err := store.DeleteJob(ctx, job.ID, application.JobQueued)
				return err
			},
			func(ctx context.Context, store application.Store) (err error) {
				seen, err = store.LockActiveJobs(ctx, key)
				return err
			},
		)
		require.NoError(t, err)
		require.Empty(t, seen, "the second locker sees the first's delete")
	},
	"a running job's lock makes a second runner step wait and see it gone": func(t *testing.T, h Harness) {
		job := NewJob(h.SeedMember(t), contractNow())
		insertJob(t, h, job)
		transactBool(t, h, func(ctx context.Context, store application.Store) (bool, error) {
			return store.ClaimJob(ctx, job.ID, "deepl", "default", contractNow())
		})
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store application.Store) error {
				status, err := store.LockJobStatus(ctx, job.ID)
				require.Equal(t, application.JobRunning, status)
				return err
			},
			func(ctx context.Context, store application.Store) error {
				_, err := store.DeleteJob(ctx, job.ID, application.JobRunning)
				return err
			},
			func(ctx context.Context, store application.Store) error {
				_, err := store.LockJobStatus(ctx, job.ID)
				return err
			},
		)
		require.ErrorIs(t, err, application.ErrJobNotFound)
	},
	"a missing job answers not found": func(t *testing.T, h Harness) {
		missing := uuid.NewString()
		_, found := readJob(t, h, missing)
		require.False(t, found)
		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store application.Store) error {
			_, err := store.JobForCommand(ctx, missing, true)
			require.ErrorIs(t, err, application.ErrJobNotFound)
			_, err = store.RequestArtifact(ctx, missing)
			require.ErrorIs(t, err, application.ErrJobNotFound)
			_, err = store.LockJobProviderDocument(ctx, missing)
			require.ErrorIs(t, err, application.ErrJobNotFound)
			return nil
		}))
		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store application.Store) error {
			_, err := store.LockJobStatus(ctx, missing)
			require.ErrorIs(t, err, application.ErrJobNotFound)
			return nil
		}))
	},
	"the request artifact reads back to the same canonical manifest and digest": func(t *testing.T, h Harness) {
		job := NewJob(h.SeedMember(t), contractNow())
		insertJob(t, h, job)
		var stored application.StoredRequestArtifact
		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store application.Store) (err error) {
			stored, err = store.RequestArtifact(ctx, job.ID)
			return err
		}))
		require.Equal(t, job.RequestXLIFF, stored.XLIFF)
		require.Equal(t, job.RequestArtifactDigest, stored.Digest)
		manifest, err := translation.CanonicalizeRequestManifest(stored.Manifest)
		require.NoError(t, err)
		require.Equal(t, job.RequestArtifactDigest, translation.RequestArtifactDigest(stored.XLIFF, manifest))

		var forCommand application.Job
		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store application.Store) (err error) {
			forCommand, err = store.JobForCommand(ctx, job.ID, true)
			return err
		}))
		require.Equal(t, job.RequestXLIFF, forCommand.RequestXLIFF)
		require.JSONEq(t, string(job.RequestManifest), string(forCommand.RequestManifest))
	},
	"a failed transaction leaves the job as it was": func(t *testing.T, h Harness) {
		job := NewJob(h.SeedMember(t), contractNow())
		insertJob(t, h, job)
		rejected := errors.New("rejected")
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store application.Store) error {
			if _, err := store.ClaimJob(ctx, job.ID, "deepl", "default", contractNow()); err != nil {
				return err
			}
			return rejected
		})
		require.ErrorIs(t, err, rejected)
		stored, _ := readJob(t, h, job.ID)
		require.Equal(t, application.JobQueued, stored.Status)
		require.Nil(t, stored.Provider)
	},
	"settings read the singleton and store each changed field": func(t *testing.T, h Harness) {
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
			updatedAt, err = store.UpdateRuntimeSettings(ctx, application.SettingsChange{
				DefaultLocale: &locale, ProtectedTerms: []string{"Geul", "Rawsector"}, TermsChanged: true,
			})
			return err
		}))
		require.False(t, updatedAt.IsZero())
		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store application.Store) (err error) {
			settings, err = store.LockRuntimeSettings(ctx)
			return err
		}))
		require.Equal(t, "ko", settings.DefaultLocale)
		require.Equal(t, []string{"Geul", "Rawsector"}, settings.ProtectedTerms)
		require.True(t, updatedAt.Equal(*settings.UpdatedAt))

		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store application.Store) error {
			_, err := store.UpdateRuntimeSettings(ctx, application.SettingsChange{TermsChanged: true})
			return err
		}))
		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store application.Store) (err error) {
			settings, err = store.LockRuntimeSettings(ctx)
			return err
		}))
		require.Empty(t, settings.ProtectedTerms, "an emptied term list is stored empty")
		require.Equal(t, "ko", settings.DefaultLocale, "an unchanged field keeps its value")
	},
	"providers keep priority order and their stored config": func(t *testing.T, h Harness) {
		now := contractNow()
		config, err := json.Marshal(application.DeepLConfig{APIKey: "key", APIBaseURL: "https://api.deepl.com"})
		require.NoError(t, err)
		var second, first application.Provider
		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store application.Store) (err error) {
			if second, err = store.InsertProvider(ctx, application.Provider{
				Name: "second", Type: application.ProviderTypeDeepL, IsActive: true, Priority: 2, Config: config, CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				return err
			}
			if first, err = store.InsertProvider(ctx, application.Provider{
				Name: "first", Type: application.ProviderTypeDeepL, IsActive: true, Priority: 1, Config: config, CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				return err
			}
			_, err = store.InsertProvider(ctx, application.Provider{
				Name: "inactive", Type: application.ProviderTypeDeepL, Priority: 0, Config: config, CreatedAt: now, UpdatedAt: now,
			})
			return err
		}))
		require.NotEmpty(t, first.ID)
		var active []application.Provider
		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store application.Store) (err error) {
			active, err = store.ActiveProviders(ctx)
			return err
		}))
		require.Len(t, active, 2)
		require.Equal(t, []string{first.ID, second.ID}, []string{active[0].ID, active[1].ID})
		require.JSONEq(t, string(config), string(active[0].Config))

		name, inactive := "renamed", false
		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store application.Store) error {
			if _, err := store.LockProvider(ctx, first.ID); err != nil {
				return err
			}
			updatedAt, err := store.UpdateProvider(ctx, first.ID, application.ProviderChange{Name: &name, IsActive: &inactive})
			require.False(t, updatedAt.IsZero())
			return err
		}))
		var page []application.Provider
		var total int64
		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store application.Store) (err error) {
			page, total, err = store.ListProviders(ctx, application.ProviderListQuery{ActiveOnly: true, Limit: 10})
			return err
		}))
		require.EqualValues(t, 1, total)
		require.Equal(t, second.ID, page[0].ID)

		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store application.Store) error {
			return store.DeleteProvider(ctx, first.ID)
		}))
		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store application.Store) error {
			_, err := store.LockProvider(ctx, first.ID)
			require.ErrorIs(t, err, application.ErrProviderNotFound)
			return nil
		}))
	},
	"a locked provider makes a second updater wait and read the first's change": func(t *testing.T, h Harness) {
		now := contractNow()
		var provider application.Provider
		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store application.Store) (err error) {
			provider, err = store.InsertProvider(ctx, application.Provider{
				Name: "before", Type: application.ProviderTypeDeepL, Config: []byte(`{"api_key":"key"}`), CreatedAt: now, UpdatedAt: now,
			})
			return err
		}))
		name := "after"
		var seen application.Provider
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store application.Store) error {
				_, err := store.LockProvider(ctx, provider.ID)
				return err
			},
			func(ctx context.Context, store application.Store) error {
				_, err := store.UpdateProvider(ctx, provider.ID, application.ProviderChange{Name: &name})
				return err
			},
			func(ctx context.Context, store application.Store) (err error) {
				seen, err = store.LockProvider(ctx, provider.ID)
				return err
			},
		)
		require.NoError(t, err)
		require.Equal(t, "after", seen.Name)
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
