package posttest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/post"
)

// Harness is one post.Store implementation the contract runs against, with
// the rows of other domains a case needs.
type Harness struct {
	mutationtest.Harness[post.Store]
	// SeedDocument stores an empty Post content document and returns its
	// ID.
	SeedDocument func(t *testing.T) string
	// SeedMember stores a Member without an Identity and returns its ID.
	SeedMember func(t *testing.T, nickname string) string
	// SeedTaxonomy stores a Category or a Tag and returns its ID.
	SeedTaxonomy func(t *testing.T, taxonomy post.Taxonomy) string
	// SeedFile stores a File and returns its ID.
	SeedFile func(t *testing.T) string
	// SeedMapPlace stores a Map Place and returns its ID.
	SeedMapPlace func(t *testing.T) string
}

// FakeHarness is the Harness of the fake Store.
func FakeHarness() Harness {
	store := NewStore()
	return Harness{
		Harness: mutationtest.FakeHarness[post.Store](store),
		SeedDocument: func(*testing.T) string {
			id := uuid.New()
			store.putDocument(Document{ID: id, Profile: "post", Revision: uuid.New()})
			return id.String()
		},
		SeedMember: func(_ *testing.T, nickname string) string {
			id := uuid.NewString()
			store.PutMember(id, Member{Nickname: nickname})
			return id
		},
		SeedTaxonomy: func(_ *testing.T, taxonomy post.Taxonomy) string {
			id := uuid.NewString()
			store.PutTaxonomy(taxonomy, id)
			return id
		},
		SeedFile: func(*testing.T) string {
			id := uuid.NewString()
			store.PutFile(id)
			return id
		},
		SeedMapPlace: func(*testing.T) string {
			id := uuid.NewString()
			store.PutMapPlace(id)
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

func transact(t *testing.T, h Harness, work func(ctx context.Context, store post.Store) error) {
	t.Helper()
	require.NoError(t, mutationtest.Transact(t, h.Harness, work))
}

// insert stores a draft Post with a source locale row titled title, in its
// own transaction.
func insert(t *testing.T, h Harness, title string, change func(*post.Post)) post.Post {
	t.Helper()
	documentID := h.SeedDocument(t)
	value := post.Post{SourceLocale: "en", Status: post.StatusDraft, ContentDocumentID: &documentID, DocumentLayout: post.DefaultDocumentLayout()}
	if change != nil {
		change(&value)
	}
	transact(t, h, func(ctx context.Context, store post.Store) error {
		if err := store.InsertPost(ctx, &value); err != nil {
			return err
		}
		return store.UpsertSourceLocale(ctx, value.ID, value.SourceLocale, &title, nil, value.CreatedAt)
	})
	return value
}

// at is a stored timestamp: PostgreSQL keeps microseconds.
func at(value time.Time) time.Time { return value.UTC().Truncate(time.Microsecond) }

func utc(value time.Time) time.Time { return value.UTC() }

var storeContract = map[string]func(t *testing.T, h Harness){
	"a missing post, comment, version, locale and file answer their sentinels": func(t *testing.T, h Harness) {
		missing := uuid.NewString()
		transact(t, h, func(ctx context.Context, store post.Store) error {
			for name, read := range map[string]func() error{
				"FindPost":             func() error { _, err := store.FindPost(ctx, missing); return err },
				"ReadPost":             func() error { _, err := store.ReadPost(ctx, missing); return err },
				"TakePost":             func() error { _, err := store.TakePost(ctx, missing); return err },
				"FindPostID":           func() error { return store.FindPostID(ctx, missing) },
				"PostStatus":           func() error { _, err := store.PostStatus(ctx, missing); return err },
				"LockPostStatus":       func() error { _, err := store.LockPostStatus(ctx, missing); return err },
				"LockParticipantRoot":  func() error { _, err := store.LockParticipantRoot(ctx, missing); return err },
				"KeySharePostStatus":   func() error { _, err := store.KeySharePostStatus(ctx, missing); return err },
				"KeyShareLockPost":     func() error { _, err := store.KeyShareLockPost(ctx, missing); return err },
				"LockPostRoot":         func() error { return store.LockPostRoot(ctx, missing) },
				"DocumentRoot":         func() error { _, err := store.DocumentRoot(ctx, missing); return err },
				"LockDocumentRoot":     func() error { _, err := store.LockDocumentRoot(ctx, missing); return err },
				"AIDocumentRoot":       func() error { _, err := store.AIDocumentRoot(ctx, missing, post.KeyShareLock); return err },
				"SourceLocale":         func() error { _, err := store.SourceLocale(ctx, missing); return err },
				"FeaturedImageFileID":  func() error { _, err := store.FeaturedImageFileID(ctx, missing); return err },
				"ShareLockCommentPost": func() error { _, err := store.ShareLockCommentPost(ctx, missing); return err },
				"LockCommentPost":      func() error { _, err := store.LockCommentPost(ctx, missing); return err },
			} {
				require.ErrorIs(t, read(), post.ErrPostNotFound, name)
			}
			for name, read := range map[string]func() error{
				"FindComment":     func() error { _, err := store.FindComment(ctx, missing); return err },
				"FindPostComment": func() error { _, err := store.FindPostComment(ctx, missing, missing); return err },
				"LockComment":     func() error { _, err := store.LockComment(ctx, missing); return err },
				"LockPostComment": func() error { _, err := store.LockPostComment(ctx, missing, missing); return err },
				"CommentPostID":   func() error { _, err := store.CommentPostID(ctx, missing); return err },
			} {
				require.ErrorIs(t, read(), post.ErrCommentNotFound, name)
			}
			_, err := store.FindVersion(ctx, missing)
			require.ErrorIs(t, err, post.ErrVersionNotFound)
			_, err = store.LocaleMetadata(ctx, missing, "en", true)
			require.ErrorIs(t, err, post.ErrLocaleNotFound)
			require.ErrorIs(t, store.FindFile(ctx, missing), post.ErrFileNotFound)
			_, found, err := store.LockLatestVersion(ctx, missing)
			require.NoError(t, err)
			require.False(t, found)
			_, found, err = store.OptionalLocaleMetadata(ctx, missing, "en", false)
			require.NoError(t, err)
			require.False(t, found)
			return nil
		})
	},
	"an inserted post reads back with its ID, revision, timestamps and source locale": func(t *testing.T, h Harness) {
		value := insert(t, h, "Inserted", func(value *post.Post) {
			value.Slug, value.CommentsEnabled = text("inserted-"+uuid.NewString()[:8]), true
		})
		require.NotEmpty(t, value.ID)
		require.NotEmpty(t, value.ConfigurationRevision)
		require.False(t, value.CreatedAt.IsZero())
		transact(t, h, func(ctx context.Context, store post.Store) error {
			stored, err := store.FindPost(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, value.ID, stored.ID)
			require.Equal(t, *value.Slug, *stored.Slug)
			require.Equal(t, post.StatusDraft, stored.Status)
			require.True(t, stored.CommentsEnabled)
			require.Equal(t, value.ConfigurationRevision, stored.ConfigurationRevision)
			require.Equal(t, post.DefaultDocumentLayout(), stored.DocumentLayout)
			require.Equal(t, at(value.CreatedAt), utc(stored.CreatedAt))
			root, err := store.LockDocumentRoot(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, *value.ContentDocumentID, root.ContentDocumentID.String())
			require.Equal(t, post.DocumentRoot{ID: value.ID, Status: post.StatusDraft, ContentDocumentID: root.ContentDocumentID, SourceLocale: "en"}, root)
			aiRoot, err := store.AIDocumentRoot(ctx, value.ID, post.UpdateLock)
			require.NoError(t, err)
			require.Equal(t, post.AIDocumentRoot{ID: value.ID, Status: post.StatusDraft, ContentDocumentID: value.ContentDocumentID}, aiRoot)
			locale, err := store.SourceLocale(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, "en", locale)
			states, err := store.SourceLocaleStates(ctx, []string{value.ID, " ", uuid.NewString()})
			require.NoError(t, err)
			require.Equal(t, map[string]post.SourceLocaleState{value.ID: {Title: text("Inserted")}}, states)
			return nil
		})
	},
	"a taken slug fails the insert and only other posts take a slug": func(t *testing.T, h Harness) {
		slug := "taken-" + uuid.NewString()[:8]
		first := insert(t, h, "First", func(value *post.Post) { value.Slug = text(slug) })
		documentID := h.SeedDocument(t)
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store post.Store) error {
			return store.InsertPost(ctx, &post.Post{
				SourceLocale: "en", Status: post.StatusDraft, Slug: text(slug), ContentDocumentID: &documentID,
				DocumentLayout: post.DefaultDocumentLayout(),
			})
		})
		require.ErrorIs(t, err, post.ErrSlugTaken)
		transact(t, h, func(ctx context.Context, store post.Store) error {
			taken, err := store.SlugTaken(ctx, slug, "")
			require.NoError(t, err)
			require.True(t, taken)
			taken, err = store.SlugTaken(ctx, slug, first.ID)
			require.NoError(t, err)
			require.False(t, taken, "a post never takes its own slug")
			return nil
		})
	},
	"a settings change writes only the changed columns and advances the configuration revision": func(t *testing.T, h Harness) {
		value := insert(t, h, "Settings", func(value *post.Post) { value.Slug = text("settings-" + uuid.NewString()[:8]) })
		mapPlaceID := h.SeedMapPlace(t)
		updatedAt := at(time.Now().Add(time.Minute))
		transact(t, h, func(ctx context.Context, store post.Store) error {
			exists, err := store.MapPlaceExists(ctx, mapPlaceID)
			require.NoError(t, err)
			require.True(t, exists)
			exists, err = store.MapPlaceExists(ctx, uuid.NewString())
			require.NoError(t, err)
			require.False(t, exists)

			stored, err := store.TakePost(ctx, value.ID)
			require.NoError(t, err)
			stored.Title = "kept"
			require.NoError(t, store.UpdateSettings(ctx, &stored, post.SettingsChange{UpdatedAt: updatedAt}))
			require.Equal(t, value.ConfigurationRevision, stored.ConfigurationRevision, "touching updated_at keeps the revision")
			require.Equal(t, updatedAt, utc(stored.UpdatedAt))
			require.Equal(t, "kept", stored.Title, "the update keeps the caller's locale fields")

			layout := post.DocumentLayout{ContentHeight: post.ContentHeightViewport, PageChrome: post.PlacementPinned, Footer: post.PlacementFlow, HideTableOfContents: true}
			enabled := true
			require.NoError(t, store.UpdateSettings(ctx, &stored, post.SettingsChange{
				MapPlaceID: &post.OptionalString{Value: &mapPlaceID}, CommentsEnabled: &enabled, DocumentLayout: &layout,
				UpdatedAt: updatedAt,
			}))
			require.NotEqual(t, value.ConfigurationRevision, stored.ConfigurationRevision)
			require.Equal(t, mapPlaceID, *stored.MapPlaceID)
			require.Equal(t, *value.Slug, *stored.Slug, "an unchanged slug stays")
			read, err := store.FindPost(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, stored.ConfigurationRevision, read.ConfigurationRevision)
			require.Equal(t, layout, read.DocumentLayout)
			require.True(t, read.CommentsEnabled)

			advanced := stored.ConfigurationRevision
			require.NoError(t, store.UpdateSettings(ctx, &stored, post.SettingsChange{Slug: &post.OptionalString{}, UpdatedAt: updatedAt}))
			require.Nil(t, stored.Slug)
			require.NotEqual(t, advanced, stored.ConfigurationRevision, "clearing the slug advances the revision")
			return nil
		})
	},
	"lifecycle changes store the status, schedule and published time": func(t *testing.T, h Harness) {
		value := insert(t, h, "Lifecycle", nil)
		scheduledAt, publishedAt := at(time.Now().Add(time.Hour)), at(time.Now())
		zone := "Asia/Seoul"
		transact(t, h, func(ctx context.Context, store post.Store) error {
			require.NoError(t, store.UpdateLifecycle(ctx, value.ID, post.LifecycleChange{
				Status: post.StatusScheduled, UpdatedAt: publishedAt, ScheduledAt: &scheduledAt, ScheduledTimeZone: &zone,
			}))
			stored, err := store.ReadPost(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, post.StatusScheduled, stored.Status)
			require.Equal(t, scheduledAt, utc(*stored.ScheduledAt))
			require.Equal(t, zone, *stored.ScheduledTimeZone)
			require.Nil(t, stored.PublishedAt)

			require.NoError(t, store.UpdateLifecycle(ctx, value.ID, post.LifecycleChange{
				Status: post.StatusPublished, UpdatedAt: publishedAt, ClearSchedule: true, PublishedAt: &publishedAt,
			}))
			status, err := store.LockPostStatus(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, post.StatusPublished, status)
			stored, err = store.ReadPost(ctx, value.ID)
			require.NoError(t, err)
			require.Nil(t, stored.ScheduledAt)
			require.Nil(t, stored.ScheduledTimeZone)
			require.Equal(t, publishedAt, utc(*stored.PublishedAt))

			require.NoError(t, store.UpdateLifecycle(ctx, value.ID, post.LifecycleChange{Status: post.StatusDraft, UpdatedAt: publishedAt}))
			stored, err = store.ReadPost(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, post.StatusDraft, stored.Status)
			require.Equal(t, publishedAt, utc(*stored.PublishedAt), "unpublishing keeps published_at")
			return nil
		})
	},
	"a due scheduled post is published once and a later one waits": func(t *testing.T, h Harness) {
		due, later := insert(t, h, "Due", nil), insert(t, h, "Later", nil)
		past, future := at(time.Now().Add(-time.Hour)), at(time.Now().Add(time.Hour))
		transact(t, h, func(ctx context.Context, store post.Store) error {
			require.NoError(t, store.UpdateLifecycle(ctx, due.ID, post.LifecycleChange{Status: post.StatusScheduled, UpdatedAt: past, ScheduledAt: &past}))
			return store.UpdateLifecycle(ctx, later.ID, post.LifecycleChange{Status: post.StatusScheduled, UpdatedAt: past, ScheduledAt: &future})
		})
		transact(t, h, func(ctx context.Context, store post.Store) error {
			now, err := store.DatabaseNow(ctx)
			require.NoError(t, err)
			posts, err := store.LockDuePosts(ctx, 10)
			require.NoError(t, err)
			require.Len(t, posts, 1)
			require.Equal(t, due.ID, posts[0].ID)
			published, err := store.PublishDuePost(ctx, due.ID, &past, now)
			require.NoError(t, err)
			require.True(t, published)
			published, err = store.PublishDuePost(ctx, due.ID, &past, now)
			require.NoError(t, err)
			require.False(t, published, "a published post is not due again")
			published, err = store.PublishDuePost(ctx, later.ID, nil, now)
			require.NoError(t, err)
			require.False(t, published, "a future schedule is not due")
			stored, err := store.ReadPost(ctx, due.ID)
			require.NoError(t, err)
			require.Equal(t, post.StatusPublished, stored.Status)
			require.Nil(t, stored.ScheduledAt)
			require.Equal(t, past, utc(*stored.PublishedAt))
			return nil
		})
	},
	"listing scopes to viewable posts, keeps archived ones apart and orders latest published first": func(t *testing.T, h Harness) {
		base := at(time.Now())
		older, newer := base.Add(-time.Hour), base
		draft, publishedOld, publishedNew, archived, hidden := insert(t, h, "Draft", nil), insert(t, h, "Old", nil),
			insert(t, h, "New", nil), insert(t, h, "Archived", nil), insert(t, h, "Hidden", nil)
		transact(t, h, func(ctx context.Context, store post.Store) error {
			require.NoError(t, store.UpdateLifecycle(ctx, publishedOld.ID, post.LifecycleChange{Status: post.StatusPublished, UpdatedAt: base, PublishedAt: &older}))
			require.NoError(t, store.UpdateLifecycle(ctx, publishedNew.ID, post.LifecycleChange{Status: post.StatusPublished, UpdatedAt: base, PublishedAt: &newer}))
			return store.UpdateLifecycle(ctx, archived.ID, post.LifecycleChange{Status: post.StatusArchived, UpdatedAt: base})
		})
		transact(t, h, func(ctx context.Context, store post.Store) error {
			scope := post.ViewScope{ViewIDs: []string{draft.ID, publishedOld.ID, publishedNew.ID, archived.ID}}
			posts, total, err := store.ListPosts(ctx, post.ListQuery{Scope: &scope, Limit: 10})
			require.NoError(t, err)
			require.EqualValues(t, 3, total, "an archived post needs the archived scope")
			require.Equal(t, []string{publishedNew.ID, publishedOld.ID, draft.ID}, postIDs(posts))

			scope.ArchivedIDs = []string{archived.ID}
			posts, total, err = store.ListPosts(ctx, post.ListQuery{Scope: &scope, Limit: 2, Offset: 2})
			require.NoError(t, err)
			require.EqualValues(t, 4, total)
			require.Len(t, posts, 2)
			require.NotContains(t, postIDs(posts), hidden.ID)

			posts, total, err = store.ListPosts(ctx, post.ListQuery{Scope: &post.ViewScope{}, Limit: 10})
			require.NoError(t, err)
			require.Zero(t, total, "an empty scope views nothing")
			require.Empty(t, posts)

			items, total, err := store.ListAIDocuments(ctx, post.AIDocumentQuery{Scope: post.ViewScope{ViewIDs: []string{draft.ID, publishedOld.ID}}, Limit: 10})
			require.NoError(t, err)
			require.EqualValues(t, 2, total)
			require.ElementsMatch(t, []string{"Draft", "Old"}, []string{items[0].Title, items[1].Title})
			return nil
		})
	},
	"locale rows are upserted, updated, counted and deleted": func(t *testing.T, h Harness) {
		value := insert(t, h, "Source", nil)
		created := at(time.Now())
		transact(t, h, func(ctx context.Context, store post.Store) error {
			inserted, err := store.InsertLocale(ctx, value.ID, "ko", text("제목"), nil, created)
			require.NoError(t, err)
			require.EqualValues(t, 1, inserted)
			require.NoError(t, store.InsertEmptyLocale(ctx, value.ID, "ja", created))
			require.NoError(t, store.UpsertLocaleText(ctx, value.ID, "ja", text("タイトル"), text("要約"), created.Add(time.Second)))
			metadata, err := store.LocaleMetadata(ctx, value.ID, "ja", true)
			require.NoError(t, err)
			require.Equal(t, post.LocaleMetadata{Locale: "ja", Title: text("タイトル"), Summary: text("要約"), UpdatedAt: created.Add(time.Second)}, normalized(metadata))

			changed, err := store.UpdateLocaleMetadata(ctx, value.ID, "ko", post.LocaleChange{SetSummary: true, Summary: text("요약"), UpdatedAt: created.Add(2 * time.Second)})
			require.NoError(t, err)
			require.EqualValues(t, 1, changed)
			changed, err = store.UpdateLocaleMetadata(ctx, value.ID, "fr", post.LocaleChange{SetTitle: true, Title: text("Titre"), UpdatedAt: created})
			require.NoError(t, err)
			require.Zero(t, changed, "a missing locale changes nothing")
			localeText, found, err := store.LocaleText(ctx, value.ID, "ko", false)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, "제목", *localeText.Title, "an update keeps the fields it does not set")
			require.Equal(t, "요약", *localeText.Summary)

			count, err := store.CountLocale(ctx, value.ID, "ko")
			require.NoError(t, err)
			require.EqualValues(t, 1, count)
			deleted, err := store.DeleteLocale(ctx, value.ID, "ko")
			require.NoError(t, err)
			require.EqualValues(t, 1, deleted)
			deleted, err = store.DeleteLocale(ctx, value.ID, "ko")
			require.NoError(t, err)
			require.Zero(t, deleted)
			_, found, err = store.OptionalLocaleMetadata(ctx, value.ID, "ko", true)
			require.NoError(t, err)
			require.False(t, found)
			return nil
		})
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store post.Store) error {
			_, err := store.InsertLocale(ctx, value.ID, "ja", nil, nil, created)
			return err
		})
		require.Error(t, err, "a locale is inserted once")
	},
	"switching the source locale moves only from the expected locale": func(t *testing.T, h Harness) {
		value := insert(t, h, "Switch", nil)
		now := at(time.Now())
		transact(t, h, func(ctx context.Context, store post.Store) error {
			switched, err := store.SwitchSourceLocale(ctx, value.ID, "ko", "ja", now)
			require.NoError(t, err)
			require.Zero(t, switched)
			switched, err = store.SwitchSourceLocale(ctx, value.ID, "en", "ko", now)
			require.NoError(t, err)
			require.EqualValues(t, 1, switched)
			locale, err := store.SourceLocale(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, "ko", locale)
			states, err := store.SourceLocaleStates(ctx, []string{value.ID})
			require.NoError(t, err)
			require.Empty(t, states, "the new source locale has no row yet")
			return nil
		})
	},
	"taxonomy links are inserted, replaced and counted against stored terms": func(t *testing.T, h Harness) {
		value := insert(t, h, "Taxonomy", nil)
		first, second := h.SeedTaxonomy(t, post.Categories), h.SeedTaxonomy(t, post.Categories)
		tag := h.SeedTaxonomy(t, post.Tags)
		transact(t, h, func(ctx context.Context, store post.Store) error {
			count, err := store.CountTaxonomy(ctx, post.Categories, []string{first, second, tag})
			require.NoError(t, err)
			require.EqualValues(t, 2, count, "a Tag is no Category")
			require.NoError(t, store.InsertTaxonomyLinks(ctx, post.Categories, value.ID, []string{first}))
			require.NoError(t, store.InsertTaxonomyLinks(ctx, post.Tags, value.ID, []string{tag}))
			categories, tags, err := store.TaxonomyIDs(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, []string{first}, categories)
			require.Equal(t, []string{tag}, tags)

			require.NoError(t, store.ReplaceTaxonomyLinks(ctx, post.Categories, value.ID, []string{second, first}))
			require.NoError(t, store.ReplaceTaxonomyLinks(ctx, post.Tags, value.ID, nil))
			categories, tags, err = store.TaxonomyIDs(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, sorted(first, second), categories)
			require.Empty(t, tags)
			stored, err := store.FindPost(ctx, value.ID)
			require.NoError(t, err)
			require.ElementsMatch(t, []string{first, second}, categoryIDs(stored))
			require.Empty(t, stored.Tags)
			return nil
		})
	},
	"participants are stored per role and listed Authors first, oldest first": func(t *testing.T, h Harness) {
		value := insert(t, h, "Participants", nil)
		author, coauthor, collaborator := h.SeedMember(t, "Author"), h.SeedMember(t, " "), h.SeedMember(t, "Collaborator")
		base := at(time.Now())
		transact(t, h, func(ctx context.Context, store post.Store) error {
			require.NoError(t, store.InsertParticipant(ctx, value.ID, coauthor, post.RoleAuthor, base.Add(time.Second)))
			require.NoError(t, store.InsertParticipant(ctx, value.ID, author, post.RoleAuthor, base))
			require.NoError(t, store.InsertParticipant(ctx, value.ID, collaborator, post.RoleCollaborator, base.Add(-time.Hour)))
			rows, err := store.ListParticipants(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, []post.ParticipantRow{
				{MemberID: author, Role: post.RoleAuthor, CreatedAt: base},
				{MemberID: coauthor, Role: post.RoleAuthor, CreatedAt: base.Add(time.Second)},
				{MemberID: collaborator, Role: post.RoleCollaborator, CreatedAt: base.Add(-time.Hour)},
			}, normalizedRows(rows))
			links, err := store.AuthorLinks(ctx, []string{value.ID})
			require.NoError(t, err)
			require.Equal(t, []post.AuthorLink{{PostID: value.ID, MemberID: author}, {PostID: value.ID, MemberID: coauthor}}, links)

			count, err := store.CountAuthors(ctx, value.ID, "")
			require.NoError(t, err)
			require.EqualValues(t, 2, count)
			count, err = store.CountAuthors(ctx, value.ID, collaborator)
			require.NoError(t, err)
			require.Zero(t, count)
			exists, err := store.ParticipantExists(ctx, value.ID, collaborator, post.RoleCollaborator)
			require.NoError(t, err)
			require.True(t, exists)
			createdAt, err := store.ParticipantCreatedAt(ctx, value.ID, author, post.RoleAuthor)
			require.NoError(t, err)
			require.Equal(t, base, utc(createdAt))
			createdAt, err = store.ParticipantCreatedAt(ctx, value.ID, author, post.RoleCollaborator)
			require.NoError(t, err)
			require.True(t, createdAt.IsZero(), "a missing relation has no creation time")

			deleted, err := store.DeleteParticipant(ctx, value.ID, coauthor, post.RoleAuthor)
			require.NoError(t, err)
			require.EqualValues(t, 1, deleted)
			deleted, err = store.DeleteParticipant(ctx, value.ID, coauthor, post.RoleAuthor)
			require.NoError(t, err)
			require.Zero(t, deleted)
			names, err := store.MemberNicknames(ctx, []string{author, coauthor, uuid.NewString()})
			require.NoError(t, err)
			require.Equal(t, map[string]string{author: "Author"}, names, "a blank nickname is no name")
			_, err = store.LockLinkedMemberIdentity(ctx, author)
			require.Error(t, err, "a Member without an Identity has no authorization target")
			return nil
		})
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store post.Store) error {
			return store.InsertParticipant(ctx, value.ID, author, post.RoleAuthor, base)
		})
		require.Error(t, err, "a Member holds a role once")
	},
	"comments are threaded, paged after a cursor and soft deleted": func(t *testing.T, h Harness) {
		value := insert(t, h, "Comments", func(value *post.Post) { value.CommentsEnabled = true })
		other := insert(t, h, "Other", nil)
		memberID := h.SeedMember(t, "Commenter")
		base := at(time.Now())
		var first, second, reply, nested post.Comment
		transact(t, h, func(ctx context.Context, store post.Store) error {
			commentPost, err := store.LockCommentPost(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, post.CommentPost{ID: value.ID, Status: post.StatusDraft, CommentsEnabled: true}, commentPost)
			first = post.Comment{PostID: value.ID, MemberID: &memberID, Content: "first", CreatedAt: base, UpdatedAt: base}
			second = post.Comment{PostID: value.ID, MemberID: &memberID, Content: "second", CreatedAt: base.Add(time.Second), UpdatedAt: base.Add(time.Second)}
			require.NoError(t, store.InsertComment(ctx, &first))
			require.NoError(t, store.InsertComment(ctx, &second))
			reply = post.Comment{PostID: value.ID, MemberID: &memberID, ParentID: &first.ID, Content: "reply", CreatedAt: base.Add(2 * time.Second), UpdatedAt: base.Add(2 * time.Second)}
			require.NoError(t, store.InsertComment(ctx, &reply))
			nested = post.Comment{PostID: value.ID, ParentID: &reply.ID, Content: "nested", CreatedAt: base.Add(3 * time.Second), UpdatedAt: base.Add(3 * time.Second)}
			return store.InsertComment(ctx, &nested)
		})
		require.NotEmpty(t, first.ID)
		transact(t, h, func(ctx context.Context, store post.Store) error {
			roots, err := store.CountComments(ctx, value.ID, nil)
			require.NoError(t, err)
			require.EqualValues(t, 2, roots)
			replies, err := store.CountComments(ctx, value.ID, &first.ID)
			require.NoError(t, err)
			require.EqualValues(t, 1, replies)
			page, err := store.ListComments(ctx, value.ID, nil, nil, 1)
			require.NoError(t, err)
			require.Equal(t, []string{first.ID}, commentIDs(page))
			cursorAt, found, err := store.CommentCursor(ctx, value.ID, nil, first.ID)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, base, utc(cursorAt))
			_, found, err = store.CommentCursor(ctx, value.ID, nil, reply.ID)
			require.NoError(t, err)
			require.False(t, found, "a reply is no root cursor")
			page, err = store.ListComments(ctx, value.ID, nil, &post.CommentCursorAt{ID: first.ID, CreatedAt: cursorAt}, 10)
			require.NoError(t, err)
			require.Equal(t, []string{second.ID}, commentIDs(page))
			descendants, err := store.CommentDescendants(ctx, value.ID, []string{first.ID}, 1)
			require.NoError(t, err)
			require.Equal(t, []string{reply.ID}, commentIDs(descendants))
			descendants, err = store.CommentDescendants(ctx, value.ID, []string{first.ID, second.ID}, 3)
			require.NoError(t, err)
			require.Equal(t, []string{reply.ID, nested.ID}, commentIDs(descendants))
			depth, err := store.CommentDepth(ctx, value.ID, nested.ID, 10)
			require.NoError(t, err)
			require.Equal(t, 3, depth)
			depth, err = store.CommentDepth(ctx, value.ID, nested.ID, 2)
			require.NoError(t, err)
			require.Equal(t, 2, depth, "the depth stops at the limit")
			depth, err = store.CommentDepth(ctx, other.ID, nested.ID, 10)
			require.NoError(t, err)
			require.Zero(t, depth)
			counts, err := store.CommentCounts(ctx, []string{value.ID, other.ID})
			require.NoError(t, err)
			require.Equal(t, map[string]int32{value.ID: 4}, counts)

			_, err = store.FindPostComment(ctx, other.ID, first.ID)
			require.ErrorIs(t, err, post.ErrCommentNotFound, "a comment belongs to one post")
			locked, err := store.LockPostComment(ctx, value.ID, second.ID)
			require.NoError(t, err)
			require.NoError(t, store.UpdateCommentContent(ctx, locked, "edited"))
			require.NoError(t, store.SoftDeleteComment(ctx, first, "[deleted]"))
			edited, err := store.FindComment(ctx, second.ID)
			require.NoError(t, err)
			require.Equal(t, "edited", edited.Content)
			require.False(t, edited.IsDeleted)
			deleted, err := store.LockComment(ctx, first.ID)
			require.NoError(t, err)
			require.True(t, deleted.IsDeleted)
			require.Equal(t, "[deleted]", deleted.Content)
			require.Equal(t, memberID, *deleted.MemberID)
			postID, err := store.CommentPostID(ctx, nested.ID)
			require.NoError(t, err)
			require.Equal(t, value.ID, postID)
			return nil
		})
	},
	"versions are numbered per post and listed newest first": func(t *testing.T, h Harness) {
		value := insert(t, h, "Versions", nil)
		memberID := h.SeedMember(t, "Contributor")
		snapshot := json.RawMessage(`{"blocks":[]}`)
		var first, second post.StoredVersion
		transact(t, h, func(ctx context.Context, store post.Store) error {
			first = post.StoredVersion{PostID: value.ID, Version: 1, ContentSnapshot: snapshot, ContributorMemberIDs: []string{memberID}}
			require.NoError(t, store.InsertVersion(ctx, &first))
			second = post.StoredVersion{PostID: value.ID, Version: 2, ContentSnapshot: snapshot, ContributorMemberIDs: []string{}}
			return store.InsertVersion(ctx, &second)
		})
		require.NotEmpty(t, first.ID)
		require.False(t, first.CreatedAt.IsZero())
		transact(t, h, func(ctx context.Context, store post.Store) error {
			latest, found, err := store.LockLatestVersion(ctx, value.ID)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, second.ID, latest.ID)
			versions, total, err := store.ListVersions(ctx, value.ID, 1, 1)
			require.NoError(t, err)
			require.EqualValues(t, 2, total)
			require.Len(t, versions, 1)
			require.Equal(t, first.ID, versions[0].ID)
			stored, err := store.FindVersion(ctx, first.ID)
			require.NoError(t, err)
			require.EqualValues(t, 1, stored.Version)
			require.Equal(t, []string{memberID}, stored.ContributorMemberIDs)
			require.JSONEq(t, string(snapshot), string(stored.ContentSnapshot))
			return nil
		})
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store post.Store) error {
			return store.InsertVersion(ctx, &post.StoredVersion{PostID: value.ID, Version: 2, ContentSnapshot: snapshot})
		})
		require.Error(t, err, "a version number is taken once per post")
	},
	"the featured image is set and cleared, and a touch moves only updated_at": func(t *testing.T, h Harness) {
		value := insert(t, h, "Featured", nil)
		fileID := h.SeedFile(t)
		now := at(time.Now().Add(time.Minute))
		transact(t, h, func(ctx context.Context, store post.Store) error {
			require.NoError(t, store.FindFile(ctx, fileID))
			require.NoError(t, store.SetFeaturedImage(ctx, value.ID, &fileID, now))
			current, err := store.FeaturedImageFileID(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, fileID, *current)
			require.NoError(t, store.SetFeaturedImage(ctx, value.ID, nil, now))
			current, err = store.FeaturedImageFileID(ctx, value.ID)
			require.NoError(t, err)
			require.Nil(t, current)
			require.NoError(t, store.TouchPost(ctx, value.ID, now.Add(time.Second)))
			stored, err := store.ReadPost(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, now.Add(time.Second), utc(stored.UpdatedAt))
			require.Equal(t, value.ConfigurationRevision, stored.ConfigurationRevision)
			return nil
		})
	},
	"a deleted post takes its rows with it": func(t *testing.T, h Harness) {
		value := insert(t, h, "Deleted", nil)
		memberID := h.SeedMember(t, "Author")
		transact(t, h, func(ctx context.Context, store post.Store) error {
			require.NoError(t, store.InsertCreatorAuthor(ctx, value.ID, memberID))
			return store.InsertComment(ctx, &post.Comment{PostID: value.ID, Content: "gone"})
		})
		transact(t, h, func(ctx context.Context, store post.Store) error {
			require.NoError(t, store.DeletePost(ctx, value.ID))
			require.ErrorIs(t, store.FindPostID(ctx, value.ID), post.ErrPostNotFound)
			_, found, err := store.OptionalLocaleMetadata(ctx, value.ID, "en", false)
			require.NoError(t, err)
			require.False(t, found)
			count, err := store.CountAuthors(ctx, value.ID, "")
			require.NoError(t, err)
			require.Zero(t, count)
			comments, err := store.CountComments(ctx, value.ID, nil)
			require.NoError(t, err)
			require.Zero(t, comments)
			return nil
		})
	},
	"a content revision advances only from the expected revision": func(t *testing.T, h Harness) {
		documentID := uuid.New()
		fence := func(context.Context, post.Store, uuid.UUID) (contentblock.DomainContext, error) {
			return contentblock.DomainContext{SourceLocale: "en"}, nil
		}
		changed := func(context.Context, post.Store) (contentblock.MetadataEffect, error) {
			return contentblock.MetadataEffect{Changed: true, AffectsTranslationSource: true}, nil
		}
		var created contentblock.Snapshot
		transact(t, h, func(ctx context.Context, store post.Store) error {
			var err error
			created, err = store.CreateContentDocument(ctx, contentblock.CreateInput{ID: documentID, Profile: "post", SourceLocale: "en"})
			return err
		})
		require.Equal(t, documentID, created.Document.ID)
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store post.Store) error {
			_, err := store.AdvanceContentRevision(ctx, contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: uuid.New()}, fence, changed)
			return err
		})
		var stale *contentblock.StaleRevisionError
		require.ErrorAs(t, err, &stale)
		require.Equal(t, created.Document.Revision, stale.CurrentRevision)
		transact(t, h, func(ctx context.Context, store post.Store) error {
			unchanged, err := store.AdvanceContentRevision(ctx, contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: created.Document.Revision}, fence,
				func(context.Context, post.Store) (contentblock.MetadataEffect, error) { return contentblock.MetadataEffect{}, nil })
			require.NoError(t, err)
			require.Equal(t, contentblock.AdvanceResult{DocumentRevision: created.Document.Revision}, unchanged, "an unchanged effect keeps the revision")
			advanced, err := store.AdvanceContentRevision(ctx, contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: created.Document.Revision}, fence, changed)
			require.NoError(t, err)
			require.True(t, advanced.Changed)
			require.True(t, advanced.TranslationSourceChanged)
			require.NotEqual(t, created.Document.Revision, advanced.DocumentRevision)
			revision, err := store.ContentRevision(ctx, documentID)
			require.NoError(t, err)
			require.Equal(t, advanced.DocumentRevision, revision)
			require.NoError(t, store.LockContentDocument(ctx, documentID))
			return store.DeleteContentDocument(ctx, documentID, fence)
		})
		transact(t, h, func(ctx context.Context, store post.Store) error {
			_, err := store.ContentRevision(ctx, documentID)
			require.Error(t, err, "a deleted document has no revision")
			return nil
		})
	},
	"a failed transaction leaves nothing behind": func(t *testing.T, h Harness) {
		value := insert(t, h, "Kept", nil)
		failed := errors.New("later step failed")
		documentID := h.SeedDocument(t)
		created := post.Post{SourceLocale: "en", Status: post.StatusDraft, ContentDocumentID: &documentID, DocumentLayout: post.DefaultDocumentLayout()}
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store post.Store) error {
			if err := store.InsertPost(ctx, &created); err != nil {
				return err
			}
			if err := store.UpdateLifecycle(ctx, value.ID, post.LifecycleChange{Status: post.StatusPublished, UpdatedAt: time.Now()}); err != nil {
				return err
			}
			if err := store.UpsertSourceLocale(ctx, value.ID, "en", text("Rolled back"), nil, time.Now()); err != nil {
				return err
			}
			return failed
		})
		require.ErrorIs(t, err, failed)
		transact(t, h, func(ctx context.Context, store post.Store) error {
			require.ErrorIs(t, store.FindPostID(ctx, created.ID), post.ErrPostNotFound)
			status, err := store.PostStatus(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, post.StatusDraft, status)
			metadata, err := store.LocaleMetadata(ctx, value.ID, "en", false)
			require.NoError(t, err)
			require.Equal(t, "Kept", *metadata.Title)
			return nil
		})
	},
	"a locked post makes a second writer wait for the first commit": func(t *testing.T, h Harness) {
		value := insert(t, h, "Locked", nil)
		var seen post.Status
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store post.Store) error {
				_, err := store.LockPostStatus(ctx, value.ID)
				return err
			},
			func(ctx context.Context, store post.Store) error {
				return store.UpdateLifecycle(ctx, value.ID, post.LifecycleChange{Status: post.StatusArchived, UpdatedAt: time.Now()})
			},
			func(ctx context.Context, store post.Store) error {
				var err error
				seen, err = store.LockPostStatus(ctx, value.ID)
				return err
			},
		)
		require.NoError(t, err)
		require.Equal(t, post.StatusArchived, seen, "the second writer reads the first commit")
	},
	"a locked comment post makes a commenter wait for the first commit": func(t *testing.T, h Harness) {
		value := insert(t, h, "Comment lock", func(value *post.Post) { value.CommentsEnabled = true })
		var seen post.CommentPost
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store post.Store) error {
				_, err := store.LockCommentPost(ctx, value.ID)
				return err
			},
			func(ctx context.Context, store post.Store) error {
				stored, err := store.TakePost(ctx, value.ID)
				if err != nil {
					return err
				}
				disabled := false
				return store.UpdateSettings(ctx, &stored, post.SettingsChange{CommentsEnabled: &disabled, UpdatedAt: time.Now()})
			},
			func(ctx context.Context, store post.Store) error {
				var err error
				seen, err = store.ShareLockCommentPost(ctx, value.ID)
				return err
			},
		)
		require.NoError(t, err)
		require.False(t, seen.CommentsEnabled, "the commenter reads the first commit")
	},
}

func postIDs(posts []post.Post) []string {
	ids := make([]string, len(posts))
	for i, stored := range posts {
		ids[i] = stored.ID
	}
	return ids
}

func categoryIDs(stored post.Post) []string {
	ids := make([]string, len(stored.Categories))
	for i, category := range stored.Categories {
		ids[i] = category.ID
	}
	return ids
}

func commentIDs(comments []post.Comment) []string {
	ids := make([]string, len(comments))
	for i, comment := range comments {
		ids[i] = comment.ID
	}
	return ids
}

func sorted(values ...string) []string {
	return sortedKeys(func() map[string]bool {
		set := map[string]bool{}
		for _, value := range values {
			set[value] = true
		}
		return set
	}())
}

func normalized(metadata post.LocaleMetadata) post.LocaleMetadata {
	metadata.UpdatedAt = utc(metadata.UpdatedAt)
	return metadata
}

func normalizedRows(rows []post.ParticipantRow) []post.ParticipantRow {
	for i := range rows {
		rows[i].CreatedAt = utc(rows[i].CreatedAt)
	}
	return rows
}
