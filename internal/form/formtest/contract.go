package formtest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// Harness is one form.Store implementation the contract runs against, with
// the rows of other domains a case needs.
type Harness struct {
	mutationtest.Harness[form.Store]
	// SeedDocument stores an empty Form content document and returns its
	// ID.
	SeedDocument func(t *testing.T) string
	// SeedMember stores a Member and returns its ID.
	SeedMember func(t *testing.T) string
	// SeedFile stores a File and returns its ID.
	SeedFile func(t *testing.T) string
	// SeedSubmission stores a submission and returns it as stored.
	SeedSubmission func(t *testing.T, submission form.Submission) form.Submission
}

// FakeHarness is the Harness of the fake Store.
func FakeHarness() Harness {
	return fakeHarness(NewStore())
}

func fakeHarness(store *Store) Harness {
	return Harness{
		Harness: mutationtest.FakeHarness[form.Store](store),
		SeedDocument: func(*testing.T) string {
			id := uuid.New()
			store.mu.Lock()
			store.documents[id] = uuid.New()
			store.mu.Unlock()
			return id.String()
		},
		SeedMember: func(*testing.T) string { return uuid.NewString() },
		SeedFile: func(*testing.T) string {
			id := uuid.NewString()
			store.PutFile(id, nil)
			return id
		},
		SeedSubmission: func(_ *testing.T, submission form.Submission) form.Submission {
			if err := store.InsertSubmission(context.Background(), &submission); err != nil {
				panic(err)
			}
			return submission
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

func transact(t *testing.T, h Harness, work func(ctx context.Context, store form.Store) error) {
	t.Helper()
	require.NoError(t, mutationtest.Transact(t, h.Harness, work))
}

func text(value string) *string { return &value }

// stamp is a time PostgreSQL stores exactly.
func stamp() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

const contractSchema = `{"id":"schema","steps":[{"id":"step-1","title":"Contact","fields":[{"id":"field-1","key":"email","label":"Email","type":"email"}]}]}`

// insert stores a draft Form with its source locale in its own transaction.
func insert(t *testing.T, h Harness, change func(*form.Form)) form.Form {
	t.Helper()
	value := form.Form{
		ContentDocumentID: h.SeedDocument(t), SourceLocale: "en", Status: form.StatusDraft, CreatedAt: stamp(),
	}
	if change != nil {
		change(&value)
	}
	transact(t, h, func(ctx context.Context, store form.Store) error {
		if err := store.InsertForm(ctx, &value); err != nil {
			return err
		}
		return store.SaveSourceLocale(ctx, value.ID, value.SourceLocale, text("Contact"), []byte(contractSchema), text("Contact"), stamp())
	})
	return value
}

func submit(t *testing.T, h Harness, submission form.Submission) form.Submission {
	t.Helper()
	return h.SeedSubmission(t, submission)
}

var storeContract = map[string]func(t *testing.T, h Harness){
	"a missing form answers its sentinels": func(t *testing.T, h Harness) {
		missing := uuid.NewString()
		transact(t, h, func(ctx context.Context, store form.Store) error {
			_, err := store.FindForm(ctx, form.ByID, missing)
			require.ErrorIs(t, err, form.ErrFormNotFound)
			_, err = store.FindForm(ctx, form.BySlug, "missing-"+missing)
			require.ErrorIs(t, err, form.ErrFormNotFound)
			_, err = store.LockForm(ctx, missing)
			require.ErrorIs(t, err, form.ErrFormNotFound)
			_, err = store.LockFormRoot(ctx, missing)
			require.ErrorIs(t, err, form.ErrFormNotFound)
			_, err = store.LockFeaturedImage(ctx, missing)
			require.ErrorIs(t, err, form.ErrFormNotFound)
			_, err = store.DocumentRoot(ctx, missing, form.UpdateLock)
			require.ErrorIs(t, err, form.ErrFormNotFound)
			_, err = store.SourceLocale(ctx, missing)
			require.ErrorIs(t, err, form.ErrFormNotFound)
			_, err = store.LocaleState(ctx, missing, "en")
			require.ErrorIs(t, err, form.ErrLocaleNotFound)
			_, err = store.FindSubmission(ctx, missing)
			require.ErrorIs(t, err, form.ErrSubmissionNotFound)
			_, err = store.FileID(ctx, missing)
			require.ErrorIs(t, err, form.ErrFileNotFound)
			_, err = store.DocumentRevision(ctx, uuid.New())
			require.ErrorIs(t, err, form.ErrContentDocumentNotFound)
			exists, err := store.FormExists(ctx, missing)
			require.NoError(t, err)
			require.False(t, exists)
			_, exists, err = store.Locale(ctx, missing, "en", true)
			require.NoError(t, err)
			require.False(t, exists)
			return nil
		})
	},
	"an inserted form reads back by ID and slug and locks by its stored ID": func(t *testing.T, h Harness) {
		slug := "contract-" + uuid.NewString()[:8]
		roles := []string{"user"}
		inserted := insert(t, h, func(value *form.Form) {
			value.Slug, value.IsPublic, value.RequireAuth, value.AllowedRoles = &slug, true, new(true), roles
		})
		require.NotEmpty(t, inserted.ID)
		require.NotNil(t, inserted.UpdatedAt)
		transact(t, h, func(ctx context.Context, store form.Store) error {
			byID, err := store.FindForm(ctx, form.ByID, strings.ToUpper(inserted.ID))
			require.NoError(t, err)
			require.Equal(t, inserted, byID)
			bySlug, err := store.FindForm(ctx, form.BySlug, slug)
			require.NoError(t, err)
			require.Equal(t, inserted.ID, bySlug.ID)
			locked, err := store.LockFormRoot(ctx, strings.ToUpper(inserted.ID))
			require.NoError(t, err)
			require.Equal(t, inserted.ID, locked)
			root, err := store.DocumentRoot(ctx, inserted.ID, form.KeyShareLock)
			require.NoError(t, err)
			require.Equal(t, form.DocumentRoot{Status: form.StatusDraft, SourceLocale: "en", DocumentID: inserted.ContentDocumentID}, root)
			source, err := store.SourceLocale(ctx, inserted.ID)
			require.NoError(t, err)
			require.Equal(t, "en", source)
			exists, err := store.FormExists(ctx, inserted.ID)
			require.NoError(t, err)
			require.True(t, exists)
			taken, err := store.SlugTaken(ctx, slug, "")
			require.NoError(t, err)
			require.True(t, taken)
			taken, err = store.SlugTaken(ctx, slug, inserted.ID)
			require.NoError(t, err)
			require.False(t, taken)
			return nil
		})
	},
	"an update writes only the named settings": func(t *testing.T, h Harness) {
		slug := "settings-" + uuid.NewString()[:8]
		inserted := insert(t, h, func(value *form.Form) { value.Slug = &slug })
		next := inserted
		next.IsPublic, next.MaxSubmissions, next.Slug, next.AllowedRoles = true, new(int32(3)), nil, []string{}
		next.Status = form.StatusPublished
		at := stamp().Add(time.Minute)
		transact(t, h, func(ctx context.Context, store form.Store) error {
			return store.UpdateSettings(ctx, next, []form.Setting{form.SettingIsPublic, form.SettingMaxSubmissions, form.SettingStatus, form.SettingAllowedRoles}, at)
		})
		transact(t, h, func(ctx context.Context, store form.Store) error {
			stored, err := store.LockForm(ctx, inserted.ID)
			require.NoError(t, err)
			require.True(t, stored.IsPublic)
			require.Equal(t, int32(3), *stored.MaxSubmissions)
			require.Equal(t, form.StatusPublished, stored.Status)
			require.Equal(t, []string{}, stored.AllowedRoles)
			require.Equal(t, &slug, stored.Slug, "an unnamed setting keeps its value")
			require.True(t, at.Equal(*stored.UpdatedAt))
			return nil
		})
	},
	"a featured image is stored and cleared": func(t *testing.T, h Harness) {
		inserted := insert(t, h, nil)
		fileID := h.SeedFile(t)
		transact(t, h, func(ctx context.Context, store form.Store) error {
			stored, err := store.FileID(ctx, strings.ToUpper(fileID))
			require.NoError(t, err)
			require.Equal(t, fileID, stored)
			return store.SetFeaturedImage(ctx, inserted.ID, &fileID, stamp())
		})
		transact(t, h, func(ctx context.Context, store form.Store) error {
			current, err := store.LockFeaturedImage(ctx, inserted.ID)
			require.NoError(t, err)
			require.Equal(t, &fileID, current)
			return store.SetFeaturedImage(ctx, inserted.ID, nil, stamp())
		})
		transact(t, h, func(ctx context.Context, store form.Store) error {
			current, err := store.LockFeaturedImage(ctx, inserted.ID)
			require.NoError(t, err)
			require.Nil(t, current)
			return nil
		})
	},
	"a source locale save keeps the title a nil value leaves and requires a schema": func(t *testing.T, h Harness) {
		inserted := insert(t, h, nil)
		transact(t, h, func(ctx context.Context, store form.Store) error {
			return store.SaveSourceLocale(ctx, inserted.ID, "en", nil, []byte(contractSchema), nil, stamp())
		})
		transact(t, h, func(ctx context.Context, store form.Store) error {
			state, err := store.LocaleState(ctx, inserted.ID, "en")
			require.NoError(t, err)
			require.Equal(t, "Contact", *state.Title, "a nil title keeps the stored one")
			require.JSONEq(t, contractSchema, string(state.ContentJSON))
			require.Nil(t, state.ContentText, "the text is always written")
			return store.SaveSourceLocale(ctx, inserted.ID, "en", text("Renamed"), []byte(contractSchema), text("Renamed"), stamp())
		})
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store form.Store) error {
			return store.SaveSourceLocale(ctx, inserted.ID, "en", text("No schema"), nil, nil, stamp())
		})
		require.Error(t, err, "content_json is required even for a stored locale")
		transact(t, h, func(ctx context.Context, store form.Store) error {
			state, err := store.LocaleState(ctx, inserted.ID, "en")
			require.NoError(t, err)
			require.Equal(t, "Renamed", *state.Title)
			require.Equal(t, "Renamed", *state.ContentText)
			return nil
		})
	},
	"an empty locale is created once": func(t *testing.T, h Harness) {
		inserted := insert(t, h, nil)
		transact(t, h, func(ctx context.Context, store form.Store) error {
			created, err := store.CreateEmptyLocale(ctx, inserted.ID, "ko", []byte(contractSchema), stamp())
			require.NoError(t, err)
			require.True(t, created)
			created, err = store.CreateEmptyLocale(ctx, inserted.ID, "ko", []byte(contractSchema), stamp())
			require.NoError(t, err)
			require.False(t, created)
			locale, exists, err := store.Locale(ctx, inserted.ID, "ko", false)
			require.NoError(t, err)
			require.True(t, exists)
			require.Nil(t, locale.Title)
			require.Nil(t, locale.ContentText)
			require.JSONEq(t, contractSchema, string(locale.Schema))
			return nil
		})
	},
	"a locale update writes only what it sets and moves the revision": func(t *testing.T, h Harness) {
		inserted := insert(t, h, nil)
		at := stamp()
		transact(t, h, func(ctx context.Context, store form.Store) error {
			return store.InsertLocale(ctx, inserted.ID, "ko", form.LocaleChange{
				SetTitle: true, Title: text("문의"), SetSchema: true, Schema: []byte(contractSchema), ContentText: text("문의"), UpdatedAt: at,
			}, at)
		})
		var before form.Locale
		transact(t, h, func(ctx context.Context, store form.Store) error {
			var err error
			before, _, err = store.Locale(ctx, inserted.ID, "ko", true)
			require.NoError(t, err)
			require.True(t, at.Equal(before.UpdatedAt))
			updated, err := store.UpdateLocale(ctx, inserted.ID, "ko", form.LocaleChange{SetTitle: true, Title: nil, UpdatedAt: at.Add(time.Second)})
			require.NoError(t, err)
			require.True(t, updated)
			missing, err := store.UpdateLocale(ctx, inserted.ID, "de", form.LocaleChange{SetTitle: true, UpdatedAt: at})
			require.NoError(t, err)
			require.False(t, missing)
			return nil
		})
		transact(t, h, func(ctx context.Context, store form.Store) error {
			after, exists, err := store.Locale(ctx, inserted.ID, "ko", false)
			require.NoError(t, err)
			require.True(t, exists)
			require.Nil(t, after.Title)
			require.JSONEq(t, contractSchema, string(after.Schema), "an unset schema stays")
			require.Equal(t, "문의", *after.ContentText)
			require.True(t, at.Add(time.Second).Equal(after.UpdatedAt))
			require.NotEqual(t, before.Revision, after.Revision)
			deleted, err := store.DeleteLocale(ctx, inserted.ID, "ko")
			require.NoError(t, err)
			require.True(t, deleted)
			deleted, err = store.DeleteLocale(ctx, inserted.ID, "ko")
			require.NoError(t, err)
			require.False(t, deleted)
			return nil
		})
	},
	"localization rows read the requested locales": func(t *testing.T, h Harness) {
		inserted := insert(t, h, nil)
		transact(t, h, func(ctx context.Context, store form.Store) error {
			_, err := store.CreateEmptyLocale(ctx, inserted.ID, "ko", []byte(contractSchema), stamp())
			return err
		})
		transact(t, h, func(ctx context.Context, store form.Store) error {
			rows, err := store.LocalizationRows(ctx, inserted.ID, []string{"ko"})
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, "ko", rows[0].Locale)
			all, err := store.LocalizationRows(ctx, inserted.ID, nil)
			require.NoError(t, err)
			require.Len(t, all, 2)
			return nil
		})
	},
	"source titles fall back to the default title": func(t *testing.T, h Harness) {
		titled := insert(t, h, nil)
		blank := insert(t, h, nil)
		transact(t, h, func(ctx context.Context, store form.Store) error {
			return store.SaveSourceLocale(ctx, blank.ID, "en", text("  "), []byte(contractSchema), nil, stamp())
		})
		missing := uuid.NewString()
		transact(t, h, func(ctx context.Context, store form.Store) error {
			titles, err := store.SourceTitles(ctx, []string{titled.ID, blank.ID, missing})
			require.NoError(t, err)
			require.Equal(t, map[string]string{titled.ID: "Contact", blank.ID: form.DefaultTitle, missing: form.DefaultTitle}, titles)
			return nil
		})
	},
	"the admin list filters by status and counts every match": func(t *testing.T, h Harness) {
		older := insert(t, h, func(value *form.Form) { value.Status = form.StatusPublished; value.CreatedAt = stamp().Add(-time.Hour) })
		newer := insert(t, h, func(value *form.Form) { value.Status = form.StatusPublished })
		insert(t, h, nil)
		transact(t, h, func(ctx context.Context, store form.Store) error {
			listed, total, err := store.ListForms(ctx, form.ListForms{
				Filters: []*commonv1.FilterSpec{{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: string(form.StatusPublished)}},
				Limit:   1,
			})
			require.NoError(t, err)
			require.Equal(t, int64(2), total)
			require.Len(t, listed, 1)
			require.Equal(t, newer.ID, listed[0].ID, "the newest Form comes first")
			listed, _, err = store.ListForms(ctx, form.ListForms{
				Filters: []*commonv1.FilterSpec{{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: string(form.StatusPublished)}},
				Limit:   10, Offset: 1,
			})
			require.NoError(t, err)
			require.Len(t, listed, 1)
			require.Equal(t, older.ID, listed[0].ID)
			return nil
		})
	},
	"submissions list by search, country and time, newest first with ties by ID": func(t *testing.T, h Harness) {
		inserted := insert(t, h, nil)
		at := stamp().Add(-time.Hour)
		first := submit(t, h, form.Submission{FormID: inserted.ID, Data: []byte(`{"name":"100% match"}`), CountryCode: text("KR"), CreatedAt: at})
		second := submit(t, h, form.Submission{FormID: inserted.ID, Data: []byte(`{"name":"100 match"}`), CountryCode: text("KR"), CreatedAt: at})
		third := submit(t, h, form.Submission{FormID: inserted.ID, Data: []byte(`{"name":"later"}`), CountryCode: text("US"), CreatedAt: at.Add(time.Minute)})
		transact(t, h, func(ctx context.Context, store form.Store) error {
			listed, total, err := store.ListSubmissions(ctx, form.SubmissionList{FormID: inserted.ID, Search: "100%", Limit: 10})
			require.NoError(t, err)
			require.Equal(t, int64(1), total, "the search matches %% literally")
			require.Equal(t, first.ID, listed[0].ID)
			listed, total, err = store.ListSubmissions(ctx, form.SubmissionList{FormID: inserted.ID, CountryCode: "KR", Limit: 10})
			require.NoError(t, err)
			require.Equal(t, int64(2), total)
			tieFirst, tieSecond := first.ID, second.ID
			if tieFirst < tieSecond {
				tieFirst, tieSecond = tieSecond, tieFirst
			}
			require.Equal(t, []string{tieFirst, tieSecond}, []string{listed[0].ID, listed[1].ID})
			before := at.Add(time.Minute)
			listed, total, err = store.ListSubmissions(ctx, form.SubmissionList{FormID: inserted.ID, CreatedAtBefore: &before, Order: []form.SubmissionOrder{{}}, Limit: 1})
			require.NoError(t, err)
			require.Equal(t, int64(2), total)
			require.Len(t, listed, 1)
			listed, _, err = store.ListSubmissions(ctx, form.SubmissionList{FormID: inserted.ID, CreatedAtFrom: &before, Limit: 10})
			require.NoError(t, err)
			require.Equal(t, third.ID, listed[0].ID)
			counts, err := store.SubmissionCounts(ctx, []string{inserted.ID})
			require.NoError(t, err)
			require.Equal(t, map[string]int64{inserted.ID: 3}, counts)
			data, err := store.SubmissionData(ctx, inserted.ID)
			require.NoError(t, err)
			require.Len(t, data, 3)
			require.True(t, data[0].CreatedAt.Equal(third.CreatedAt))
			found, err := store.FindSubmission(ctx, third.ID)
			require.NoError(t, err)
			require.Equal(t, third, found)
			deleted, err := store.DeleteSubmission(ctx, third.ID)
			require.NoError(t, err)
			require.True(t, deleted)
			deleted, err = store.DeleteSubmission(ctx, third.ID)
			require.NoError(t, err)
			require.False(t, deleted)
			return nil
		})
	},
	"a deleted form takes its locales and submissions": func(t *testing.T, h Harness) {
		inserted := insert(t, h, nil)
		submission := submit(t, h, form.Submission{FormID: inserted.ID, Data: []byte(`{}`), CreatedAt: stamp()})
		transact(t, h, func(ctx context.Context, store form.Store) error {
			return store.DeleteForm(ctx, inserted.ID)
		})
		transact(t, h, func(ctx context.Context, store form.Store) error {
			_, err := store.LocaleState(ctx, inserted.ID, "en")
			require.ErrorIs(t, err, form.ErrLocaleNotFound)
			_, err = store.FindSubmission(ctx, submission.ID)
			require.ErrorIs(t, err, form.ErrSubmissionNotFound)
			return nil
		})
	},
	"a failed transaction leaves no form": func(t *testing.T, h Harness) {
		failure := errors.New("rollback")
		var inserted form.Form
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store form.Store) error {
			inserted = form.Form{ContentDocumentID: h.SeedDocument(t), SourceLocale: "en", Status: form.StatusDraft, CreatedAt: stamp()}
			if err := store.InsertForm(ctx, &inserted); err != nil {
				return err
			}
			return failure
		})
		require.ErrorIs(t, err, failure)
		transact(t, h, func(ctx context.Context, store form.Store) error {
			exists, err := store.FormExists(ctx, inserted.ID)
			require.NoError(t, err)
			require.False(t, exists)
			return nil
		})
	},
	"a revision advance checks the expected revision and runs its effect once": func(t *testing.T, h Harness) {
		inserted := insert(t, h, nil)
		documentID := uuid.MustParse(inserted.ContentDocumentID)
		fence := func(context.Context, form.Store, uuid.UUID) (contentblock.DomainContext, error) {
			return contentblock.DomainContext{SourceLocale: "en"}, nil
		}
		transact(t, h, func(ctx context.Context, store form.Store) error {
			current, err := store.DocumentRevision(ctx, documentID)
			require.NoError(t, err)
			_, err = store.AdvanceContentRevision(ctx, contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: uuid.New()}, fence,
				func(context.Context, form.Store) (contentblock.MetadataEffect, error) {
					t.Fatal("a stale revision runs no effect")
					return contentblock.MetadataEffect{}, nil
				})
			var stale *contentblock.StaleRevisionError
			require.ErrorAs(t, err, &stale)
			require.Equal(t, current, stale.CurrentRevision)
			return nil
		})
		transact(t, h, func(ctx context.Context, store form.Store) error {
			current, err := store.DocumentRevision(ctx, documentID)
			require.NoError(t, err)
			advance, err := store.AdvanceContentRevision(ctx, contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: current}, fence,
				func(ctx context.Context, store form.Store) (contentblock.MetadataEffect, error) {
					return contentblock.MetadataEffect{Changed: true, AffectsTranslationSource: true, ChangedLocales: []string{"en"}},
						store.SaveSourceLocale(ctx, inserted.ID, "en", text("Advanced"), []byte(contractSchema), nil, stamp())
				})
			require.NoError(t, err)
			require.True(t, advance.Changed)
			require.NotEqual(t, current, advance.DocumentRevision)
			next, err := store.DocumentRevision(ctx, documentID)
			require.NoError(t, err)
			require.Equal(t, advance.DocumentRevision, next)
			state, err := store.LocaleState(ctx, inserted.ID, "en")
			require.NoError(t, err)
			require.Equal(t, "Advanced", *state.Title)
			return nil
		})
	},
	"a locked form makes a second writer wait for the first commit": func(t *testing.T, h Harness) {
		inserted := insert(t, h, nil)
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store form.Store) error {
				_, err := store.LockForm(ctx, inserted.ID)
				return err
			},
			func(ctx context.Context, store form.Store) error {
				next := inserted
				next.IsPublic = true
				return store.UpdateSettings(ctx, next, []form.Setting{form.SettingIsPublic}, stamp())
			},
			func(ctx context.Context, store form.Store) error {
				locked, err := store.LockFormRoot(ctx, inserted.ID)
				if err != nil {
					return err
				}
				require.Equal(t, inserted.ID, locked)
				current, err := store.LockForm(ctx, inserted.ID)
				if err != nil {
					return err
				}
				require.True(t, current.IsPublic, "the second writer sees the first commit")
				return nil
			},
		)
		require.NoError(t, err)
	},
	"a document root locked for update makes a second locker wait for its locale write": func(t *testing.T, h Harness) {
		inserted := insert(t, h, nil)
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store form.Store) error {
				_, err := store.DocumentRoot(ctx, inserted.ID, form.UpdateLock)
				if err != nil {
					return err
				}
				_, _, err = store.Locale(ctx, inserted.ID, "en", true)
				return err
			},
			func(ctx context.Context, store form.Store) error {
				_, err := store.UpdateLocale(ctx, inserted.ID, "en", form.LocaleChange{SetTitle: true, Title: text("Locked"), UpdatedAt: stamp()})
				return err
			},
			func(ctx context.Context, store form.Store) error {
				if _, err := store.LockFeaturedImage(ctx, inserted.ID); err != nil {
					return err
				}
				locale, _, err := store.Locale(ctx, inserted.ID, "en", true)
				if err != nil {
					return err
				}
				require.Equal(t, "Locked", *locale.Title)
				return nil
			},
		)
		require.NoError(t, err)
	},
}
