package programeventtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/programevent"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// Harness is one programevent.Store implementation the contract runs
// against, with the rows of other domains a case needs.
type Harness struct {
	mutationtest.Harness[programevent.Store]
	// SeedRelated stores an artist, label or client and returns its ID.
	SeedRelated func(t *testing.T, kind programevent.RelationKind) string
	// SeedFile stores a File and returns its ID.
	SeedFile func(t *testing.T) string
	// SeedMember stores a Member and returns its ID.
	SeedMember func(t *testing.T) string
}

// FakeHarness is the Harness of the fake Store.
func FakeHarness() Harness {
	store := NewStore()
	return Harness{
		Harness:     mutationtest.FakeHarness[programevent.Store](store),
		SeedRelated: func(*testing.T, programevent.RelationKind) string { return uuid.NewString() },
		SeedFile:    func(*testing.T) string { return uuid.NewString() },
		SeedMember: func(*testing.T) string {
			id := uuid.NewString()
			store.PutMember(id)
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

func transact(t *testing.T, h Harness, work func(ctx context.Context, store programevent.Store) error) {
	t.Helper()
	require.NoError(t, mutationtest.Transact(t, h.Harness, work))
}

func text(value string) *string { return &value }

// at is a fixed instant at the precision PostgreSQL stores.
func at(minutes int) time.Time {
	return time.Date(2026, time.October, 1, 10, 0, 0, 0, time.UTC).Add(time.Duration(minutes) * time.Minute)
}

// insertType stores an active type.
func insertType(t *testing.T, h Harness, slug string) programevent.Type {
	t.Helper()
	eventType := programevent.Type{Slug: slug, Status: programevent.TypeStatusActive, CreatedAt: at(0), UpdatedAt: at(0)}
	transact(t, h, func(ctx context.Context, store programevent.Store) error { return store.InsertType(ctx, &eventType) })
	return eventType
}

// insertEvent stores a draft online event of a new type, with its content
// document and source locale.
func insertEvent(t *testing.T, h Harness, change func(*programevent.Event)) programevent.Event {
	t.Helper()
	eventType := insertType(t, h, "type-"+uuid.NewString())
	event := programevent.Event{
		Title: "Event", Slug: "event-" + uuid.NewString(), Status: programevent.StatusDraft, SourceLocale: "en",
		TypeID: eventType.ID, StartsAt: at(60), Timezone: "UTC", LocationMode: programevent.LocationModeOnline,
		CreatedAt: at(0), UpdatedAt: at(0),
	}
	if change != nil {
		change(&event)
	}
	transact(t, h, func(ctx context.Context, store programevent.Store) error {
		document, err := store.CreateContentDocument(ctx, contentblock.CreateInput{Profile: "program_event", SourceLocale: event.SourceLocale})
		if err != nil {
			return err
		}
		documentID := document.Document.ID.String()
		event.ContentDocumentID = &documentID
		if err := store.InsertEvent(ctx, &event); err != nil {
			return err
		}
		return store.InsertSourceLocale(ctx, event.ID, event.SourceLocale, text("Source summary"), at(0))
	})
	return event
}

func insertMedia(t *testing.T, h Harness, eventID, role string, sortOrder int32, primary bool) programevent.Media {
	t.Helper()
	media := programevent.Media{EventID: eventID, FileID: h.SeedFile(t), Role: role, SortOrder: sortOrder, IsPrimary: primary}
	transact(t, h, func(ctx context.Context, store programevent.Store) error {
		media.CreatedAt, media.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond), time.Now().UTC().Truncate(time.Microsecond)
		return store.InsertMedia(ctx, &media)
	})
	return media
}

func insertCredit(t *testing.T, h Harness, eventID, name string, sortOrder int32) programevent.CreditRecord {
	t.Helper()
	credit := programevent.CreditRecord{EventID: eventID, DisplayName: text(name), SortOrder: sortOrder, CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	transact(t, h, func(ctx context.Context, store programevent.Store) error { return store.InsertCredit(ctx, &credit) })
	return credit
}

// requireWaits checks that lock in a first transaction makes second wait
// until the first commits.
func requireWaits(t *testing.T, h Harness, lock, second func(ctx context.Context, store programevent.Store) error) {
	t.Helper()
	require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, lock,
		func(context.Context, programevent.Store) error { return nil }, second))
}

var storeContract = map[string]func(t *testing.T, h Harness){
	"a missing event answers its sentinel": func(t *testing.T, h Harness) {
		missing := uuid.NewString()
		transact(t, h, func(ctx context.Context, store programevent.Store) error {
			_, err := store.Event(ctx, missing, programevent.NoLock)
			require.ErrorIs(t, err, programevent.ErrEventNotFound)
			require.ErrorIs(t, store.RequireEvent(ctx, missing), programevent.ErrEventNotFound)
			require.ErrorIs(t, store.LockEventID(ctx, missing), programevent.ErrEventNotFound)
			_, err = store.EventStatus(ctx, missing, programevent.ShareLock)
			require.ErrorIs(t, err, programevent.ErrEventNotFound)
			_, err = store.LockLifecycle(ctx, missing)
			require.ErrorIs(t, err, programevent.ErrEventNotFound)
			_, err = store.ContentDocumentID(ctx, missing, programevent.UpdateLock)
			require.ErrorIs(t, err, programevent.ErrEventNotFound)
			_, err = store.SourceLocale(ctx, missing, programevent.NoLock)
			require.ErrorIs(t, err, programevent.ErrEventNotFound)
			_, err = store.DocumentRoot(ctx, missing, programevent.UpdateLock)
			require.ErrorIs(t, err, programevent.ErrEventNotFound)
			_, _, err = store.LockTitle(ctx, missing)
			require.ErrorIs(t, err, programevent.ErrEventNotFound)
			_, err = store.Title(ctx, missing)
			require.ErrorIs(t, err, programevent.ErrEventNotFound)
			_, err = store.EventUpdatedAt(ctx, missing)
			require.ErrorIs(t, err, programevent.ErrEventNotFound)
			deleted, err := store.DeleteEvent(ctx, missing)
			require.NoError(t, err)
			require.False(t, deleted)
			return nil
		})
	},
	"an inserted event reads back with its document, lifecycle and source": func(t *testing.T, h Harness) {
		event := insertEvent(t, h, func(event *programevent.Event) {
			event.EndsAt, event.TicketURL = timePtr(at(120)), text("https://tickets.example.com")
		})
		require.NotEmpty(t, event.ID)
		transact(t, h, func(ctx context.Context, store programevent.Store) error {
			stored, err := store.Event(ctx, event.ID, programevent.ShareLock)
			require.NoError(t, err)
			require.Equal(t, event.Slug, stored.Slug)
			require.Equal(t, at(60), stored.StartsAt.UTC())
			require.Equal(t, at(120), stored.EndsAt.UTC())
			require.Equal(t, "https://tickets.example.com", *stored.TicketURL)
			require.Nil(t, stored.PublishedAt)
			status, err := store.EventStatus(ctx, event.ID, programevent.UpdateLock)
			require.NoError(t, err)
			require.Equal(t, programevent.StatusDraft, status)
			reference, err := store.ContentDocumentID(ctx, event.ID, programevent.NoLock)
			require.NoError(t, err)
			require.Equal(t, *event.ContentDocumentID, *reference)
			sourceLocale, err := store.SourceLocale(ctx, event.ID, programevent.UpdateLock)
			require.NoError(t, err)
			require.Equal(t, "en", sourceLocale)
			root, err := store.DocumentRoot(ctx, event.ID, programevent.ShareLock)
			require.NoError(t, err)
			require.Equal(t, programevent.DocumentRoot{ID: event.ID, Title: "Event", ContentDocumentID: event.ContentDocumentID, Status: programevent.StatusDraft, SourceLocale: "en"}, root)
			title, sourceLocale, err := store.LockTitle(ctx, event.ID)
			require.NoError(t, err)
			require.Equal(t, "Event", title)
			require.Equal(t, "en", sourceLocale)
			require.NoError(t, store.RequireEvent(ctx, event.ID))
			return nil
		})
	},
	"a slug another event holds is taken": func(t *testing.T, h Harness) {
		first := insertEvent(t, h, nil)
		second := insertEvent(t, h, nil)
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store programevent.Store) error {
			document, err := store.CreateContentDocument(ctx, contentblock.CreateInput{Profile: "program_event", SourceLocale: "en"})
			require.NoError(t, err)
			documentID := document.Document.ID.String()
			duplicate := programevent.Event{
				Title: "Copy", Slug: first.Slug, Status: programevent.StatusDraft, SourceLocale: "en", TypeID: first.TypeID,
				StartsAt: at(0), Timezone: "UTC", LocationMode: programevent.LocationModeTBA, ContentDocumentID: &documentID,
				CreatedAt: at(0), UpdatedAt: at(0),
			}
			return store.InsertEvent(ctx, &duplicate)
		})
		require.ErrorIs(t, err, programevent.ErrSlugTaken)
		err = mutationtest.Transact(t, h.Harness, func(ctx context.Context, store programevent.Store) error {
			return store.UpdateSettings(ctx, second.ID, programevent.SettingsUpdate{Slug: &first.Slug, UpdatedAt: at(5)})
		})
		require.ErrorIs(t, err, programevent.ErrSlugTaken)
	},
	"settings write the fields an update sets and clear the nullable ones": func(t *testing.T, h Harness) {
		series := programevent.Series{Slug: "series-" + uuid.NewString(), Status: programevent.StatusDraft, Title: "Series", CreatedAt: at(0), UpdatedAt: at(0)}
		transact(t, h, func(ctx context.Context, store programevent.Store) error { return store.InsertSeries(ctx, &series) })
		event := insertEvent(t, h, func(event *programevent.Event) {
			event.EndsAt, event.ExternalURL, event.SeriesID, event.SeriesOrder = timePtr(at(90)), text("https://example.com"), &series.ID, int32Ptr(2)
		})
		starts, mode, slug := at(30), programevent.LocationModeTBA, "renamed-"+uuid.NewString()
		transact(t, h, func(ctx context.Context, store programevent.Store) error {
			return store.UpdateSettings(ctx, event.ID, programevent.SettingsUpdate{
				StartsAt: &starts, EndsAt: programevent.Nullable[time.Time]{Set: true}, LocationMode: &mode, Slug: &slug,
				ExternalURL: programevent.Nullable[string]{Set: true}, StreamURL: programevent.Nullable[string]{Set: true, Value: text("https://stream.example.com")},
				SeriesID: programevent.Nullable[string]{Set: true}, SeriesOrder: programevent.Nullable[int32]{Set: true},
				UpdatedAt: at(10),
			})
		})
		transact(t, h, func(ctx context.Context, store programevent.Store) error {
			stored, err := store.Event(ctx, event.ID, programevent.NoLock)
			require.NoError(t, err)
			require.Equal(t, at(30), stored.StartsAt.UTC())
			require.Nil(t, stored.EndsAt)
			require.Nil(t, stored.ExternalURL)
			require.Nil(t, stored.SeriesID)
			require.Nil(t, stored.SeriesOrder)
			require.Equal(t, "https://stream.example.com", *stored.StreamURL)
			require.Equal(t, programevent.LocationModeTBA, stored.LocationMode)
			require.Equal(t, slug, stored.Slug)
			require.Equal(t, "UTC", stored.Timezone)
			require.Equal(t, at(10), stored.UpdatedAt.UTC())
			updatedAt, err := store.EventUpdatedAt(ctx, event.ID)
			require.NoError(t, err)
			require.Equal(t, at(10), updatedAt.UTC())
			require.NoError(t, store.TouchEvent(ctx, event.ID, at(20)))
			updatedAt, err = store.EventUpdatedAt(ctx, event.ID)
			require.NoError(t, err)
			require.Equal(t, at(20), updatedAt.UTC())
			return nil
		})
	},
	"a lifecycle change keeps the publication date it does not set": func(t *testing.T, h Harness) {
		event := insertEvent(t, h, nil)
		transact(t, h, func(ctx context.Context, store programevent.Store) error {
			published, err := store.SetLifecycle(ctx, event.ID, programevent.StatusPublished, timePtr(at(5)), at(5))
			require.NoError(t, err)
			require.Equal(t, programevent.StatusPublished, published.Status)
			require.Equal(t, at(5), published.PublishedAt.UTC())
			archived, err := store.SetLifecycle(ctx, event.ID, programevent.StatusArchived, nil, at(6))
			require.NoError(t, err)
			require.Equal(t, programevent.StatusArchived, archived.Status)
			require.Equal(t, at(5), archived.PublishedAt.UTC())
			require.Equal(t, at(6), archived.UpdatedAt.UTC())
			locked, err := store.LockLifecycle(ctx, event.ID)
			require.NoError(t, err)
			require.Equal(t, archived.ID, locked.ID)
			require.Equal(t, programevent.StatusArchived, locked.Status)
			return nil
		})
	},
	"events list by status, newest start first, and count every match": func(t *testing.T, h Harness) {
		early := insertEvent(t, h, func(event *programevent.Event) { event.StartsAt = at(10) })
		late := insertEvent(t, h, func(event *programevent.Event) { event.StartsAt = at(20) })
		published := insertEvent(t, h, nil)
		transact(t, h, func(ctx context.Context, store programevent.Store) error {
			_, err := store.SetLifecycle(ctx, published.ID, programevent.StatusPublished, timePtr(at(1)), at(1))
			return err
		})
		transact(t, h, func(ctx context.Context, store programevent.Store) error {
			draft := []*commonv1.FilterSpec{{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: string(programevent.StatusDraft)}}
			events, total, err := store.ListEvents(ctx, programevent.ListPage{Filters: draft, Limit: 1})
			require.NoError(t, err)
			require.EqualValues(t, 2, total)
			require.Equal(t, []string{late.ID}, eventIDs(events))
			events, _, err = store.ListEvents(ctx, programevent.ListPage{Filters: draft, Limit: 5, Offset: 1})
			require.NoError(t, err)
			require.Equal(t, []string{early.ID}, eventIDs(events))
			return nil
		})
	},
	"a deleted event takes its child rows": func(t *testing.T, h Harness) {
		event := insertEvent(t, h, nil)
		insertMedia(t, h, event.ID, "poster", 0, true)
		insertCredit(t, h, event.ID, "Crew", 0)
		transact(t, h, func(ctx context.Context, store programevent.Store) error {
			deleted, err := store.DeleteEvent(ctx, event.ID)
			require.NoError(t, err)
			require.True(t, deleted)
			media, err := store.EventMedia(ctx, event.ID)
			require.NoError(t, err)
			require.Empty(t, media)
			credits, err := store.Credits(ctx, event.ID)
			require.NoError(t, err)
			require.Empty(t, credits)
			_, found, err := store.LocaleMetadata(ctx, event.ID, "en", false)
			require.NoError(t, err)
			require.False(t, found)
			return nil
		})
	},
	"locales keep their rows, counts and order": func(t *testing.T, h Harness) {
		event := insertEvent(t, h, func(event *programevent.Event) { event.SourceLocale = "ko" })
		transact(t, h, func(ctx context.Context, store programevent.Store) error {
			created, err := store.InsertTargetLocale(ctx, event.ID, "en", text("Target"), at(1))
			require.NoError(t, err)
			require.EqualValues(t, 1, created)
			created, err = store.InsertTargetLocale(ctx, event.ID, "ja", nil, at(1))
			require.NoError(t, err)
			require.EqualValues(t, 1, created)
			summaries, err := store.LocaleSummaries(ctx, event.ID)
			require.NoError(t, err)
			require.Equal(t, []programevent.LocaleSummary{
				{Locale: "ko", Summary: text("Source summary")}, {Locale: "en", Summary: text("Target")}, {Locale: "ja"},
			}, summaries)
			updated, err := store.UpdateTargetLocale(ctx, event.ID, "en", text("Edited"), at(2))
			require.NoError(t, err)
			require.EqualValues(t, 1, updated)
			row, found, err := store.LocaleMetadata(ctx, event.ID, "en", true)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, "Edited", *row.Summary)
			require.Equal(t, at(2), row.UpdatedAt.UTC())
			summary, found, err := store.LocaleSummary(ctx, event.ID, "ja")
			require.NoError(t, err)
			require.True(t, found)
			require.Nil(t, summary)
			deleted, err := store.DeleteTargetLocale(ctx, event.ID, "ja")
			require.NoError(t, err)
			require.EqualValues(t, 1, deleted)
			deleted, err = store.DeleteTargetLocale(ctx, event.ID, "ja")
			require.NoError(t, err)
			require.Zero(t, deleted)
			updated, err = store.UpdateTargetLocale(ctx, event.ID, "ja", nil, at(3))
			require.NoError(t, err)
			require.Zero(t, updated)
			_, found, err = store.LocaleSummary(ctx, event.ID, "ja")
			require.NoError(t, err)
			require.False(t, found)
			_, err = store.LockLocale(ctx, event.ID, "ja")
			require.ErrorIs(t, err, programevent.ErrLocaleNotFound)
			return nil
		})
	},
	"the source locale holds the translation source and its summary": func(t *testing.T, h Harness) {
		event := insertEvent(t, h, nil)
		transact(t, h, func(ctx context.Context, store programevent.Store) error {
			title, summary, found, err := store.TranslationSource(ctx, event.ID, "en")
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, "Event", title)
			require.Equal(t, "Source summary", *summary)
			_, _, found, err = store.TranslationSource(ctx, event.ID, "ko")
			require.NoError(t, err)
			require.False(t, found)
			require.NoError(t, store.SetTitle(ctx, event.ID, "Renamed", at(3)))
			require.NoError(t, store.SetLocaleSummary(ctx, event.ID, "en", nil, at(3)))
			title, err = store.Title(ctx, event.ID)
			require.NoError(t, err)
			require.Equal(t, "Renamed", title)
			locked, err := store.LockLocale(ctx, event.ID, "en")
			require.NoError(t, err)
			require.Nil(t, locked.Summary)
			require.Equal(t, at(3), locked.UpdatedAt.UTC())
			return nil
		})
	},
	"media of a role order by position and the primary leads": func(t *testing.T, h Harness) {
		event := insertEvent(t, h, nil)
		first := insertMedia(t, h, event.ID, "gallery", 1, false)
		second := insertMedia(t, h, event.ID, "gallery", 0, false)
		poster := insertMedia(t, h, event.ID, "poster", 0, true)
		transact(t, h, func(ctx context.Context, store programevent.Store) error {
			roleMedia, err := store.RoleMedia(ctx, event.ID, "gallery")
			require.NoError(t, err)
			require.Equal(t, []string{second.ID, first.ID}, mediaIDs(roleMedia))
			primary, err := store.PrimaryMediaFileID(ctx, event.ID, "gallery")
			require.NoError(t, err)
			require.Equal(t, second.FileID, primary)
			has, err := store.HasPrimaryMedia(ctx, event.ID, "gallery")
			require.NoError(t, err)
			require.False(t, has)
			require.NoError(t, store.MarkMediaPrimary(ctx, first.ID, at(4)))
			primary, err = store.PrimaryMediaFileID(ctx, event.ID, "gallery")
			require.NoError(t, err)
			require.Equal(t, first.FileID, primary)
			all, err := store.EventMedia(ctx, event.ID)
			require.NoError(t, err)
			require.Equal(t, []string{first.ID, second.ID, poster.ID}, mediaIDs(all))
			posters, err := store.PosterFileIDs(ctx, []string{event.ID, uuid.NewString()})
			require.NoError(t, err)
			require.Equal(t, map[string]string{event.ID: poster.FileID}, posters)
			next, err := store.NextMediaSortOrder(ctx, event.ID, "gallery")
			require.NoError(t, err)
			require.EqualValues(t, 2, next)
			none, err := store.PrimaryMediaFileID(ctx, event.ID, "venue")
			require.NoError(t, err)
			require.Empty(t, none)
			byFile, found, err := store.MediaOfFile(ctx, event.ID, "poster", poster.FileID)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, poster.ID, byFile.ID)
			_, found, err = store.MediaOfFile(ctx, event.ID, "gallery", poster.FileID)
			require.NoError(t, err)
			require.False(t, found)
			return nil
		})
	},
	"a role holds one primary relation": func(t *testing.T, h Harness) {
		event := insertEvent(t, h, nil)
		insertMedia(t, h, event.ID, "gallery", 0, true)
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store programevent.Store) error {
			media := programevent.Media{EventID: event.ID, FileID: h.SeedFile(t), Role: "gallery", SortOrder: 1, IsPrimary: true, CreatedAt: at(1), UpdatedAt: at(1)}
			return store.InsertMedia(ctx, &media)
		})
		require.Error(t, err)
	},
	"media edits keep the relation and its role": func(t *testing.T, h Harness) {
		event := insertEvent(t, h, nil)
		first := insertMedia(t, h, event.ID, "gallery", 0, true)
		second := insertMedia(t, h, event.ID, "gallery", 1, false)
		other := insertMedia(t, h, event.ID, "venue", 0, true)
		transact(t, h, func(ctx context.Context, store programevent.Store) error {
			require.NoError(t, store.ClearPrimaryMedia(ctx, event.ID, "gallery", at(5)))
			has, err := store.HasPrimaryMedia(ctx, event.ID, "gallery")
			require.NoError(t, err)
			require.False(t, has)
			require.NoError(t, store.UpdateMedia(ctx, second.ID, text("alt"), nil, true, at(6)))
			stored, err := store.MediaByID(ctx, event.ID, second.ID)
			require.NoError(t, err)
			require.True(t, stored.IsPrimary)
			require.Equal(t, "alt", *stored.Alt)
			require.Equal(t, at(6), stored.UpdatedAt.UTC())
			require.NoError(t, store.ClearPrimaryMedia(ctx, event.ID, "gallery", at(7)))
			placed, err := store.PlaceMedia(ctx, event.ID, "gallery", second.ID, 0, at(7))
			require.NoError(t, err)
			require.True(t, placed)
			placed, err = store.PlaceMedia(ctx, event.ID, "gallery", first.ID, 1, at(7))
			require.NoError(t, err)
			require.True(t, placed)
			placed, err = store.PlaceMedia(ctx, event.ID, "gallery", other.ID, 2, at(7))
			require.NoError(t, err)
			require.False(t, placed, "a relation of another role is not placed")
			firstMedia, found, err := store.FirstMedia(ctx, event.ID, "gallery")
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, second.ID, firstMedia.ID)
			deleted, err := store.DeleteMedia(ctx, uuid.NewString(), second.ID)
			require.NoError(t, err)
			require.False(t, deleted)
			_, err = store.MediaByID(ctx, uuid.NewString(), second.ID)
			require.ErrorIs(t, err, programevent.ErrMediaNotFound)
			deleted, err = store.DeleteMedia(ctx, event.ID, second.ID)
			require.NoError(t, err)
			require.True(t, deleted)
			removed, err := store.DeleteRoleMedia(ctx, event.ID, "gallery")
			require.NoError(t, err)
			require.Equal(t, []string{first.ID}, mediaIDs(removed))
			remaining, err := store.EventMedia(ctx, event.ID)
			require.NoError(t, err)
			require.Equal(t, []string{other.ID}, mediaIDs(remaining))
			return nil
		})
	},
	"credits keep their positions and count only the event's own": func(t *testing.T, h Harness) {
		event := insertEvent(t, h, nil)
		other := insertEvent(t, h, nil)
		second := insertCredit(t, h, event.ID, "Second", 1)
		first := insertCredit(t, h, event.ID, "First", 0)
		foreign := insertCredit(t, h, other.ID, "Other", 0)
		transact(t, h, func(ctx context.Context, store programevent.Store) error {
			credits, err := store.Credits(ctx, event.ID)
			require.NoError(t, err)
			require.Equal(t, []string{first.ID, second.ID}, creditIDs(credits))
			locked, err := store.LockCredits(ctx, event.ID)
			require.NoError(t, err)
			require.Equal(t, creditIDs(credits), creditIDs(locked))
			next, err := store.NextCreditSortOrder(ctx, event.ID)
			require.NoError(t, err)
			require.EqualValues(t, 2, next)
			count, err := store.CountCredits(ctx, event.ID, []string{first.ID, second.ID, foreign.ID})
			require.NoError(t, err)
			require.EqualValues(t, 2, count)
			require.NoError(t, store.PlaceCredit(ctx, event.ID, first.ID, 5))
			require.NoError(t, store.UpdateCredit(ctx, second.ID, programevent.CreditUpdate{
				CreditRole: programevent.Nullable[string]{Set: true, Value: text("host")}, SortOrder: int32Ptr(2),
			}))
			stored, err := store.Credit(ctx, event.ID, second.ID)
			require.NoError(t, err)
			require.Equal(t, "host", *stored.CreditRole)
			require.Equal(t, "Second", *stored.DisplayName)
			require.EqualValues(t, 2, stored.SortOrder)
			_, err = store.Credit(ctx, other.ID, second.ID)
			require.ErrorIs(t, err, programevent.ErrCreditNotFound)
			byID, err := store.CreditsByID(ctx, event.ID)
			require.NoError(t, err)
			require.Equal(t, []string{second.ID, first.ID}, creditIDs(byID))
			deleted, err := store.DeleteCredit(ctx, other.ID, first.ID)
			require.NoError(t, err)
			require.False(t, deleted)
			deleted, err = store.DeleteCredit(ctx, event.ID, first.ID)
			require.NoError(t, err)
			require.True(t, deleted)
			require.NoError(t, store.RemoveCredit(ctx, second.ID))
			credits, err = store.Credits(ctx, event.ID)
			require.NoError(t, err)
			require.Empty(t, credits)
			return nil
		})
	},
	"relations order by position and report what they changed": func(t *testing.T, h Harness) {
		for _, kind := range []programevent.RelationKind{programevent.ArtistRelation, programevent.LabelRelation, programevent.ClientRelation} {
			event := insertEvent(t, h, nil)
			first, second, third := h.SeedRelated(t, kind), h.SeedRelated(t, kind), h.SeedRelated(t, kind)
			transact(t, h, func(ctx context.Context, store programevent.Store) error {
				require.NoError(t, store.InsertRelations(ctx, kind, event.ID, []programevent.Relation{
					{ID: first, SortOrder: 1}, {ID: second, Role: text("host"), SortOrder: 0},
				}, at(1)))
				require.NoError(t, store.InsertRelation(ctx, kind, event.ID, programevent.Relation{ID: third, SortOrder: 2}))
				relations, err := store.Relations(ctx, kind, event.ID)
				require.NoError(t, err)
				require.Equal(t, []programevent.Relation{
					{ID: second, Role: text("host"), SortOrder: 0}, {ID: first, SortOrder: 1}, {ID: third, SortOrder: 2},
				}, relations)
				changed, err := store.SetRelationRole(ctx, kind, event.ID, first, text("guest"))
				require.NoError(t, err)
				require.EqualValues(t, 1, changed)
				changed, err = store.DeleteRelation(ctx, kind, event.ID, third)
				require.NoError(t, err)
				require.EqualValues(t, 1, changed)
				changed, err = store.DeleteRelation(ctx, kind, event.ID, third)
				require.NoError(t, err)
				require.Zero(t, changed)
				changed, err = store.SetRelationRole(ctx, kind, event.ID, third, nil)
				require.NoError(t, err)
				require.Zero(t, changed)
				relations, err = store.Relations(ctx, kind, event.ID)
				require.NoError(t, err)
				require.Equal(t, []programevent.Relation{
					{ID: second, Role: text("host"), SortOrder: 0}, {ID: first, Role: text("guest"), SortOrder: 1},
				}, relations)
				require.NoError(t, store.DeleteRelations(ctx, kind, event.ID))
				relations, err = store.Relations(ctx, kind, event.ID)
				require.NoError(t, err)
				require.Empty(t, relations)
				return nil
			})
		}
	},
	"series keep their slug, order and events": func(t *testing.T, h Harness) {
		series := programevent.Series{Slug: "series-" + uuid.NewString(), Status: programevent.StatusDraft, Title: "Series", CreatedAt: at(0), UpdatedAt: at(0)}
		transact(t, h, func(ctx context.Context, store programevent.Store) error { return store.InsertSeries(ctx, &series) })
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store programevent.Store) error {
			duplicate := programevent.Series{Slug: series.Slug, Status: programevent.StatusDraft, Title: "Copy", CreatedAt: at(0), UpdatedAt: at(0)}
			return store.InsertSeries(ctx, &duplicate)
		})
		require.ErrorIs(t, err, programevent.ErrSlugTaken)
		member := insertEvent(t, h, func(event *programevent.Event) { event.SeriesID, event.SeriesOrder = &series.ID, int32Ptr(1) })
		transact(t, h, func(ctx context.Context, store programevent.Store) error {
			taken, err := store.SeriesOrderTaken(ctx, series.ID, 1, "")
			require.NoError(t, err)
			require.True(t, taken)
			taken, err = store.SeriesOrderTaken(ctx, series.ID, 1, member.ID)
			require.NoError(t, err)
			require.False(t, taken, "an event does not take its own order")
			taken, err = store.SeriesOrderTaken(ctx, series.ID, 2, "")
			require.NoError(t, err)
			require.False(t, taken)
			bySlug, err := store.SeriesBySlug(ctx, series.Slug)
			require.NoError(t, err)
			require.Equal(t, series.ID, bySlug.ID)
			require.NoError(t, store.LockSeriesID(ctx, series.ID))
			require.ErrorIs(t, store.LockSeriesID(ctx, uuid.NewString()), programevent.ErrSeriesNotFound)
			_, err = store.SeriesBySlug(ctx, "missing-"+uuid.NewString())
			require.ErrorIs(t, err, programevent.ErrSeriesNotFound)
			events, err := store.LockSeriesEvents(ctx, series.ID)
			require.NoError(t, err)
			require.Equal(t, []string{member.ID}, events)
			require.NoError(t, store.DetachFromSeries(ctx, member.ID, at(9)))
			detached, err := store.Event(ctx, member.ID, programevent.NoLock)
			require.NoError(t, err)
			require.Nil(t, detached.SeriesID)
			require.Nil(t, detached.SeriesOrder)
			require.Equal(t, at(9), detached.UpdatedAt.UTC())
			deleted, err := store.DeleteSeries(ctx, series.ID)
			require.NoError(t, err)
			require.True(t, deleted)
			deleted, err = store.DeleteSeries(ctx, series.ID)
			require.NoError(t, err)
			require.False(t, deleted)
			_, err = store.Series(ctx, series.ID, programevent.UpdateLock)
			require.ErrorIs(t, err, programevent.ErrSeriesNotFound)
			return nil
		})
	},
	"a series update writes what it sets": func(t *testing.T, h Harness) {
		taken := programevent.Series{Slug: "taken-" + uuid.NewString(), Status: programevent.StatusDraft, Title: "Taken", CreatedAt: at(0), UpdatedAt: at(0)}
		series := programevent.Series{Slug: "series-" + uuid.NewString(), Status: programevent.StatusDraft, Title: "Series", Summary: text("Summary"), CreatedAt: at(0), UpdatedAt: at(0)}
		fileID := h.SeedFile(t)
		transact(t, h, func(ctx context.Context, store programevent.Store) error {
			if err := store.InsertSeries(ctx, &taken); err != nil {
				return err
			}
			return store.InsertSeries(ctx, &series)
		})
		published := programevent.StatusPublished
		transact(t, h, func(ctx context.Context, store programevent.Store) error {
			return store.UpdateSeries(ctx, series.ID, programevent.SeriesChange{
				Title: text("Renamed"), Status: &published, PosterChanged: true, PosterFileID: &fileID,
			}, at(4))
		})
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store programevent.Store) error {
			return store.UpdateSeries(ctx, series.ID, programevent.SeriesChange{Slug: &taken.Slug}, at(5))
		})
		require.ErrorIs(t, err, programevent.ErrSlugTaken)
		transact(t, h, func(ctx context.Context, store programevent.Store) error {
			stored, err := store.Series(ctx, series.ID, programevent.NoLock)
			require.NoError(t, err)
			require.Equal(t, "Renamed", stored.Title)
			require.Equal(t, "Summary", *stored.Summary)
			require.Equal(t, programevent.StatusPublished, stored.Status)
			require.Equal(t, fileID, *stored.PosterFileID)
			require.Equal(t, at(4), stored.UpdatedAt.UTC())
			require.NoError(t, store.UpdateSeries(ctx, series.ID, programevent.SeriesChange{PosterChanged: true}, at(6)))
			stored, err = store.Series(ctx, series.ID, programevent.NoLock)
			require.NoError(t, err)
			require.Nil(t, stored.PosterFileID)
			draft := []*commonv1.FilterSpec{{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: string(programevent.StatusPublished)}}
			listed, total, err := store.ListSeries(ctx, programevent.ListPage{Filters: draft, Limit: 10})
			require.NoError(t, err)
			require.EqualValues(t, 1, total)
			require.Equal(t, series.ID, listed[0].ID)
			return nil
		})
	},
	"types keep their slug, locales and events": func(t *testing.T, h Harness) {
		eventType := insertType(t, h, "type-"+uuid.NewString())
		taken := insertType(t, h, "taken-"+uuid.NewString())
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store programevent.Store) error {
			duplicate := programevent.Type{Slug: eventType.Slug, Status: programevent.TypeStatusActive, CreatedAt: at(0), UpdatedAt: at(0)}
			return store.InsertType(ctx, &duplicate)
		})
		require.ErrorIs(t, err, programevent.ErrSlugTaken)
		err = mutationtest.Transact(t, h.Harness, func(ctx context.Context, store programevent.Store) error {
			return store.UpdateType(ctx, eventType.ID, programevent.TypeChange{Slug: &taken.Slug}, at(1))
		})
		require.ErrorIs(t, err, programevent.ErrSlugTaken)
		inactive := programevent.TypeStatusInactive
		transact(t, h, func(ctx context.Context, store programevent.Store) error {
			require.NoError(t, store.InsertTypeLocale(ctx, programevent.TypeLocale{TypeID: eventType.ID, Locale: "ko", Name: "공연", CreatedAt: at(0), UpdatedAt: at(0)}))
			require.NoError(t, store.InsertTypeLocale(ctx, programevent.TypeLocale{TypeID: eventType.ID, Locale: "en", Name: "Concert", Description: text("Live"), CreatedAt: at(0), UpdatedAt: at(0)}))
			require.NoError(t, store.UpdateType(ctx, eventType.ID, programevent.TypeChange{Status: &inactive, SortOrder: int32Ptr(3)}, at(2)))
			require.NoError(t, store.UpdateTypeLocale(ctx, eventType.ID, "en", programevent.TypeLocaleChange{Description: programevent.Nullable[string]{Set: true}}, at(2)))
			stored, err := store.Type(ctx, eventType.ID, programevent.UpdateLock)
			require.NoError(t, err)
			require.Equal(t, programevent.TypeStatusInactive, stored.Status)
			require.EqualValues(t, 3, stored.SortOrder)
			require.Equal(t, at(2), stored.UpdatedAt.UTC())
			locales, err := store.TypeLocales(ctx, eventType.ID)
			require.NoError(t, err)
			require.Len(t, locales, 2)
			require.Equal(t, "en", locales[0].Locale)
			require.Nil(t, locales[0].Description)
			require.Equal(t, "ko", locales[1].Locale)
			locale, found, err := store.TypeLocale(ctx, eventType.ID, "ko")
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, "공연", locale.Name)
			_, found, err = store.TypeLocale(ctx, eventType.ID, "ja")
			require.NoError(t, err)
			require.False(t, found)
			_, err = store.Type(ctx, uuid.NewString(), programevent.NoLock)
			require.ErrorIs(t, err, programevent.ErrTypeNotFound)
			inactiveOnly := []*commonv1.FilterSpec{{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: string(programevent.TypeStatusInactive)}}
			listed, total, err := store.ListTypes(ctx, programevent.ListPage{Filters: inactiveOnly, Limit: 10})
			require.NoError(t, err)
			require.EqualValues(t, 1, total)
			require.Equal(t, eventType.ID, listed[0].ID)
			return nil
		})
	},
	"a type an event uses stays": func(t *testing.T, h Harness) {
		event := insertEvent(t, h, nil)
		unused := insertType(t, h, "unused-"+uuid.NewString())
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store programevent.Store) error {
			count, err := store.CountTypeEvents(ctx, event.TypeID)
			require.NoError(t, err)
			require.EqualValues(t, 1, count)
			return store.DeleteType(ctx, event.TypeID)
		})
		require.ErrorIs(t, err, programevent.ErrTypeInUse)
		transact(t, h, func(ctx context.Context, store programevent.Store) error {
			require.NoError(t, store.DeleteType(ctx, unused.ID))
			_, err := store.Type(ctx, unused.ID, programevent.NoLock)
			require.ErrorIs(t, err, programevent.ErrTypeNotFound)
			_, err = store.Type(ctx, event.TypeID, programevent.NoLock)
			require.NoError(t, err)
			return nil
		})
	},
	"Members are counted only when they exist": func(t *testing.T, h Harness) {
		member := h.SeedMember(t)
		transact(t, h, func(ctx context.Context, store programevent.Store) error {
			count, err := store.LockMembers(ctx, []string{member, uuid.NewString()})
			require.NoError(t, err)
			require.Equal(t, 1, count)
			return nil
		})
	},
	"a content write checks the revision it read": func(t *testing.T, h Harness) {
		event := insertEvent(t, h, nil)
		documentID := uuid.MustParse(*event.ContentDocumentID)
		fenced := func(context.Context, programevent.Store, uuid.UUID) (contentblock.DomainContext, error) {
			return contentblock.DomainContext{SourceLocale: "en"}, nil
		}
		changed := func(context.Context, programevent.Store) (contentblock.MetadataEffect, error) {
			return contentblock.MetadataEffect{Changed: true, AffectsTranslationSource: true}, nil
		}
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store programevent.Store) error {
			_, err := store.AdvanceContentRevision(ctx, contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: uuid.New()}, fenced, changed)
			return err
		})
		var stale *contentblock.StaleRevisionError
		require.ErrorAs(t, err, &stale)
		var advanced contentblock.AdvanceResult
		transact(t, h, func(ctx context.Context, store programevent.Store) error {
			snapshot, err := store.ContentSnapshot(ctx, documentID, "en")
			require.NoError(t, err)
			require.Equal(t, stale.CurrentRevision, snapshot.Document.Revision)
			require.Equal(t, "program_event", snapshot.Document.Profile)
			advanced, err = store.AdvanceContentRevision(ctx, contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: snapshot.Document.Revision}, fenced, changed)
			require.NoError(t, err)
			require.True(t, advanced.Changed)
			require.True(t, advanced.TranslationSourceChanged)
			require.NotEqual(t, snapshot.Document.Revision, advanced.DocumentRevision)
			return nil
		})
		denied := errors.New("fence denied")
		err = mutationtest.Transact(t, h.Harness, func(ctx context.Context, store programevent.Store) error {
			_, err := store.AdvanceContentRevision(ctx, contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: advanced.DocumentRevision},
				func(context.Context, programevent.Store, uuid.UUID) (contentblock.DomainContext, error) {
					return contentblock.DomainContext{}, denied
				}, changed)
			return err
		})
		require.ErrorIs(t, err, denied)
	},
	"a failed transaction leaves no rows": func(t *testing.T, h Harness) {
		event := insertEvent(t, h, nil)
		failed := errors.New("failed after writing")
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store programevent.Store) error {
			require.NoError(t, store.SetTitle(ctx, event.ID, "Rolled back", at(3)))
			credit := programevent.CreditRecord{EventID: event.ID, DisplayName: text("Rolled back"), CreatedAt: at(3)}
			require.NoError(t, store.InsertCredit(ctx, &credit))
			return failed
		})
		require.ErrorIs(t, err, failed)
		transact(t, h, func(ctx context.Context, store programevent.Store) error {
			title, err := store.Title(ctx, event.ID)
			require.NoError(t, err)
			require.Equal(t, "Event", title)
			credits, err := store.Credits(ctx, event.ID)
			require.NoError(t, err)
			require.Empty(t, credits)
			return nil
		})
	},
	"a locked event makes a second writer wait": func(t *testing.T, h Harness) {
		event := insertEvent(t, h, nil)
		for name, lock := range map[string]func(context.Context, programevent.Store) error{
			"event row": func(ctx context.Context, store programevent.Store) error {
				_, err := store.Event(ctx, event.ID, programevent.UpdateLock)
				return err
			},
			"event ID": func(ctx context.Context, store programevent.Store) error { return store.LockEventID(ctx, event.ID) },
			"lifecycle": func(ctx context.Context, store programevent.Store) error {
				_, err := store.LockLifecycle(ctx, event.ID)
				return err
			},
			"status": func(ctx context.Context, store programevent.Store) error {
				_, err := store.EventStatus(ctx, event.ID, programevent.UpdateLock)
				return err
			},
			"content document": func(ctx context.Context, store programevent.Store) error {
				_, err := store.ContentDocumentID(ctx, event.ID, programevent.UpdateLock)
				return err
			},
			"document root": func(ctx context.Context, store programevent.Store) error {
				_, err := store.DocumentRoot(ctx, event.ID, programevent.UpdateLock)
				return err
			},
			"title": func(ctx context.Context, store programevent.Store) error {
				_, _, err := store.LockTitle(ctx, event.ID)
				return err
			},
		} {
			t.Run(name, func(t *testing.T) {
				requireWaits(t, h, lock, func(ctx context.Context, store programevent.Store) error {
					return store.LockEventID(ctx, event.ID)
				})
			})
		}
	},
	"a share-locked event makes a writer wait": func(t *testing.T, h Harness) {
		event := insertEvent(t, h, nil)
		requireWaits(t, h, func(ctx context.Context, store programevent.Store) error {
			_, err := store.Event(ctx, event.ID, programevent.ShareLock)
			return err
		}, func(ctx context.Context, store programevent.Store) error {
			return store.TouchEvent(ctx, event.ID, at(5))
		})
	},
	"a locked locale makes a second writer wait": func(t *testing.T, h Harness) {
		event := insertEvent(t, h, nil)
		requireWaits(t, h, func(ctx context.Context, store programevent.Store) error {
			_, _, err := store.LocaleMetadata(ctx, event.ID, "en", true)
			return err
		}, func(ctx context.Context, store programevent.Store) error {
			_, err := store.LockLocale(ctx, event.ID, "en")
			return err
		})
	},
	"locked credits make a second writer wait": func(t *testing.T, h Harness) {
		event := insertEvent(t, h, nil)
		credit := insertCredit(t, h, event.ID, "Crew", 0)
		requireWaits(t, h, func(ctx context.Context, store programevent.Store) error {
			_, err := store.LockCredits(ctx, event.ID)
			return err
		}, func(ctx context.Context, store programevent.Store) error {
			return store.PlaceCredit(ctx, event.ID, credit.ID, 3)
		})
	},
	"a locked series makes a second writer wait": func(t *testing.T, h Harness) {
		series := programevent.Series{Slug: "series-" + uuid.NewString(), Status: programevent.StatusDraft, Title: "Series", CreatedAt: at(0), UpdatedAt: at(0)}
		transact(t, h, func(ctx context.Context, store programevent.Store) error { return store.InsertSeries(ctx, &series) })
		member := insertEvent(t, h, func(event *programevent.Event) { event.SeriesID, event.SeriesOrder = &series.ID, int32Ptr(0) })
		requireWaits(t, h, func(ctx context.Context, store programevent.Store) error {
			_, err := store.Series(ctx, series.ID, programevent.UpdateLock)
			return err
		}, func(ctx context.Context, store programevent.Store) error { return store.LockSeriesID(ctx, series.ID) })
		requireWaits(t, h, func(ctx context.Context, store programevent.Store) error {
			_, err := store.LockSeriesEvents(ctx, series.ID)
			return err
		}, func(ctx context.Context, store programevent.Store) error { return store.LockEventID(ctx, member.ID) })
	},
	"a locked type makes a second writer wait": func(t *testing.T, h Harness) {
		eventType := insertType(t, h, "type-"+uuid.NewString())
		requireWaits(t, h, func(ctx context.Context, store programevent.Store) error {
			_, err := store.Type(ctx, eventType.ID, programevent.UpdateLock)
			return err
		}, func(ctx context.Context, store programevent.Store) error {
			return store.UpdateType(ctx, eventType.ID, programevent.TypeChange{SortOrder: int32Ptr(1)}, at(1))
		})
	},
}

func timePtr(value time.Time) *time.Time { return &value }

func int32Ptr(value int32) *int32 { return &value }

func eventIDs(events []programevent.Event) []string {
	ids := make([]string, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.ID)
	}
	return ids
}

func mediaIDs(media []programevent.Media) []string {
	ids := make([]string, 0, len(media))
	for _, item := range media {
		ids = append(ids, item.ID)
	}
	return ids
}

func creditIDs(credits []programevent.CreditRecord) []string {
	ids := make([]string, 0, len(credits))
	for _, credit := range credits {
		ids = append(ids, credit.ID)
	}
	return ids
}
