package pagetest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/page"
)

// Harness is one page.Store implementation the contract runs against, with
// the rows of other domains a case needs.
type Harness struct {
	mutationtest.Harness[page.Store]
	// SeedDocument stores an empty Page content document and returns its
	// ID.
	SeedDocument func(t *testing.T) string
	// SeedFile stores a File and returns its ID.
	SeedFile func(t *testing.T) string
}

// FakeHarness is the Harness of the fake Store.
func FakeHarness() Harness {
	store := NewStore()
	return Harness{
		Harness: mutationtest.FakeHarness[page.Store](store),
		SeedDocument: func(*testing.T) string {
			document := Document{ID: uuid.New(), Profile: "page", Revision: uuid.New()}
			store.mu.Lock()
			store.documents[document.ID] = document
			store.mu.Unlock()
			return document.ID.String()
		},
		SeedFile: func(*testing.T) string {
			id := uuid.NewString()
			store.PutFile(page.PublicationFile{ID: id, MIMEType: "image/webp"})
			return id
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

func transact(t *testing.T, h Harness, work func(ctx context.Context, store page.Store) error) {
	t.Helper()
	require.NoError(t, mutationtest.Transact(t, h.Harness, work))
}

func text(value string) *string { return &value }

// versionSnapshot is a version checkpoint envelope the page_version table
// accepts.
var versionSnapshot = json.RawMessage(`{"schemaVersion":1,"sourceLocale":"en","title":"v","summary":null,"document":{}}`)

// stamp is a time PostgreSQL stores exactly.
func stamp() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// insert stores a draft Page with its source locale in its own transaction.
func insert(t *testing.T, h Harness, change func(*page.Page)) page.Page {
	t.Helper()
	documentID := h.SeedDocument(t)
	at := stamp()
	value := page.Page{
		ContentDocumentID: &documentID, SourceLocale: "en", DocumentLayout: page.DefaultDocumentLayout(),
		AccessPolicy: json.RawMessage(`{"mode":"PAGE_ACCESS_MODE_PUBLIC"}`), Status: page.StatusDraft, ShowTitle: true,
		CreatedAt: at, UpdatedAt: at,
	}
	if change != nil {
		change(&value)
	}
	transact(t, h, func(ctx context.Context, store page.Store) error {
		if err := store.InsertPage(ctx, &value); err != nil {
			return err
		}
		return store.InsertLocale(ctx, value.ID, value.SourceLocale, text("Source title"), text("Source summary"), at)
	})
	return value
}

var storeContract = map[string]func(t *testing.T, h Harness){
	"a missing page answers its sentinels": func(t *testing.T, h Harness) {
		missing := uuid.NewString()
		transact(t, h, func(ctx context.Context, store page.Store) error {
			_, err := store.FindPage(ctx, page.ByID, missing, page.UpdateLock)
			require.ErrorIs(t, err, page.ErrPageNotFound)
			_, err = store.FindPage(ctx, page.BySlug, "missing-"+missing, page.ShareLock)
			require.ErrorIs(t, err, page.ErrPageNotFound)
			_, err = store.DocumentRoot(ctx, missing, page.UpdateLock)
			require.ErrorIs(t, err, page.ErrPageNotFound)
			_, err = store.LockSettings(ctx, missing)
			require.ErrorIs(t, err, page.ErrPageNotFound)
			_, err = store.AccessPolicy(ctx, missing)
			require.ErrorIs(t, err, page.ErrPageNotFound)
			_, err = store.DocumentLayout(ctx, missing)
			require.ErrorIs(t, err, page.ErrPageNotFound)
			_, err = store.FindVersion(ctx, missing)
			require.ErrorIs(t, err, page.ErrVersionNotFound)
			require.ErrorIs(t, store.FileExists(ctx, missing), page.ErrFileNotFound)
			_, exists, err := store.LockLatestVersion(ctx, missing)
			require.NoError(t, err)
			require.False(t, exists)
			_, exists, err = store.Locale(ctx, missing, "en", page.UpdateLock)
			require.NoError(t, err)
			require.False(t, exists)
			states, err := store.SourceMetadata(ctx, []string{missing})
			require.NoError(t, err)
			require.Empty(t, states)
			return nil
		})
	},
	"an inserted page reads back by ID and slug": func(t *testing.T, h Harness) {
		value := insert(t, h, func(value *page.Page) { value.Slug = text("about/" + uuid.NewString()) })
		require.NotEmpty(t, value.ID)
		transact(t, h, func(ctx context.Context, store page.Store) error {
			for _, found := range []func() (page.Page, error){
				func() (page.Page, error) { return store.FindPage(ctx, page.ByID, value.ID, page.ShareLock) },
				func() (page.Page, error) { return store.FindPage(ctx, page.BySlug, *value.Slug, page.Unlocked) },
			} {
				stored, err := found()
				require.NoError(t, err)
				require.Equal(t, value.ID, stored.ID)
				require.Equal(t, *value.Slug, *stored.Slug)
				require.Equal(t, page.StatusDraft, stored.Status)
				require.Equal(t, page.DefaultDocumentLayout(), stored.DocumentLayout)
				require.Equal(t, *value.ContentDocumentID, *stored.ContentDocumentID)
				require.True(t, value.CreatedAt.Equal(stored.CreatedAt))
			}
			root, err := store.DocumentRoot(ctx, value.ID, page.UpdateLock)
			require.NoError(t, err)
			require.Equal(t, *value.ContentDocumentID, root.ContentDocumentID.String())
			require.Equal(t, "en", root.SourceLocale)
			policy, err := store.AccessPolicy(ctx, value.ID)
			require.NoError(t, err)
			require.JSONEq(t, `{"mode":"PAGE_ACCESS_MODE_PUBLIC"}`, string(policy))
			settings, err := store.LockSettings(ctx, value.ID)
			require.NoError(t, err)
			require.True(t, settings.ShowTitle)
			require.Equal(t, *value.Slug, *settings.Slug)
			states, err := store.SourceMetadata(ctx, []string{value.ID, " " + value.ID})
			require.NoError(t, err)
			require.Equal(t, map[string]page.SourceMetadata{value.ID: {Locale: "en", Title: text("Source title"), Summary: text("Source summary")}}, states)
			return nil
		})
	},
	"a slug another page holds is taken": func(t *testing.T, h Harness) {
		slug := "taken-" + uuid.NewString()
		holder := insert(t, h, func(value *page.Page) { value.Slug = &slug })
		other := insert(t, h, nil)
		transact(t, h, func(ctx context.Context, store page.Store) error {
			taken, err := store.SlugTaken(ctx, slug, "")
			require.NoError(t, err)
			require.True(t, taken)
			taken, err = store.SlugTaken(ctx, slug, holder.ID)
			require.NoError(t, err)
			require.False(t, taken)
			return nil
		})
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store page.Store) error {
			documentID := h.SeedDocument(t)
			value := page.Page{ContentDocumentID: &documentID, SourceLocale: "en", DocumentLayout: page.DefaultDocumentLayout(),
				AccessPolicy: json.RawMessage(`{}`), Status: page.StatusDraft, Slug: &slug, CreatedAt: stamp(), UpdatedAt: stamp()}
			return store.InsertPage(ctx, &value)
		})
		require.ErrorIs(t, err, page.ErrSlugTaken)
		err = mutationtest.Transact(t, h.Harness, func(ctx context.Context, store page.Store) error {
			return store.UpdateSettings(ctx, other.ID, page.SettingsChange{SetSlug: true, Slug: &slug}, stamp())
		})
		require.ErrorIs(t, err, page.ErrSlugTaken)
	},
	"settings, status, featured image and layout updates are stored": func(t *testing.T, h Harness) {
		value := insert(t, h, func(value *page.Page) { value.Slug = text("settings-" + uuid.NewString()) })
		fileID := h.SeedFile(t)
		at := stamp().Add(time.Second)
		hidden := false
		layout := page.DocumentLayout{ContentHeight: page.ContentHeightViewport, PageChrome: page.PlacementPinned, Footer: page.PlacementFlow, HideTableOfContents: true}
		transact(t, h, func(ctx context.Context, store page.Store) error {
			require.NoError(t, store.UpdateSettings(ctx, value.ID, page.SettingsChange{
				SetSlug: true, ShowTitle: &hidden, AccessPolicy: json.RawMessage(`{"mode":"PAGE_ACCESS_MODE_AUTHENTICATED"}`),
			}, at))
			require.NoError(t, store.SetStatus(ctx, value.ID, page.StatusPublished, &at, at))
			require.NoError(t, store.FileExists(ctx, fileID))
			require.NoError(t, store.SetFeaturedImage(ctx, value.ID, &fileID, at))
			require.NoError(t, store.SetDocumentLayout(ctx, value.ID, layout, at))
			return nil
		})
		transact(t, h, func(ctx context.Context, store page.Store) error {
			stored, err := store.FindPage(ctx, page.ByID, value.ID, page.Unlocked)
			require.NoError(t, err)
			require.Nil(t, stored.Slug)
			require.False(t, stored.ShowTitle)
			require.Equal(t, page.StatusPublished, stored.Status)
			require.True(t, at.Equal(*stored.PublishedAt))
			require.True(t, at.Equal(stored.UpdatedAt))
			require.Equal(t, fileID, *stored.FeaturedImageFileID)
			require.Equal(t, layout, stored.DocumentLayout)
			require.JSONEq(t, `{"mode":"PAGE_ACCESS_MODE_AUTHENTICATED"}`, string(stored.AccessPolicy))
			loaded, err := store.DocumentLayout(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, layout, loaded)
			require.NoError(t, store.SetFeaturedImage(ctx, value.ID, nil, at))
			require.NoError(t, store.SetStatus(ctx, value.ID, page.StatusDraft, nil, at.Add(time.Second)))
			touched := at.Add(2 * time.Second)
			require.NoError(t, store.TouchPage(ctx, value.ID, touched))
			stored, err = store.FindPage(ctx, page.ByID, value.ID, page.Unlocked)
			require.NoError(t, err)
			require.Nil(t, stored.FeaturedImageFileID)
			require.True(t, at.Equal(*stored.PublishedAt), "unpublishing keeps the first publication")
			require.True(t, touched.Equal(stored.UpdatedAt))
			return nil
		})
	},
	"the source locale switches only from the current one": func(t *testing.T, h Harness) {
		value := insert(t, h, nil)
		transact(t, h, func(ctx context.Context, store page.Store) error {
			switched, err := store.SwitchSourceLocale(ctx, value.ID, "ko", "ja", stamp())
			require.NoError(t, err)
			require.False(t, switched)
			switched, err = store.SwitchSourceLocale(ctx, value.ID, "en", "ko", stamp())
			require.NoError(t, err)
			require.True(t, switched)
			root, err := store.DocumentRoot(ctx, value.ID, page.Unlocked)
			require.NoError(t, err)
			require.Equal(t, "ko", root.SourceLocale)
			return nil
		})
	},
	"locales are created, updated and deleted with their incarnation": func(t *testing.T, h Harness) {
		value := insert(t, h, nil)
		at := stamp()
		transact(t, h, func(ctx context.Context, store page.Store) error {
			incarnation, err := store.CreateTargetLocale(ctx, value.ID, "ko", nil, text("요약"), at)
			require.NoError(t, err)
			require.NotEqual(t, uuid.Nil, incarnation)
			locale, exists, err := store.Locale(ctx, value.ID, "ko", page.UpdateLock)
			require.NoError(t, err)
			require.True(t, exists)
			require.Equal(t, "ko", locale.Locale)
			require.Nil(t, locale.Title)
			require.Equal(t, "요약", *locale.Summary)
			require.Equal(t, incarnation, locale.IncarnationID)
			require.True(t, at.Equal(locale.UpdatedAt))

			later := at.Add(time.Second)
			exists, err = store.UpdateLocale(ctx, value.ID, "ko", page.LocaleChange{SetTitle: true, Title: text("제목"), UpdatedAt: later})
			require.NoError(t, err)
			require.True(t, exists)
			locale, _, err = store.Locale(ctx, value.ID, "ko", page.Unlocked)
			require.NoError(t, err)
			require.Equal(t, "제목", *locale.Title)
			require.Equal(t, "요약", *locale.Summary, "an update writes only the columns it sets")
			require.True(t, later.Equal(locale.UpdatedAt))
			require.Equal(t, incarnation, locale.IncarnationID)

			require.NoError(t, store.UpsertLocale(ctx, value.ID, "ko", text("새 제목"), nil, later))
			require.NoError(t, store.UpsertLocale(ctx, value.ID, "ja", text("題名"), nil, later))
			locale, _, err = store.Locale(ctx, value.ID, "ko", page.Unlocked)
			require.NoError(t, err)
			require.Equal(t, "새 제목", *locale.Title)
			require.Nil(t, locale.Summary)
			require.Equal(t, incarnation, locale.IncarnationID, "an upsert keeps the locale's incarnation")

			deleted, err := store.DeleteLocale(ctx, value.ID, "ko")
			require.NoError(t, err)
			require.True(t, deleted)
			deleted, err = store.DeleteLocale(ctx, value.ID, "ko")
			require.NoError(t, err)
			require.False(t, deleted)
			exists, err = store.UpdateLocale(ctx, value.ID, "ko", page.LocaleChange{UpdatedAt: later})
			require.NoError(t, err)
			require.False(t, exists)
			_, exists, err = store.Locale(ctx, value.ID, "ja", page.Unlocked)
			require.NoError(t, err)
			require.True(t, exists)
			return nil
		})
	},
	"versions list newest first and count every version": func(t *testing.T, h Harness) {
		value := insert(t, h, nil)
		other := insert(t, h, nil)
		member := uuid.NewString()
		snapshot := versionSnapshot
		transact(t, h, func(ctx context.Context, store page.Store) error {
			for number := int32(1); number <= 3; number++ {
				version := page.Version{PageID: value.ID, Version: number, Title: text("v"), ContentSnapshot: snapshot, ContributorMemberIDs: []string{member}, CreatedAt: stamp()}
				require.NoError(t, store.InsertVersion(ctx, &version))
				require.NotEmpty(t, version.ID)
			}
			version := page.Version{PageID: other.ID, Version: 9, ContentSnapshot: snapshot, ContributorMemberIDs: []string{}, CreatedAt: stamp()}
			return store.InsertVersion(ctx, &version)
		})
		transact(t, h, func(ctx context.Context, store page.Store) error {
			versions, total, err := store.ListVersions(ctx, value.ID, 2, 1)
			require.NoError(t, err)
			require.Equal(t, int64(3), total)
			require.Len(t, versions, 2)
			require.Equal(t, []int32{2, 1}, []int32{versions[0].Version, versions[1].Version})
			require.Equal(t, []string{member}, versions[0].ContributorMemberIDs)
			found, err := store.FindVersion(ctx, versions[0].ID)
			require.NoError(t, err)
			require.Equal(t, value.ID, found.PageID)
			require.JSONEq(t, string(snapshot), string(found.ContentSnapshot))
			latest, exists, err := store.LockLatestVersion(ctx, value.ID)
			require.NoError(t, err)
			require.True(t, exists)
			require.Equal(t, int32(3), latest.Version)
			return nil
		})
	},
	"pages list newest first with their total": func(t *testing.T, h Harness) {
		base := stamp().Add(-time.Hour)
		var inserted []page.Page
		for offset := range 3 {
			inserted = append(inserted, insert(t, h, func(value *page.Page) {
				value.CreatedAt = base.Add(time.Duration(offset) * time.Minute)
				value.UpdatedAt = value.CreatedAt
			}))
		}
		transact(t, h, func(ctx context.Context, store page.Store) error {
			pages, total, err := store.ListPages(ctx, page.ListPage{Limit: 2, Offset: 0})
			require.NoError(t, err)
			require.Equal(t, int64(3), total)
			require.Equal(t, []string{inserted[2].ID, inserted[1].ID}, []string{pages[0].ID, pages[1].ID})
			pages, _, err = store.ListPages(ctx, page.ListPage{Limit: 2, Offset: 2})
			require.NoError(t, err)
			require.Equal(t, []string{inserted[0].ID}, []string{pages[0].ID})
			return nil
		})
	},
	"a deleted page takes its locales with it": func(t *testing.T, h Harness) {
		value := insert(t, h, nil)
		transact(t, h, func(ctx context.Context, store page.Store) error { return store.DeletePage(ctx, value.ID) })
		transact(t, h, func(ctx context.Context, store page.Store) error {
			_, err := store.FindPage(ctx, page.ByID, value.ID, page.Unlocked)
			require.ErrorIs(t, err, page.ErrPageNotFound)
			_, exists, err := store.Locale(ctx, value.ID, "en", page.Unlocked)
			require.NoError(t, err)
			require.False(t, exists)
			return nil
		})
	},
	"a failed transaction stores nothing": func(t *testing.T, h Harness) {
		value := insert(t, h, nil)
		failed := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store page.Store) error {
			require.NoError(t, store.SetStatus(ctx, value.ID, page.StatusPublished, nil, stamp()))
			require.NoError(t, store.UpsertLocale(ctx, value.ID, "ko", text("롤백"), nil, stamp()))
			return context.Canceled
		})
		require.ErrorIs(t, failed, context.Canceled)
		transact(t, h, func(ctx context.Context, store page.Store) error {
			stored, err := store.FindPage(ctx, page.ByID, value.ID, page.Unlocked)
			require.NoError(t, err)
			require.Equal(t, page.StatusDraft, stored.Status)
			_, exists, err := store.Locale(ctx, value.ID, "ko", page.Unlocked)
			require.NoError(t, err)
			require.False(t, exists)
			return nil
		})
	},
	"a locked page makes a second writer wait for the first commit": func(t *testing.T, h Harness) {
		value := insert(t, h, nil)
		var seen page.Status
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store page.Store) error {
				_, err := store.LockSettings(ctx, value.ID)
				return err
			},
			func(ctx context.Context, store page.Store) error {
				return store.SetStatus(ctx, value.ID, page.StatusPublished, nil, stamp())
			},
			func(ctx context.Context, store page.Store) error {
				stored, err := store.FindPage(ctx, page.ByID, value.ID, page.UpdateLock)
				seen = stored.Status
				return err
			},
		)
		require.NoError(t, err)
		require.Equal(t, page.StatusPublished, seen)
	},
	"a locked document root makes a second root lock wait": func(t *testing.T, h Harness) {
		value := insert(t, h, nil)
		var seen string
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store page.Store) error {
				_, err := store.DocumentRoot(ctx, value.ID, page.UpdateLock)
				return err
			},
			func(ctx context.Context, store page.Store) error {
				_, err := store.SwitchSourceLocale(ctx, value.ID, "en", "ko", stamp())
				return err
			},
			func(ctx context.Context, store page.Store) error {
				root, err := store.DocumentRoot(ctx, value.ID, page.UpdateLock)
				seen = root.SourceLocale
				return err
			},
		)
		require.NoError(t, err)
		require.Equal(t, "ko", seen)
	},
	"a locked latest version makes a second checkpoint wait": func(t *testing.T, h Harness) {
		value := insert(t, h, nil)
		transact(t, h, func(ctx context.Context, store page.Store) error {
			version := page.Version{PageID: value.ID, Version: 1, ContentSnapshot: versionSnapshot, ContributorMemberIDs: []string{}, CreatedAt: stamp()}
			return store.InsertVersion(ctx, &version)
		})
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store page.Store) error {
				_, _, err := store.LockLatestVersion(ctx, value.ID)
				return err
			},
			func(context.Context, page.Store) error { return nil },
			func(ctx context.Context, store page.Store) error {
				_, exists, err := store.LockLatestVersion(ctx, value.ID)
				require.True(t, exists)
				return err
			},
		)
		require.NoError(t, err)
	},
}
