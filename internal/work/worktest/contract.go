package worktest

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/work"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// Harness is one work.Store implementation the contract runs against,
// with the rows of other domains a case needs.
type Harness struct {
	mutationtest.Harness[work.Store]
	// SeedDocument stores an empty Work content document and returns its
	// ID.
	SeedDocument func(t *testing.T) string
	// SeedMember stores an onboarded Member, a deleted one when deleted is
	// set, and returns its ID.
	SeedMember func(t *testing.T, nickname string, preferredLocale *string, deleted bool) string
	// SeedClient stores a client and returns its ID.
	SeedClient func(t *testing.T, name string) string
	// SeedMapPlace stores a Map Place and returns its ID.
	SeedMapPlace func(t *testing.T) string
	// SeedArtist stores an Artist with its source-locale name, owned by
	// ownerMemberID when set, and returns its ID.
	SeedArtist func(t *testing.T, name string, ownerMemberID string) string
}

// FakeHarness is the Harness of the fake Store.
func FakeHarness() Harness {
	store := NewStore()
	return Harness{
		Harness:      mutationtest.FakeHarness[work.Store](store),
		SeedDocument: func(*testing.T) string { return uuid.NewString() },
		SeedMember: func(_ *testing.T, nickname string, preferredLocale *string, deleted bool) string {
			id := uuid.NewString()
			store.PutMember(id, Member{Nickname: nickname, PreferredLocale: preferredLocale, Deleted: deleted})
			return id
		},
		SeedClient: func(_ *testing.T, name string) string {
			id := uuid.NewString()
			store.PutClient(work.ClientRow{ID: id, Name: name})
			return id
		},
		SeedMapPlace: func(*testing.T) string {
			id := uuid.NewString()
			store.PutMapPlace(id)
			return id
		},
		SeedArtist: func(_ *testing.T, name string, ownerMemberID string) string {
			id := uuid.NewString()
			members := []string{}
			if ownerMemberID != "" {
				members = append(members, ownerMemberID)
			}
			store.PutArtist(work.CreditArtistRow{ID: id, Name: name}, members...)
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

func transact(t *testing.T, h Harness, work func(ctx context.Context, store work.Store) error) {
	t.Helper()
	require.NoError(t, mutationtest.Transact(t, h.Harness, work))
}

func text(value string) *string { return &value }

func number(value int32) *int32 { return &value }

// insert stores a present-tense Work with its source-locale title in its
// own transaction.
func insert(t *testing.T, h Harness, change func(*work.Work)) work.Work {
	t.Helper()
	documentID := h.SeedDocument(t)
	value := work.Work{
		Type: "WORK_TYPE_PORTFOLIO", Year: 2026, Month: 8, IsPresent: true, SourceLocale: "en",
		Status: work.StatusDraft, ContentDocumentID: &documentID,
	}
	if change != nil {
		change(&value)
	}
	transact(t, h, func(ctx context.Context, store work.Store) error {
		if err := store.InsertWork(ctx, &value); err != nil {
			return err
		}
		return store.InsertSourceLocale(ctx, value.ID, value.SourceLocale, "Source "+value.ID[:8], nil, time.Now().UTC())
	})
	return value
}

func find(t *testing.T, h Harness, workID string) work.Work {
	t.Helper()
	var value work.Work
	transact(t, h, func(ctx context.Context, store work.Store) error {
		var err error
		value, err = store.FindWork(ctx, workID)
		return err
	})
	return value
}

var storeContract = map[string]func(t *testing.T, h Harness){
	"a missing work and its rows answer their sentinels": func(t *testing.T, h Harness) {
		missing := uuid.NewString()
		transact(t, h, func(ctx context.Context, store work.Store) error {
			_, err := store.FindWork(ctx, missing)
			require.ErrorIs(t, err, work.ErrWorkNotFound)
			_, err = store.LockWork(ctx, missing, work.UpdateLock)
			require.ErrorIs(t, err, work.ErrWorkNotFound)
			_, err = store.LockRoot(ctx, missing)
			require.ErrorIs(t, err, work.ErrWorkNotFound)
			_, err = store.ContentRoot(ctx, missing, work.ShareLock)
			require.ErrorIs(t, err, work.ErrWorkNotFound)
			_, err = store.LockDocumentRoot(ctx, uuid.New())
			require.ErrorIs(t, err, work.ErrWorkNotFound)
			_, err = store.SourceLocale(ctx, missing)
			require.ErrorIs(t, err, work.ErrWorkNotFound)
			_, err = store.LockSourceLocale(ctx, missing)
			require.ErrorIs(t, err, work.ErrWorkNotFound)
			_, err = store.FeaturedImageFileID(ctx, missing)
			require.ErrorIs(t, err, work.ErrWorkNotFound)
			require.ErrorIs(t, store.SetFeaturedImage(ctx, missing, nil, time.Now()), work.ErrWorkNotFound)
			exists, err := store.WorkExists(ctx, missing)
			require.NoError(t, err)
			require.False(t, exists)
			_, err = store.FindCreditGroup(ctx, missing)
			require.ErrorIs(t, err, work.ErrCreditGroupNotFound)
			_, err = store.LockCreditGroup(ctx, missing, missing)
			require.ErrorIs(t, err, work.ErrCreditGroupNotFound)
			_, err = store.FindCredit(ctx, missing)
			require.ErrorIs(t, err, work.ErrCreditNotFound)
			_, err = store.LockCredit(ctx, missing, missing)
			require.ErrorIs(t, err, work.ErrCreditNotFound)
			_, err = store.FindVersion(ctx, missing)
			require.ErrorIs(t, err, work.ErrVersionNotFound)
			_, latest, err := store.LockLatestVersion(ctx, missing)
			require.NoError(t, err)
			require.False(t, latest)
			_, err = store.FileID(ctx, missing)
			require.ErrorIs(t, err, work.ErrFileNotFound)
			_, err = store.LockLocaleText(ctx, missing, "en")
			require.ErrorIs(t, err, work.ErrLocaleNotFound)
			_, err = store.LocaleUpdatedAt(ctx, missing, "en")
			require.ErrorIs(t, err, work.ErrLocaleNotFound)
			_, found, err := store.LockedLocaleText(ctx, missing, "en")
			require.NoError(t, err)
			require.False(t, found)
			_, found, err = store.LocaleMetadata(ctx, missing, "en", true)
			require.NoError(t, err)
			require.False(t, found)
			change := work.LocaleTextChange{SetTitle: true, Title: text("Title"), UpdatedAt: time.Now()}
			require.ErrorIs(t, store.UpdateLocaleText(ctx, missing, "en", change), work.ErrRowChanged)
			require.ErrorIs(t, store.UpdateTargetLocale(ctx, missing, "ko", change), work.ErrRowChanged)
			require.ErrorIs(t, store.RestoreLocaleText(ctx, missing, "en", text("Title"), nil, time.Now()), work.ErrRowChanged)
			require.ErrorIs(t, store.DeleteTargetLocale(ctx, missing, "ko"), work.ErrRowChanged)
			require.ErrorIs(t, store.SetGroupSortOrder(ctx, missing, missing, 1), work.ErrRowChanged)
			require.ErrorIs(t, store.SetCreditSortOrder(ctx, missing, missing, nil, 1), work.ErrRowChanged)
			require.ErrorIs(t, store.SwitchSourceLocale(ctx, missing, "en", "ko", time.Now()), work.ErrRowChanged)
			_, found, err = store.CreditArtist(ctx, missing)
			require.NoError(t, err)
			require.False(t, found)
			return nil
		})
	},
	"an inserted work reads back with its ID, timestamps and content root": func(t *testing.T, h Harness) {
		value := insert(t, h, func(value *work.Work) {
			value.Slug, value.Featured = text("inserted-"+uuid.NewString()[:8]), true
			value.Metadata = map[string]any{"key": "value"}
		})
		require.NotEmpty(t, value.ID)
		require.False(t, value.CreatedAt.IsZero())
		transact(t, h, func(ctx context.Context, store work.Store) error {
			stored, err := store.FindWork(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, value.ID, stored.ID)
			require.Equal(t, *value.Slug, *stored.Slug)
			require.Equal(t, "WORK_TYPE_PORTFOLIO", stored.Type)
			require.Equal(t, map[string]any{"key": "value"}, stored.Metadata)
			require.True(t, stored.Featured)
			require.True(t, stored.IsPresent)
			require.Equal(t, work.StatusDraft, stored.Status)
			require.Equal(t, value.CreatedAt.Truncate(time.Microsecond).UTC(), stored.CreatedAt.UTC())
			locked, err := store.LockWork(ctx, value.ID, work.ShareLock)
			require.NoError(t, err)
			require.Equal(t, stored.ID, locked.ID)
			root, err := store.LockRoot(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, work.Root{ID: value.ID, Status: work.StatusDraft}, root)
			content, err := store.ContentRoot(ctx, value.ID, work.UpdateLock)
			require.NoError(t, err)
			require.Equal(t, *value.ContentDocumentID, *content.ContentDocumentID)
			require.Equal(t, "en", content.SourceLocale)
			byDocument, err := store.LockDocumentRoot(ctx, uuid.MustParse(*value.ContentDocumentID))
			require.NoError(t, err)
			require.Equal(t, content, byDocument)
			exists, err := store.WorkExists(ctx, value.ID)
			require.NoError(t, err)
			require.True(t, exists)
			sourceLocale, err := store.LockSourceLocale(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, "en", sourceLocale)
			return nil
		})
	},
	"a slug another work holds is taken on insert and update": func(t *testing.T, h Harness) {
		slug := "taken-" + uuid.NewString()[:8]
		first := insert(t, h, func(value *work.Work) { value.Slug = text(slug) })
		second := insert(t, h, nil)
		documentID := h.SeedDocument(t)
		duplicate := work.Work{Type: "WORK_TYPE_PORTFOLIO", Year: 2026, Month: 8, IsPresent: true, SourceLocale: "en", Status: work.StatusDraft, ContentDocumentID: &documentID, Slug: text(slug)}
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store work.Store) error { return store.InsertWork(ctx, &duplicate) })
		require.ErrorIs(t, err, work.ErrSlugTaken)
		err = mutationtest.Transact(t, h.Harness, func(ctx context.Context, store work.Store) error {
			return store.UpdateWork(ctx, second.ID, work.WorkChange{Slug: text(slug), UpdatedAt: time.Now()})
		})
		require.ErrorIs(t, err, work.ErrSlugTaken)
		transact(t, h, func(ctx context.Context, store work.Store) error {
			taken, err := store.SlugTaken(ctx, slug, nil)
			require.NoError(t, err)
			require.True(t, taken)
			taken, err = store.SlugTaken(ctx, slug, &first.ID)
			require.NoError(t, err)
			require.False(t, taken, "a Work's own slug is free for it")
			taken, err = store.SlugTaken(ctx, "free-"+uuid.NewString()[:8], nil)
			require.NoError(t, err)
			require.False(t, taken)
			return nil
		})
	},
	"an update writes the set columns and keeps the rest": func(t *testing.T, h Harness) {
		mapPlace := h.SeedMapPlace(t)
		value := insert(t, h, func(value *work.Work) {
			value.Slug, value.Metadata = text("kept-"+uuid.NewString()[:8]), map[string]any{"old": true}
		})
		now := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
		transact(t, h, func(ctx context.Context, store work.Store) error {
			return store.UpdateWork(ctx, value.ID, work.WorkChange{
				Type: text("WORK_TYPE_ARTICLE"), Metadata: map[string]any{"new": "value"}, Featured: new(true),
				Year: number(2025), Month: number(3), IsPresent: new(false),
				UntilYear: work.OptionalInt32{Set: true, Value: number(2025)}, UntilMonth: work.OptionalInt32{Set: true, Value: number(4)},
				MapPlaceID: &mapPlace, UpdatedAt: now,
			})
		})
		updated := find(t, h, value.ID)
		require.Equal(t, *value.Slug, *updated.Slug)
		require.Equal(t, "WORK_TYPE_ARTICLE", updated.Type)
		require.Equal(t, map[string]any{"new": "value"}, updated.Metadata)
		require.True(t, updated.Featured)
		require.Equal(t, int32(2025), updated.Year)
		require.Equal(t, int32(3), updated.Month)
		require.False(t, updated.IsPresent)
		require.Equal(t, int32(2025), *updated.UntilYear)
		require.Equal(t, int32(4), *updated.UntilMonth)
		require.Equal(t, mapPlace, *updated.MapPlaceID)
		require.Equal(t, now, updated.UpdatedAt.UTC())
		transact(t, h, func(ctx context.Context, store work.Store) error {
			return store.UpdateWork(ctx, value.ID, work.WorkChange{
				ClearSlug: true, ClearPlace: true, IsPresent: new(true),
				UntilYear: work.OptionalInt32{Set: true}, UntilMonth: work.OptionalInt32{Set: true}, UpdatedAt: now,
			})
		})
		cleared := find(t, h, value.ID)
		require.Nil(t, cleared.Slug)
		require.Nil(t, cleared.MapPlaceID)
		require.Nil(t, cleared.UntilYear)
		require.Nil(t, cleared.UntilMonth)
		require.True(t, cleared.IsPresent)
		require.Equal(t, map[string]any{"new": "value"}, cleared.Metadata)
	},
	"status, featured image, stamps and the source locale switch write their columns": func(t *testing.T, h Harness) {
		value := insert(t, h, nil)
		published := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
		transact(t, h, func(ctx context.Context, store work.Store) error {
			require.NoError(t, store.SetStatus(ctx, value.ID, work.StatusPublished, &published, published))
			current, err := store.FeaturedImageFileID(ctx, value.ID)
			require.NoError(t, err)
			require.Nil(t, current)
			require.NoError(t, store.SetFeaturedImage(ctx, value.ID, nil, published))
			return nil
		})
		stored := find(t, h, value.ID)
		require.Equal(t, work.StatusPublished, stored.Status)
		require.Equal(t, published, stored.PublishedAt.UTC())
		archivedAt := published.Add(time.Minute)
		transact(t, h, func(ctx context.Context, store work.Store) error {
			return store.SetStatus(ctx, value.ID, work.StatusArchived, nil, archivedAt)
		})
		stored = find(t, h, value.ID)
		require.Equal(t, work.StatusArchived, stored.Status)
		require.Equal(t, published, stored.PublishedAt.UTC(), "a status write without published_at keeps it")
		require.Equal(t, archivedAt, stored.UpdatedAt.UTC())
		touched := archivedAt.Add(time.Minute)
		transact(t, h, func(ctx context.Context, store work.Store) error { return store.TouchWork(ctx, value.ID, touched) })
		require.Equal(t, touched, find(t, h, value.ID).UpdatedAt.UTC())
		stamped := touched.Add(time.Minute)
		transact(t, h, func(ctx context.Context, store work.Store) error { return store.StampWork(ctx, value.ID, stamped) })
		require.Equal(t, stamped, find(t, h, value.ID).UpdatedAt.UTC())
		transact(t, h, func(ctx context.Context, store work.Store) error {
			require.ErrorIs(t, store.SwitchSourceLocale(ctx, value.ID, "ko", "ja", stamped), work.ErrRowChanged)
			require.NoError(t, store.SwitchSourceLocale(ctx, value.ID, "en", "ko", stamped))
			locale, err := store.SourceLocale(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, "ko", locale)
			return nil
		})
	},
	"the admin list pages by the requested sort with ties by ID and presence filters": func(t *testing.T, h Harness) {
		slug := "listed-" + uuid.NewString()[:8]
		withSlug := insert(t, h, func(value *work.Work) { value.Slug = text(slug) })
		withoutSlug := insert(t, h, nil)
		at := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
		transact(t, h, func(ctx context.Context, store work.Store) error {
			require.NoError(t, store.TouchWork(ctx, withSlug.ID, at))
			return store.TouchWork(ctx, withoutSlug.ID, at)
		})
		transact(t, h, func(ctx context.Context, store work.Store) error {
			sorts := []*commonv1.SortSpec{{Field: "updated_at", Order: commonv1.SortOrder_SORT_ORDER_DESC}}
			works, total, err := store.ListWorks(ctx, work.AdminListPage{Sorts: sorts, Limit: 2})
			require.NoError(t, err)
			require.Equal(t, int64(2), total)
			want := []string{withSlug.ID, withoutSlug.ID}
			if want[1] < want[0] {
				want[0], want[1] = want[1], want[0]
			}
			require.Equal(t, want, []string{works[0].ID, works[1].ID})
			works, total, err = store.ListWorks(ctx, work.AdminListPage{Sorts: sorts, Limit: 1, Offset: 1})
			require.NoError(t, err)
			require.Equal(t, int64(2), total)
			require.Equal(t, []string{want[1]}, []string{works[0].ID})
			works, total, err = store.ListWorks(ctx, work.AdminListPage{
				Filters: []*commonv1.FilterSpec{{Field: "has_slug", Value: "true"}}, Limit: 10,
			})
			require.NoError(t, err)
			require.Equal(t, int64(1), total)
			require.Equal(t, withSlug.ID, works[0].ID)
			works, _, err = store.ListWorks(ctx, work.AdminListPage{
				Filters: []*commonv1.FilterSpec{{Field: "has_featured_image", Value: "true"}}, Limit: 10,
			})
			require.NoError(t, err)
			require.Empty(t, works)
			return nil
		})
	},
	"the admin list searches the source title and filters by status, type and map place": func(t *testing.T, h Harness) {
		mapPlace := h.SeedMapPlace(t)
		titled := func(title string, change func(*work.Work)) work.Work {
			value := insert(t, h, change)
			transact(t, h, func(ctx context.Context, store work.Store) error {
				return store.UpsertSourceLocale(ctx, value.ID, "en", text(title), nil, time.Now())
			})
			return value
		}
		gamma := titled("Listed Gamma", func(value *work.Work) { value.Type, value.MapPlaceID = "WORK_TYPE_ARTICLE", &mapPlace })
		alpha := titled("Listed Alpha", nil)
		beta := titled("Listed Beta", nil)
		titled("Unrelated", nil)
		transact(t, h, func(ctx context.Context, store work.Store) error {
			return store.SetStatus(ctx, alpha.ID, work.StatusPublished, nil, time.Now())
		})
		search := &commonv1.FilterSpec{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "LISTED"}
		list := func(filters ...*commonv1.FilterSpec) ([]string, int64) {
			var ids []string
			var total int64
			transact(t, h, func(ctx context.Context, store work.Store) error {
				works, count, err := store.ListWorks(ctx, work.AdminListPage{
					Filters: append([]*commonv1.FilterSpec{search}, filters...),
					Sorts:   []*commonv1.SortSpec{{Field: "title", Order: commonv1.SortOrder_SORT_ORDER_ASC}}, Limit: 10,
				})
				for _, listed := range works {
					ids = append(ids, listed.ID)
				}
				total = count
				return err
			})
			return ids, total
		}
		ids, total := list()
		require.Equal(t, int64(3), total)
		require.Equal(t, []string{alpha.ID, beta.ID, gamma.ID}, ids)
		ids, _ = list(&commonv1.FilterSpec{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: string(work.StatusPublished)})
		require.Equal(t, []string{alpha.ID}, ids)
		ids, _ = list(&commonv1.FilterSpec{Field: "type", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "WORK_TYPE_ARTICLE"})
		require.Equal(t, []string{gamma.ID}, ids)
		ids, _ = list(&commonv1.FilterSpec{Field: "map_place_id", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: mapPlace})
		require.Equal(t, []string{gamma.ID}, ids)
	},
	"the source locale row reads as the source metadata and document": func(t *testing.T, h Harness) {
		value := insert(t, h, nil)
		empty := insert(t, h, nil)
		now := time.Now().UTC().Truncate(time.Microsecond)
		transact(t, h, func(ctx context.Context, store work.Store) error {
			require.NoError(t, store.InsertTargetLocale(ctx, value.ID, work.LocaleMetadata{Locale: "ko", Title: text("Target"), UpdatedAt: now}))
			require.NoError(t, store.UpsertSourceLocale(ctx, value.ID, "en", text("Source"), text("Summary"), now))
			require.NoError(t, store.SwitchSourceLocale(ctx, empty.ID, "en", "ja", now))
			return nil
		})
		transact(t, h, func(ctx context.Context, store work.Store) error {
			metadata, err := store.SourceMetadata(ctx, []string{value.ID, " " + value.ID + " ", empty.ID, uuid.NewString()})
			require.NoError(t, err)
			require.Equal(t, map[string]work.LocaleMetadata{value.ID: {Locale: "en", Title: text("Source"), Summary: text("Summary")}}, metadata,
				"a Work whose source locale has no row has no source metadata")
			documents, err := store.SourceDocuments(ctx, []string{value.ID})
			require.NoError(t, err)
			require.Equal(t, map[string]work.SourceDocument{value.ID: {Title: text("Source"), Summary: text("Summary")}}, documents)
			none, err := store.SourceMetadata(ctx, nil)
			require.NoError(t, err)
			require.Empty(t, none)
			return nil
		})
	},
	"locale rows store, read, lock and delete their text": func(t *testing.T, h Harness) {
		value := insert(t, h, nil)
		created := time.Now().UTC().Truncate(time.Microsecond)
		transact(t, h, func(ctx context.Context, store work.Store) error {
			require.NoError(t, store.InsertTargetLocale(ctx, value.ID, work.LocaleMetadata{Locale: "ko", Title: text("제목"), UpdatedAt: created}))
			row, found, err := store.LocaleMetadata(ctx, value.ID, "ko", true)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, work.LocaleMetadata{Locale: "ko", Title: text("제목"), UpdatedAt: created}, withUTC(row))
			updated := created.Add(time.Second)
			require.NoError(t, store.UpdateTargetLocale(ctx, value.ID, "ko", work.LocaleTextChange{SetSummary: true, Summary: text("요약"), UpdatedAt: updated}))
			locked, err := store.LockLocaleText(ctx, value.ID, "ko")
			require.NoError(t, err)
			require.Equal(t, "제목", *locked.Title)
			require.Equal(t, "요약", *locked.Summary)
			at, err := store.LocaleUpdatedAt(ctx, value.ID, "ko")
			require.NoError(t, err)
			require.Equal(t, updated, at.UTC())
			require.NoError(t, store.UpdateLocaleText(ctx, value.ID, "en", work.LocaleTextChange{SetTitle: true, Title: textOf("Retitled"), UpdatedAt: updated}))
			source, found, err := store.LockedLocaleText(ctx, value.ID, "en")
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, "Retitled", *source.Title)
			require.Nil(t, source.Summary)
			require.NoError(t, store.RestoreLocaleText(ctx, value.ID, "en", textOf("Restored"), textOf(""), updated))
			source, err = store.LockLocaleText(ctx, value.ID, "en")
			require.NoError(t, err)
			require.Equal(t, "Restored", *source.Title)
			require.Equal(t, "", *source.Summary, "an explicit empty summary stays empty")
			require.NoError(t, store.DeleteTargetLocale(ctx, value.ID, "ko"))
			_, found, err = store.LocaleMetadata(ctx, value.ID, "ko", false)
			require.NoError(t, err)
			require.False(t, found)
			return nil
		})
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store work.Store) error {
			return store.InsertTargetLocale(ctx, value.ID, work.LocaleMetadata{Locale: "en", Title: text("Duplicate"), UpdatedAt: created})
		})
		require.Error(t, err, "a locale row exists once")
	},
	"credit rows read by sort order and write their order, group and role": func(t *testing.T, h Harness) {
		value := insert(t, h, nil)
		other := insert(t, h, nil)
		member := h.SeedMember(t, "Credited "+uuid.NewString()[:8], nil, false)
		artist := h.SeedArtist(t, "Artist "+uuid.NewString()[:8], "")
		var group, otherGroup work.CreditGroup
		var named, memberCredit, artistCredit work.Credit
		transact(t, h, func(ctx context.Context, store work.Store) error {
			group = work.CreditGroup{WorkID: value.ID, Name: "Group"}
			require.NoError(t, store.InsertCreditGroup(ctx, &group))
			otherGroup = work.CreditGroup{WorkID: other.ID, Name: "Other"}
			require.NoError(t, store.InsertCreditGroup(ctx, &otherGroup))
			named = work.Credit{WorkID: value.ID, GroupID: &group.ID, Name: text("Named"), CreditRole: text("Role")}
			require.NoError(t, store.InsertCredit(ctx, &named))
			memberCredit = work.Credit{WorkID: value.ID, MemberID: &member}
			require.NoError(t, store.InsertCredit(ctx, &memberCredit))
			artistCredit = work.Credit{WorkID: value.ID, ArtistID: &artist}
			require.NoError(t, store.InsertCredit(ctx, &artistCredit))
			require.NoError(t, store.SetGroupSortOrder(ctx, value.ID, group.ID, 1))
			require.NoError(t, store.SetCreditSortOrder(ctx, value.ID, named.ID, &group.ID, 2))
			require.NoError(t, store.SetCreditSortOrder(ctx, value.ID, memberCredit.ID, nil, 4))
			require.NoError(t, store.SetCreditSortOrder(ctx, value.ID, artistCredit.ID, nil, 3))
			require.ErrorIs(t, store.SetGroupSortOrder(ctx, value.ID, otherGroup.ID, 5), work.ErrRowChanged, "a group of another Work is not this Work's")
			require.ErrorIs(t, store.SetCreditSortOrder(ctx, other.ID, named.ID, nil, 5), work.ErrRowChanged)
			return nil
		})
		transact(t, h, func(ctx context.Context, store work.Store) error {
			groups, credits, err := store.CreditRows(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, []work.CreditGroup{{ID: group.ID, WorkID: value.ID, Name: "Group", SortOrder: 1}}, groups)
			require.Equal(t, []string{named.ID, artistCredit.ID, memberCredit.ID}, creditIDs(credits))
			require.Equal(t, group.ID, *credits[0].GroupID)
			require.Equal(t, "Role", *credits[0].CreditRole)
			owned, err := store.CreditGroupInWork(ctx, value.ID, group.ID)
			require.NoError(t, err)
			require.True(t, owned)
			owned, err = store.CreditGroupInWork(ctx, value.ID, otherGroup.ID)
			require.NoError(t, err)
			require.False(t, owned)
			for _, id := range []string{group.ID, otherGroup.ID, named.ID} {
				exists, err := store.CreditItemExists(ctx, id)
				require.NoError(t, err)
				require.True(t, exists)
			}
			exists, err := store.CreditItemExists(ctx, uuid.NewString())
			require.NoError(t, err)
			require.False(t, exists)
			_, err = store.LockCreditGroup(ctx, otherGroup.ID, value.ID)
			require.ErrorIs(t, err, work.ErrCreditGroupNotFound, "a group locks only under its Work")
			locked, err := store.LockCreditGroup(ctx, group.ID, value.ID)
			require.NoError(t, err)
			require.Equal(t, "Group", locked.Name)
			_, err = store.LockCredit(ctx, named.ID, other.ID)
			require.ErrorIs(t, err, work.ErrCreditNotFound)
			counts, err := store.CountCredits(ctx, []string{value.ID, other.ID})
			require.NoError(t, err)
			require.Equal(t, map[string]int32{value.ID: 3}, counts)
			require.NoError(t, store.UpdateCredit(ctx, named.ID, false, nil, text("New role")))
			require.NoError(t, store.RenameCreditGroup(ctx, group.ID, "Renamed"))
			require.NoError(t, store.UngroupCredit(ctx, named.ID, value.ID))
			return nil
		})
		transact(t, h, func(ctx context.Context, store work.Store) error {
			credit, err := store.FindCredit(ctx, named.ID)
			require.NoError(t, err)
			require.Nil(t, credit.GroupID)
			require.Equal(t, "New role", *credit.CreditRole)
			renamed, err := store.FindCreditGroup(ctx, group.ID)
			require.NoError(t, err)
			require.Equal(t, "Renamed", renamed.Name)
			require.NoError(t, store.UpdateCredit(ctx, named.ID, true, &group.ID, nil))
			require.NoError(t, store.DeleteCredit(ctx, memberCredit.ID))
			return store.DeleteCreditGroup(ctx, otherGroup.ID)
		})
		transact(t, h, func(ctx context.Context, store work.Store) error {
			credit, err := store.FindCredit(ctx, named.ID)
			require.NoError(t, err)
			require.Equal(t, group.ID, *credit.GroupID)
			_, err = store.FindCredit(ctx, memberCredit.ID)
			require.ErrorIs(t, err, work.ErrCreditNotFound)
			_, err = store.FindCreditGroup(ctx, otherGroup.ID)
			require.ErrorIs(t, err, work.ErrCreditGroupNotFound)
			return nil
		})
	},
	"clients replace in order and read with their rows": func(t *testing.T, h Harness) {
		value := insert(t, h, nil)
		a, b, c := h.SeedClient(t, "A"), h.SeedClient(t, "B"), h.SeedClient(t, "C")
		transact(t, h, func(ctx context.Context, store work.Store) error {
			ids, err := store.ClientIDs(ctx, value.ID)
			require.NoError(t, err)
			require.NotNil(t, ids, "no clients read as an empty list")
			require.Empty(t, ids)
			require.NoError(t, store.ReplaceClients(ctx, value.ID, []string{c, a, b}))
			require.NoError(t, store.ReplaceClients(ctx, value.ID, []string{b, c}))
			ids, err = store.ClientIDs(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, []string{b, c}, ids)
			rows, err := store.Clients(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, []work.ClientRow{{ID: b, Name: "B"}, {ID: c, Name: "C"}}, rows)
			existing, err := store.CountExistingClients(ctx, []string{a, b, uuid.NewString()})
			require.NoError(t, err)
			require.Equal(t, int64(2), existing)
			counts, err := store.CountClients(ctx, []string{value.ID})
			require.NoError(t, err)
			require.Equal(t, map[string]int32{value.ID: 2}, counts)
			return nil
		})
	},
	"versions list newest first and the latest locks": func(t *testing.T, h Harness) {
		value := insert(t, h, nil)
		member := h.SeedMember(t, "Versioner "+uuid.NewString()[:8], nil, false)
		tombstone := h.SeedMember(t, "Gone "+uuid.NewString()[:8], nil, true)
		snapshot := []byte(`{"schemaVersion":1,"sourceLocale":"en","title":"Title","summary":null,"document":{}}`)
		created := time.Now().UTC().Truncate(time.Microsecond)
		var first, second work.Version
		transact(t, h, func(ctx context.Context, store work.Store) error {
			first = work.Version{WorkID: value.ID, Version: 1, Title: text("Title"), ContentSnapshot: snapshot, ContributorMemberIDs: []string{member}, CreatedAt: created}
			require.NoError(t, store.InsertVersion(ctx, &first))
			second = work.Version{WorkID: value.ID, Version: 2, ContentSnapshot: snapshot, ContributorMemberIDs: []string{}, CreatedAt: created}
			return store.InsertVersion(ctx, &second)
		})
		transact(t, h, func(ctx context.Context, store work.Store) error {
			count, err := store.CountVersions(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, int64(2), count)
			versions, err := store.ListVersions(ctx, value.ID, 1, 0)
			require.NoError(t, err)
			require.Equal(t, []string{second.ID}, []string{versions[0].ID})
			versions, err = store.ListVersions(ctx, value.ID, 5, 1)
			require.NoError(t, err)
			require.Len(t, versions, 1)
			require.Equal(t, first.ID, versions[0].ID)
			require.Equal(t, []string{member}, versions[0].ContributorMemberIDs)
			require.Equal(t, "Title", *versions[0].Title)
			require.JSONEq(t, string(snapshot), string(versions[0].ContentSnapshot))
			latest, found, err := store.LockLatestVersion(ctx, value.ID)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, int32(2), latest.Version)
			found2, err := store.FindVersion(ctx, first.ID)
			require.NoError(t, err)
			require.Equal(t, value.ID, found2.WorkID)
			names, err := store.ContributorNicknames(ctx, []string{member, tombstone, uuid.NewString()})
			require.NoError(t, err)
			require.Len(t, names, 2, "a deleted Member keeps its nickname; a missing one has none")
			return nil
		})
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store work.Store) error {
			duplicate := work.Version{WorkID: value.ID, Version: 2, ContentSnapshot: snapshot, ContributorMemberIDs: []string{}, CreatedAt: created}
			return store.InsertVersion(ctx, &duplicate)
		})
		require.Error(t, err, "a version number exists once per Work")
	},
	"credited works list the Member's credits and its Artists' credits": func(t *testing.T, h Harness) {
		member := h.SeedMember(t, "Owner "+uuid.NewString()[:8], nil, false)
		stranger := h.SeedMember(t, "Stranger "+uuid.NewString()[:8], nil, false)
		owned := h.SeedArtist(t, "Owned "+uuid.NewString()[:8], member)
		older := insert(t, h, nil)
		newer := insert(t, h, nil)
		var memberCredit, artistCredit work.Credit
		transact(t, h, func(ctx context.Context, store work.Store) error {
			memberCredit = work.Credit{WorkID: older.ID, MemberID: &member}
			require.NoError(t, store.InsertCredit(ctx, &memberCredit))
			artistCredit = work.Credit{WorkID: newer.ID, ArtistID: &owned}
			require.NoError(t, store.InsertCredit(ctx, &artistCredit))
			strangerCredit := work.Credit{WorkID: newer.ID, MemberID: &stranger}
			return store.InsertCredit(ctx, &strangerCredit)
		})
		transact(t, h, func(ctx context.Context, store work.Store) error {
			artists, err := store.CreditingArtistIDs(ctx, member)
			require.NoError(t, err)
			require.Equal(t, []string{owned}, artists)
			rows, total, err := store.ListCreditedWorks(ctx, work.CreditedPage{MemberID: member, ArtistIDs: artists, Limit: 10})
			require.NoError(t, err)
			require.Equal(t, int64(2), total)
			want := []string{artistCredit.ID, memberCredit.ID}
			if !newer.CreatedAt.After(older.CreatedAt) {
				want = []string{memberCredit.ID, artistCredit.ID}
				if older.CreatedAt.Equal(newer.CreatedAt) && artistCredit.ID > memberCredit.ID {
					want = []string{artistCredit.ID, memberCredit.ID}
				}
			}
			require.Equal(t, want, []string{rows[0].CreditID, rows[1].CreditID})
			for _, row := range rows {
				require.Equal(t, "en", row.SourceLocale)
				require.Equal(t, "Source "+row.WorkID[:8], row.Title)
			}
			rows, total, err = store.ListCreditedWorks(ctx, work.CreditedPage{MemberID: member, Limit: 10})
			require.NoError(t, err)
			require.Equal(t, int64(1), total)
			require.Equal(t, memberCredit.ID, rows[0].CreditID)
			return nil
		})
	},
	"a member's preferred locale, Map Places and share links read": func(t *testing.T, h Harness) {
		member := h.SeedMember(t, "Preferring "+uuid.NewString()[:8], text("ko"), false)
		deleted := h.SeedMember(t, "Deleted "+uuid.NewString()[:8], nil, true)
		mapPlace := h.SeedMapPlace(t)
		transact(t, h, func(ctx context.Context, store work.Store) error {
			preferred, err := store.PreferredLocale(ctx, member)
			require.NoError(t, err)
			require.Equal(t, "ko", *preferred)
			preferred, err = store.PreferredLocale(ctx, deleted)
			require.NoError(t, err)
			require.Nil(t, preferred)
			exists, err := store.MapPlaceExists(ctx, mapPlace)
			require.NoError(t, err)
			require.True(t, exists)
			exists, err = store.MapPlaceExists(ctx, uuid.NewString())
			require.NoError(t, err)
			require.False(t, exists)
			locked, err := store.LockContributors(ctx, []string{member, deleted, uuid.NewString()})
			require.NoError(t, err)
			require.ElementsMatch(t, []string{member, deleted}, locked, "a contributor may be a deleted Member")
			return nil
		})
	},
	"a deleted work leaves none of its rows": func(t *testing.T, h Harness) {
		value := insert(t, h, nil)
		client := h.SeedClient(t, "Client")
		transact(t, h, func(ctx context.Context, store work.Store) error {
			group := work.CreditGroup{WorkID: value.ID, Name: "Group"}
			require.NoError(t, store.InsertCreditGroup(ctx, &group))
			credit := work.Credit{WorkID: value.ID, Name: text("Named")}
			require.NoError(t, store.InsertCredit(ctx, &credit))
			return store.ReplaceClients(ctx, value.ID, []string{client})
		})
		transact(t, h, func(ctx context.Context, store work.Store) error { return store.DeleteWork(ctx, value.ID) })
		transact(t, h, func(ctx context.Context, store work.Store) error {
			_, err := store.FindWork(ctx, value.ID)
			require.ErrorIs(t, err, work.ErrWorkNotFound)
			groups, credits, err := store.CreditRows(ctx, value.ID)
			require.NoError(t, err)
			require.Empty(t, groups)
			require.Empty(t, credits)
			ids, err := store.ClientIDs(ctx, value.ID)
			require.NoError(t, err)
			require.Empty(t, ids)
			_, found, err := store.LocaleMetadata(ctx, value.ID, "en", false)
			require.NoError(t, err)
			require.False(t, found)
			return nil
		})
	},
	"a failed transaction stores nothing": func(t *testing.T, h Harness) {
		documentID := h.SeedDocument(t)
		value := work.Work{Type: "WORK_TYPE_PORTFOLIO", Year: 2026, Month: 8, IsPresent: true, SourceLocale: "en", Status: work.StatusDraft, ContentDocumentID: &documentID}
		failure := errors.New("rolled back")
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store work.Store) error {
			if err := store.InsertWork(ctx, &value); err != nil {
				return err
			}
			return failure
		})
		require.ErrorIs(t, err, failure)
		transact(t, h, func(ctx context.Context, store work.Store) error {
			exists, err := store.WorkExists(ctx, value.ID)
			require.NoError(t, err)
			require.False(t, exists)
			return nil
		})
	},
	"each row lock makes a second writer wait for the first commit": func(t *testing.T, h Harness) {
		value := insert(t, h, nil)
		documentID := uuid.MustParse(*value.ContentDocumentID)
		var group work.CreditGroup
		var credit work.Credit
		transact(t, h, func(ctx context.Context, store work.Store) error {
			group = work.CreditGroup{WorkID: value.ID, Name: "Group"}
			require.NoError(t, store.InsertCreditGroup(ctx, &group))
			credit = work.Credit{WorkID: value.ID, Name: text("Named")}
			require.NoError(t, store.InsertCredit(ctx, &credit))
			version := work.Version{WorkID: value.ID, Version: 1, ContentSnapshot: []byte(`{"schemaVersion":1,"sourceLocale":"en","title":null,"summary":null,"document":{}}`), ContributorMemberIDs: []string{}, CreatedAt: time.Now().UTC()}
			return store.InsertVersion(ctx, &version)
		})
		locks := map[string]func(ctx context.Context, store work.Store) error{
			"LockRoot": func(ctx context.Context, store work.Store) error { _, err := store.LockRoot(ctx, value.ID); return err },
			"LockWork": func(ctx context.Context, store work.Store) error {
				_, err := store.LockWork(ctx, value.ID, work.UpdateLock)
				return err
			},
			"ContentRoot": func(ctx context.Context, store work.Store) error {
				_, err := store.ContentRoot(ctx, value.ID, work.UpdateLock)
				return err
			},
			"LockDocumentRoot": func(ctx context.Context, store work.Store) error {
				_, err := store.LockDocumentRoot(ctx, documentID)
				return err
			},
			"LockSourceLocale": func(ctx context.Context, store work.Store) error {
				_, err := store.LockSourceLocale(ctx, value.ID)
				return err
			},
			"LockLocaleText": func(ctx context.Context, store work.Store) error {
				_, err := store.LockLocaleText(ctx, value.ID, "en")
				return err
			},
			"LockedLocaleText": func(ctx context.Context, store work.Store) error {
				_, _, err := store.LockedLocaleText(ctx, value.ID, "en")
				return err
			},
			"LocaleMetadata": func(ctx context.Context, store work.Store) error {
				_, _, err := store.LocaleMetadata(ctx, value.ID, "en", true)
				return err
			},
			"LockCreditGroup": func(ctx context.Context, store work.Store) error {
				_, err := store.LockCreditGroup(ctx, group.ID, value.ID)
				return err
			},
			"LockCredit": func(ctx context.Context, store work.Store) error {
				_, err := store.LockCredit(ctx, credit.ID, value.ID)
				return err
			},
			"LockLatestVersion": func(ctx context.Context, store work.Store) error {
				_, _, err := store.LockLatestVersion(ctx, value.ID)
				return err
			},
		}
		for name, lock := range locks {
			t.Run(name, func(t *testing.T) {
				var waited atomic.Bool
				err := mutationtest.RequireSecondWaits(t, h.Harness, lock,
					func(context.Context, work.Store) error { waited.Store(true); return nil },
					func(ctx context.Context, store work.Store) error {
						if err := lock(ctx, store); err != nil {
							return err
						}
						if !waited.Load() {
							return errors.New("the second lock did not wait for the first commit")
						}
						return nil
					},
				)
				require.NoError(t, err)
			})
		}
	},
}

func textOf(value string) *string { return &value }

func withUTC(row work.LocaleMetadata) work.LocaleMetadata {
	row.UpdatedAt = row.UpdatedAt.UTC()
	return row
}

func creditIDs(credits []work.Credit) []string {
	ids := make([]string, 0, len(credits))
	for _, credit := range credits {
		ids = append(ids, credit.ID)
	}
	return ids
}
