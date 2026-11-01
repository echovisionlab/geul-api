package seriestest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/series"
)

// Fixture is one Store implementation under the contract and the rows of
// other domains its cases need.
type Fixture struct {
	mutationtest.Harness[series.Store]
	// Member returns a Member a manager attribution may name.
	Member func(t *testing.T) string
	// File returns a File a Series may point its featured image at.
	File func(t *testing.T) string
}

// FakeFixture is the Fixture of the in-memory store.
func FakeFixture(*testing.T) Fixture {
	store, _ := New()
	return Fixture{
		Harness: mutationtest.FakeHarness[series.Store](store),
		Member:  func(*testing.T) string { return uuid.NewString() },
		File:    func(*testing.T) string { return uuid.NewString() },
	}
}

// RunStoreContract runs every Store contract case against a fresh Fixture.
func RunStoreContract(t *testing.T, open func(t *testing.T) Fixture) {
	t.Helper()
	// RunContract opens each case's Harness in the case's own test, so the
	// case finds its Fixture by that test.
	var fixtures sync.Map
	cases := make(map[string]func(*testing.T, mutationtest.Harness[series.Store]), len(storeContract))
	for name, run := range storeContract {
		cases[name] = func(t *testing.T, _ mutationtest.Harness[series.Store]) {
			fixture, _ := fixtures.Load(t)
			run(t, fixture.(Fixture))
		}
	}
	mutationtest.RunContract(t, func(t *testing.T) mutationtest.Harness[series.Store] {
		fixture := open(t)
		fixtures.Store(t, fixture)
		return fixture.Harness
	}, cases)
}

var contractTime = time.Date(2026, 3, 4, 5, 6, 7, 123456000, time.UTC)

// seed creates a Series through the store with its compact content document
// and its source copy, and returns it as stored.
func seed(t *testing.T, f Fixture, slug, title string) series.Series {
	t.Helper()
	var created series.Series
	require.NoError(t, mutationtest.Transact(t, f.Harness, func(ctx context.Context, store series.Store) error {
		var err error
		if created, err = insert(ctx, store, slug, contractTime); err != nil {
			return err
		}
		return store.UpsertTranslation(ctx, created.ID, "en", series.TranslationWrite{Title: &title, Now: contractTime})
	}))
	return created
}

// insert creates a draft Series root and the compact content document it
// must own when its transaction commits.
func insert(ctx context.Context, store series.Store, slug string, createdAt time.Time) (series.Series, error) {
	documentID := uuid.NewString()
	if err := store.CreateContentDocument(ctx, documentID, uuid.NewString(), createdAt); err != nil {
		return series.Series{}, err
	}
	return store.InsertSeries(ctx, series.Series{
		ContentDocumentID: documentID, Slug: slug, Status: series.StatusDraft, SourceLocale: "en",
		CreatedAt: createdAt, UpdatedAt: &createdAt,
	})
}

func transact(t *testing.T, f Fixture, work func(ctx context.Context, store series.Store) error) {
	t.Helper()
	require.NoError(t, mutationtest.Transact(t, f.Harness, work))
}

func revisionOf(t *testing.T, f Fixture, seriesID string) (documentID, revision string) {
	t.Helper()
	transact(t, f, func(ctx context.Context, store series.Store) error {
		root, err := store.ContentDocumentRoot(ctx, seriesID, false)
		if err != nil {
			return err
		}
		document, err := store.ContentDocument(ctx, *root.ContentDocumentID, false)
		if err != nil {
			return err
		}
		documentID, revision = *root.ContentDocumentID, *document.Revision
		return nil
	})
	return documentID, revision
}

func text(value string) *string { return &value }

var errContractRollback = errors.New("contract rollback")

var storeContract = map[string]func(t *testing.T, f Fixture){
	"a created Series reads back with its source copy and content document": func(t *testing.T, f Fixture) {
		created := seed(t, f, "created-"+uuid.NewString(), "Created")
		require.NotEmpty(t, created.ID)
		transact(t, f, func(ctx context.Context, store series.Store) error {
			root, err := store.Series(ctx, created.ID)
			require.NoError(t, err)
			require.Equal(t, created.Slug, root.Slug)
			require.Equal(t, series.StatusDraft, root.Status)
			require.Equal(t, "en", root.SourceLocale)
			require.Equal(t, contractTime, root.CreatedAt.UTC())
			require.Empty(t, root.Title, "the root carries no copy until it is overlaid")
			summary := "Summary"
			require.NoError(t, store.UpsertTranslation(ctx, created.ID, "en", series.TranslationWrite{Title: text("Retitled"), Summary: &summary, ContentText: &summary, Now: contractTime}))
			copies, err := store.SourceCopies(ctx, []string{created.ID, uuid.NewString()})
			require.NoError(t, err)
			require.Equal(t, map[string]series.SourceCopy{created.ID: {Title: text("Retitled"), Summary: &summary, ContentText: &summary}}, copies)
			document, err := store.ContentDocumentRoot(ctx, created.ID, true)
			require.NoError(t, err)
			require.Equal(t, created.ContentDocumentID, *document.ContentDocumentID)
			row, err := store.ContentDocument(ctx, created.ContentDocumentID, true)
			require.NoError(t, err)
			require.Equal(t, "compact", row.Profile)
			require.NotNil(t, row.Revision)
			return nil
		})
	},
	"a source copy belongs to the source locale only": func(t *testing.T, f Fixture) {
		created := seed(t, f, "source-"+uuid.NewString(), "English")
		transact(t, f, func(ctx context.Context, store series.Store) error {
			require.NoError(t, store.UpsertTranslation(ctx, created.ID, "ko", series.TranslationWrite{Title: text("한국어"), Now: contractTime}))
			copies, err := store.SourceCopies(ctx, []string{created.ID})
			require.NoError(t, err)
			require.Equal(t, text("English"), copies[created.ID].Title)
			require.NoError(t, store.SetSourceLocale(ctx, created.ID, "ko", contractTime))
			copies, err = store.SourceCopies(ctx, []string{created.ID})
			require.NoError(t, err)
			require.Equal(t, text("한국어"), copies[created.ID].Title)
			return nil
		})
	},
	"a missing Series answers ErrSeriesNotFound": func(t *testing.T, f Fixture) {
		missing := uuid.NewString()
		transact(t, f, func(ctx context.Context, store series.Store) error {
			_, err := store.Series(ctx, missing)
			require.ErrorIs(t, err, series.ErrSeriesNotFound)
			require.ErrorIs(t, store.LockSeries(ctx, missing), series.ErrSeriesNotFound)
			_, err = store.ContentDocumentRoot(ctx, missing, true)
			require.ErrorIs(t, err, series.ErrSeriesNotFound)
			_, err = store.ContentDocument(ctx, uuid.NewString(), false)
			require.ErrorIs(t, err, series.ErrContentDocumentNotFound)
			require.ErrorIs(t, store.DeleteContentDocument(ctx, uuid.NewString()), series.ErrContentDocumentNotFound)
			return nil
		})
	},
	"a taken slug answers ErrSlugTaken on insert and update": func(t *testing.T, f Fixture) {
		slug := "taken-" + uuid.NewString()
		first := seed(t, f, slug, "First")
		second := seed(t, f, "other-"+uuid.NewString(), "Second")
		err := mutationtest.Transact(t, f.Harness, func(ctx context.Context, store series.Store) error {
			_, err := insert(ctx, store, slug, contractTime)
			return err
		})
		require.ErrorIs(t, err, series.ErrSlugTaken)
		err = mutationtest.Transact(t, f.Harness, func(ctx context.Context, store series.Store) error {
			return store.UpdateSeriesRoot(ctx, second.ID, &slug, nil, contractTime)
		})
		require.ErrorIs(t, err, series.ErrSlugTaken)
		transact(t, f, func(ctx context.Context, store series.Store) error {
			taken, err := store.SlugTaken(ctx, slug, "")
			require.NoError(t, err)
			require.True(t, taken)
			taken, err = store.SlugTaken(ctx, slug, first.ID)
			require.NoError(t, err)
			require.False(t, taken, "a Series does not collide with its own slug")
			return nil
		})
	},
	"a root update writes only the named fields": func(t *testing.T, f Fixture) {
		created := seed(t, f, "update-"+uuid.NewString(), "Update")
		later := contractTime.Add(time.Hour)
		published := series.StatusPublished
		fileID := f.File(t)
		transact(t, f, func(ctx context.Context, store series.Store) error {
			require.NoError(t, store.UpdateSeriesRoot(ctx, created.ID, nil, &published, later))
			root, err := store.Series(ctx, created.ID)
			require.NoError(t, err)
			require.Equal(t, created.Slug, root.Slug)
			require.Equal(t, published, root.Status)
			require.Equal(t, later, root.UpdatedAt.UTC())
			require.NoError(t, store.SetFeaturedImage(ctx, created.ID, &fileID, later))
			root, err = store.Series(ctx, created.ID)
			require.NoError(t, err)
			require.Equal(t, &fileID, root.FeaturedImageFileID)
			require.NoError(t, store.SetFeaturedImage(ctx, created.ID, nil, later))
			root, err = store.Series(ctx, created.ID)
			require.NoError(t, err)
			require.Nil(t, root.FeaturedImageFileID)
			return nil
		})
	},
	"advancing the content document compares and sets its revision": func(t *testing.T, f Fixture) {
		created := seed(t, f, "advance-"+uuid.NewString(), "Advance")
		other := seed(t, f, "advance-other-"+uuid.NewString(), "Other")
		documentID, revision := revisionOf(t, f, created.ID)
		next := uuid.NewString()
		transact(t, f, func(ctx context.Context, store series.Store) error {
			require.ErrorIs(t, store.AdvanceContentDocument(ctx, created.ID, documentID, uuid.NewString(), next, contractTime), series.ErrContentDocumentRevisionChanged)
			require.ErrorIs(t, store.AdvanceContentDocument(ctx, other.ID, documentID, revision, next, contractTime), series.ErrContentDocumentRevisionChanged,
				"the document must belong to the Series")
			require.NoError(t, store.AdvanceContentDocument(ctx, created.ID, documentID, revision, next, contractTime))
			require.ErrorIs(t, store.AdvanceContentDocument(ctx, created.ID, documentID, revision, uuid.NewString(), contractTime), series.ErrContentDocumentRevisionChanged)
			return nil
		})
		_, stored := revisionOf(t, f, created.ID)
		require.Equal(t, next, stored)
	},
	"locale rows insert, update and delete by Series and locale": func(t *testing.T, f Fixture) {
		created := seed(t, f, "locale-"+uuid.NewString(), "Locale")
		updatedAt := contractTime.Add(time.Minute)
		transact(t, f, func(ctx context.Context, store series.Store) error {
			_, err := store.Translation(ctx, created.ID, "ko", true)
			require.ErrorIs(t, err, series.ErrTranslationNotFound)
			require.ErrorIs(t, store.UpdateTranslation(ctx, created.ID, "ko", text("x"), nil, nil, updatedAt), series.ErrTranslationNotFound)
			require.ErrorIs(t, store.DeleteTranslation(ctx, created.ID, "ko"), series.ErrTranslationNotFound)
			require.NoError(t, store.InsertTranslation(ctx, series.Translation{SeriesID: created.ID, Locale: "ko", Title: text(""), CreatedAt: contractTime, UpdatedAt: contractTime}))
			inserted, err := store.Translation(ctx, created.ID, "ko", false)
			require.NoError(t, err)
			require.Equal(t, text(""), inserted.Title, "an explicit empty title stays empty, not absent")
			require.Nil(t, inserted.Summary)
			require.Equal(t, contractTime, inserted.UpdatedAt.UTC())
			require.NoError(t, store.UpdateTranslation(ctx, created.ID, "ko", text("제목"), text("요약"), text("요약"), updatedAt))
			updated, err := store.Translation(ctx, created.ID, "ko", true)
			require.NoError(t, err)
			require.Equal(t, text("제목"), updated.Title)
			require.Equal(t, text("요약"), updated.ContentText)
			require.Equal(t, updatedAt, updated.UpdatedAt.UTC())
			require.NoError(t, store.DeleteTranslation(ctx, created.ID, "ko"))
			_, err = store.Translation(ctx, created.ID, "ko", false)
			require.ErrorIs(t, err, series.ErrTranslationNotFound)
			return nil
		})
	},
	"an upsert replaces the whole locale copy": func(t *testing.T, f Fixture) {
		created := seed(t, f, "upsert-"+uuid.NewString(), "Upsert")
		html := "<p>Body</p>"
		transact(t, f, func(ctx context.Context, store series.Store) error {
			require.NoError(t, store.UpsertTranslation(ctx, created.ID, "ko", series.TranslationWrite{
				Title: text("첫"), Summary: text("요약"), ContentJSON: []byte(`{"a":1}`), ContentHTML: &html, ContentText: text("본문"), Now: contractTime,
			}))
			require.NoError(t, store.UpsertTranslation(ctx, created.ID, "ko", series.TranslationWrite{Title: text("둘"), Now: contractTime.Add(time.Second)}))
			replaced, err := store.Translation(ctx, created.ID, "ko", false)
			require.NoError(t, err)
			require.Equal(t, text("둘"), replaced.Title)
			require.Nil(t, replaced.Summary)
			require.Nil(t, replaced.ContentHTML)
			require.Nil(t, replaced.ContentText)
			require.Empty(t, replaced.ContentJSON)
			require.Equal(t, contractTime.Add(time.Second), replaced.UpdatedAt.UTC())
			return nil
		})
	},
	"deleting a Series deletes its locale rows and manager attributions": func(t *testing.T, f Fixture) {
		created := seed(t, f, "delete-"+uuid.NewString(), "Delete")
		memberID := f.Member(t)
		transact(t, f, func(ctx context.Context, store series.Store) error {
			require.NoError(t, store.UpsertManagerAttribution(ctx, created.ID, memberID, contractTime))
			require.NoError(t, store.DeleteSeries(ctx, created.ID))
			require.NoError(t, store.DeleteContentDocument(ctx, created.ContentDocumentID))
			_, err := store.Translation(ctx, created.ID, "en", false)
			require.ErrorIs(t, err, series.ErrTranslationNotFound)
			attributed, err := store.ManagerAttributed(ctx, created.ID, memberID)
			require.NoError(t, err)
			require.False(t, attributed)
			_, err = store.Series(ctx, created.ID)
			require.ErrorIs(t, err, series.ErrSeriesNotFound)
			return nil
		})
	},
	"manager attributions keep their first creation and list oldest first": func(t *testing.T, f Fixture) {
		created := seed(t, f, "managers-"+uuid.NewString(), "Managers")
		first, second := f.Member(t), f.Member(t)
		transact(t, f, func(ctx context.Context, store series.Store) error {
			require.NoError(t, store.UpsertManagerAttribution(ctx, created.ID, second, contractTime.Add(time.Hour)))
			require.NoError(t, store.UpsertManagerAttribution(ctx, created.ID, first, contractTime))
			require.NoError(t, store.UpsertManagerAttribution(ctx, created.ID, first, contractTime.Add(2*time.Hour)))
			attributions, err := store.ManagerAttributions(ctx, created.ID)
			require.NoError(t, err)
			require.Len(t, attributions, 2)
			require.Equal(t, first, attributions[0].MemberID)
			require.Equal(t, contractTime, attributions[0].CreatedAt.UTC())
			require.Equal(t, second, attributions[1].MemberID)
			require.NoError(t, store.DeleteManagerAttribution(ctx, created.ID, first))
			attributed, err := store.ManagerAttributed(ctx, created.ID, first)
			require.NoError(t, err)
			require.False(t, attributed)
			attributed, err = store.ManagerAttributed(ctx, created.ID, second)
			require.NoError(t, err)
			require.True(t, attributed)
			return nil
		})
	},
	"a page of Series lists the newest first and counts every match": func(t *testing.T, f Fixture) {
		var ids []string
		for index := range 3 {
			created := seed(t, f, fmt.Sprintf("page-%d-%s", index, uuid.NewString()), fmt.Sprintf("Page %d", index))
			ids = append(ids, created.ID)
		}
		transact(t, f, func(ctx context.Context, store series.Store) error {
			listed, total, err := store.SeriesPage(ctx, ids, 2, 0)
			require.NoError(t, err)
			require.EqualValues(t, 3, total)
			require.Len(t, listed, 2)
			require.Less(t, listed[0].ID, listed[1].ID, "equal creation times break ties by ID")
			rest, _, err := store.SeriesPage(ctx, ids, 2, 2)
			require.NoError(t, err)
			require.Len(t, rest, 1)
			return nil
		})
	},
	"the picker lists every Series it is given, oldest first and unpaged": func(t *testing.T, f Fixture) {
		const count = 105
		ids := make([]string, 0, count)
		transact(t, f, func(ctx context.Context, store series.Store) error {
			for index := range count {
				createdAt := contractTime.Add(time.Duration(count-index) * time.Second)
				created, err := insert(ctx, store, fmt.Sprintf("picker-%03d-%s", index, uuid.NewString()), createdAt)
				if err != nil {
					return err
				}
				ids = append(ids, created.ID)
			}
			return nil
		})
		transact(t, f, func(ctx context.Context, store series.Store) error {
			listed, err := store.SeriesByCreation(ctx, ids)
			require.NoError(t, err)
			require.Len(t, listed, count)
			require.Equal(t, ids[count-1], listed[0].ID)
			require.Equal(t, ids[0], listed[count-1].ID)
			return nil
		})
	},
	"a failed transaction leaves nothing behind": func(t *testing.T, f Fixture) {
		slug := "rolled-back-" + uuid.NewString()
		var id string
		err := mutationtest.Transact(t, f.Harness, func(ctx context.Context, store series.Store) error {
			created, err := insert(ctx, store, slug, contractTime)
			id = created.ID
			if err != nil {
				return err
			}
			return errContractRollback
		})
		require.ErrorIs(t, err, errContractRollback)
		transact(t, f, func(ctx context.Context, store series.Store) error {
			_, err := store.Series(ctx, id)
			require.ErrorIs(t, err, series.ErrSeriesNotFound)
			taken, err := store.SlugTaken(ctx, slug, "")
			require.NoError(t, err)
			require.False(t, taken)
			return nil
		})
	},
	"a locked Series makes a second writer wait and read the first commit": func(t *testing.T, f Fixture) {
		created := seed(t, f, "lock-"+uuid.NewString(), "Lock")
		renamed := "renamed-" + uuid.NewString()
		var seen string
		err := mutationtest.RequireSecondWaits(t, f.Harness,
			func(ctx context.Context, store series.Store) error { return store.LockSeries(ctx, created.ID) },
			func(ctx context.Context, store series.Store) error {
				return store.UpdateSeriesRoot(ctx, created.ID, &renamed, nil, contractTime)
			},
			func(ctx context.Context, store series.Store) error {
				if err := store.LockSeries(ctx, created.ID); err != nil {
					return err
				}
				root, err := store.Series(ctx, created.ID)
				seen = root.Slug
				return err
			})
		require.NoError(t, err)
		require.Equal(t, renamed, seen)
	},
	"a locked content document root makes a second writer wait for the new revision": func(t *testing.T, f Fixture) {
		created := seed(t, f, "document-lock-"+uuid.NewString(), "Document lock")
		documentID, revision := revisionOf(t, f, created.ID)
		next := uuid.NewString()
		var seen string
		err := mutationtest.RequireSecondWaits(t, f.Harness,
			func(ctx context.Context, store series.Store) error {
				_, err := store.ContentDocumentRoot(ctx, created.ID, true)
				return err
			},
			func(ctx context.Context, store series.Store) error {
				return store.AdvanceContentDocument(ctx, created.ID, documentID, revision, next, contractTime)
			},
			func(ctx context.Context, store series.Store) error {
				if _, err := store.ContentDocumentRoot(ctx, created.ID, true); err != nil {
					return err
				}
				document, err := store.ContentDocument(ctx, documentID, true)
				if document.Revision != nil {
					seen = *document.Revision
				}
				return err
			})
		require.NoError(t, err)
		require.Equal(t, next, seen)
	},
	"a locked locale row makes a second writer wait for its update": func(t *testing.T, f Fixture) {
		created := seed(t, f, "locale-lock-"+uuid.NewString(), "Locale lock")
		var seen *string
		err := mutationtest.RequireSecondWaits(t, f.Harness,
			func(ctx context.Context, store series.Store) error {
				_, err := store.Translation(ctx, created.ID, "en", true)
				return err
			},
			func(ctx context.Context, store series.Store) error {
				return store.UpdateTranslation(ctx, created.ID, "en", text("Updated"), nil, nil, contractTime)
			},
			func(ctx context.Context, store series.Store) error {
				translation, err := store.Translation(ctx, created.ID, "en", true)
				seen = translation.Title
				return err
			})
		require.NoError(t, err)
		require.Equal(t, text("Updated"), seen)
	},
}
