//go:build integration

package programeventpublicpostgres_test

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/programevent/public"
	programeventpublicpostgres "github.com/echovisionlab/geul-api/internal/programevent/public/postgres"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

const (
	published = "PROGRAM_EVENT_STATUS_PUBLISHED"
	archived  = "PROGRAM_EVENT_STATUS_ARCHIVED"
	draft     = "PROGRAM_EVENT_STATUS_DRAFT"
	online    = "PROGRAM_EVENT_LOCATION_MODE_ONLINE"
	tba       = "PROGRAM_EVENT_LOCATION_MODE_TBA"
	mapPlace  = "PROGRAM_EVENT_LOCATION_MODE_MAP_PLACE"
)

// seeds writes the rows a public read projects, straight to PostgreSQL.
type seeds struct {
	t  *testing.T
	db *gorm.DB
}

func (s seeds) exec(sql string, args ...any) {
	s.t.Helper()
	require.NoError(s.t, s.db.Exec(sql, args...).Error)
}

func (s seeds) document(profile string) string {
	id := uuid.NewString()
	s.exec(`INSERT INTO content_document (id, profile) VALUES (?, ?)`, id, profile)
	return id
}

func (s seeds) file() string {
	id := uuid.NewString()
	digest := sha256.Sum256([]byte(id))
	s.exec(`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256) VALUES (?, ?, 'image/webp', 1024, 'webp', ?)`, id, id, digest[:])
	return id
}

func (s seeds) eventType(slug, status string, sortOrder int, names map[string]string) string {
	id := uuid.NewString()
	s.exec(`INSERT INTO program_event_type (id, slug, status, sort_order, requires_place) VALUES (?, ?, ?, ?, TRUE)`, id, slug, status, sortOrder)
	for locale, name := range names {
		s.exec(`INSERT INTO program_event_type_locale (type_id, locale, name, description) VALUES (?, ?, ?, ?)`, id, locale, name, name+" description")
	}
	return id
}

type event struct {
	title, status, mode, typeID string
	startsAt                    time.Time
	mapPlaceID, seriesID        *string
}

func (s seeds) event(e event) string {
	id := uuid.NewString()
	s.exec(`INSERT INTO program_event (id, slug, status, source_locale, type_id, series_id, starts_at, location_mode, map_place_id, title, content_document_id)
		VALUES (?, ?, ?, 'en', ?, ?, ?, ?, ?, ?, ?)`,
		id, "event-"+id, e.status, e.typeID, e.seriesID, e.startsAt, e.mode, e.mapPlaceID, e.title, s.document("program_event"))
	s.exec(`INSERT INTO program_event_translation (entity_id, locale, summary) VALUES (?, 'en', ?)`, id, e.title+" summary")
	return id
}

func (s seeds) artist(status, name string) string {
	id := uuid.NewString()
	s.exec(`INSERT INTO artist (id, slug, status, content_document_id) VALUES (?, ?, ?, ?)`, id, "artist-"+id, status, s.document("compact"))
	s.exec(`INSERT INTO artist_translation (entity_id, locale, title) VALUES (?, 'en', ?)`, id, name)
	return id
}

func (s seeds) label(status, name string) string {
	id := uuid.NewString()
	s.exec(`INSERT INTO label (id, slug, status, content_document_id) VALUES (?, ?, ?, ?)`, id, "label-"+id, status, s.document("compact"))
	s.exec(`INSERT INTO label_translation (entity_id, locale, title) VALUES (?, 'en', ?)`, id, name)
	return id
}

func (s seeds) series(slug, status, title string) string {
	id := uuid.NewString()
	s.exec(`INSERT INTO program_event_series (id, slug, status, title, summary) VALUES (?, ?, ?, ?, ?)`, id, slug, status, title, title+" summary")
	return id
}

// read runs work in the read-only snapshot most public reads use.
func read(t *testing.T, units mutation.UnitOfWork[public.Store], work func(ctx context.Context, store public.Store)) {
	t.Helper()
	require.NoError(t, units.Read(t.Context(), func(store public.Store) error {
		work(t.Context(), store)
		return nil
	}))
}

// readCurrent runs work in the read-committed transaction the event detail
// uses: PublicEvent share-locks the event and TranslationSettings stores
// the default settings.
func readCurrent(t *testing.T, units mutation.UnitOfWork[public.Store], work func(ctx context.Context, store public.Store)) {
	t.Helper()
	require.NoError(t, units.ReadCurrent(t.Context(), func(store public.Store) error {
		work(t.Context(), store)
		return nil
	}))
}

func newUnits(t *testing.T) (*gorm.DB, mutation.UnitOfWork[public.Store]) {
	t.Helper()
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	// The projections read no content document.
	units, err := programeventpublicpostgres.NewUnits(db, &contentblock.Store{})
	require.NoError(t, err)
	return db, units
}

func TestPostgresPublicStoreProjectsEventsIntegration(t *testing.T) {
	t.Parallel()
	db, units := newUnits(t)
	seed := seeds{t: t, db: db}
	start := time.Date(2026, time.November, 1, 10, 0, 0, 0, time.UTC)
	typeID := seed.eventType("live", "PROGRAM_EVENT_TYPE_STATUS_ACTIVE", 0, map[string]string{"en": "Live"})
	otherTypeID := seed.eventType("talk", "PROGRAM_EVENT_TYPE_STATUS_ACTIVE", 1, map[string]string{"en": "Talk"})
	var placeID string
	require.NoError(t, db.Raw(`INSERT INTO map_place (name, address, lat, lng, google_place_id) VALUES ('Venue', '2 Event Way', 37.57, 126.98, 'google-place') RETURNING id`).Scan(&placeID).Error)

	later := seed.event(event{title: "Later concert", status: published, mode: online, typeID: typeID, startsAt: start.Add(48 * time.Hour)})
	sooner := seed.event(event{title: "Sooner concert", status: published, mode: mapPlace, typeID: typeID, startsAt: start, mapPlaceID: &placeID})
	unscheduled := seed.event(event{title: "Unscheduled concert", status: published, mode: tba, typeID: typeID, startsAt: start.Add(96 * time.Hour)})
	past := seed.event(event{title: "Archived concert", status: archived, mode: online, typeID: typeID, startsAt: start.Add(-48 * time.Hour)})
	hidden := seed.event(event{title: "Draft concert", status: draft, mode: online, typeID: typeID, startsAt: start})
	talk := seed.event(event{title: "Talk session", status: published, mode: online, typeID: otherTypeID, startsAt: start})

	readCurrent(t, units, func(ctx context.Context, store public.Store) {
		got, err := store.PublicEvent(ctx, "event-"+sooner, false)
		require.NoError(t, err)
		require.Equal(t, sooner, got.ID)
		require.Equal(t, programevent.StatusPublished, got.Status)
		require.Equal(t, &placeID, got.MapPlaceID)
		got, err = store.PublicEvent(ctx, past, true)
		require.NoError(t, err)
		require.Equal(t, programevent.StatusArchived, got.Status, "an archived event stays public")
		_, err = store.PublicEvent(ctx, hidden, true)
		require.ErrorIs(t, err, programevent.ErrEventNotFound)
		_, err = store.PublicEvent(ctx, "event-"+hidden, false)
		require.ErrorIs(t, err, programevent.ErrEventNotFound)

		place, err := store.MapPlace(ctx, placeID)
		require.NoError(t, err)
		require.Equal(t, public.MapPlace{ID: placeID, Name: "Venue", Address: "2 Event Way", Lat: 37.57, Lng: 126.98, GooglePlaceID: stringPtr("google-place")}, place)
		_, err = store.MapPlace(ctx, uuid.NewString())
		require.ErrorIs(t, err, public.ErrMapPlaceNotFound)
	})

	list := func(t *testing.T, page programevent.ListPage) ([]string, int64) {
		t.Helper()
		if page.Limit == 0 {
			page.Limit = 20
		}
		var ids []string
		var total int64
		read(t, units, func(ctx context.Context, store public.Store) {
			events, count, err := store.ListPublicEvents(ctx, page)
			require.NoError(t, err)
			for _, event := range events {
				ids = append(ids, event.ID)
			}
			total = count
		})
		return ids, total
	}
	t.Run("lists public events with the unscheduled first, then by start", func(t *testing.T) {
		ids, total := list(t, programevent.ListPage{})
		require.Len(t, ids, 5)
		require.Equal(t, []string{unscheduled, past}, ids[:2])
		require.ElementsMatch(t, []string{sooner, talk}, ids[2:4], "same start, by ID")
		require.Equal(t, later, ids[4])
		require.EqualValues(t, 5, total)
		paged, total := list(t, programevent.ListPage{Limit: 2, Offset: 1})
		require.Equal(t, ids[1:3], paged)
		require.EqualValues(t, 5, total)
	})
	t.Run("filters by type, search and status", func(t *testing.T) {
		ids, _ := list(t, programevent.ListPage{Filters: []*commonv1.FilterSpec{nil, {Field: "type_id", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: otherTypeID}}})
		require.Equal(t, []string{talk}, ids)
		ids, _ = list(t, programevent.ListPage{Filters: []*commonv1.FilterSpec{{Field: "type_slug", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "talk"}}})
		require.Equal(t, []string{talk}, ids)
		ids, _ = list(t, programevent.ListPage{Filters: []*commonv1.FilterSpec{{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "sooner concert summary"}}})
		require.Equal(t, []string{sooner}, ids, "search reads the source-locale summary")
		ids, _ = list(t, programevent.ListPage{Filters: []*commonv1.FilterSpec{{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: archived}}})
		require.Equal(t, []string{past}, ids)
		ids, _ = list(t, programevent.ListPage{Filters: []*commonv1.FilterSpec{{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: draft}}})
		require.Empty(t, ids, "a draft status filter matches nothing")
		ids, _ = list(t, programevent.ListPage{Filters: []*commonv1.FilterSpec{{Field: "map_place_id", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: placeID}}})
		require.Equal(t, []string{sooner}, ids)
	})
	t.Run("sorts by an allowed field and keeps the unscheduled first by start", func(t *testing.T) {
		ids, _ := list(t, programevent.ListPage{Sorts: []*commonv1.SortSpec{{Field: "starts_at", Order: commonv1.SortOrder_SORT_ORDER_DESC}}})
		require.Equal(t, unscheduled, ids[0])
		require.Equal(t, later, ids[1])
		require.Equal(t, past, ids[len(ids)-1])
		ids, _ = list(t, programevent.ListPage{Sorts: []*commonv1.SortSpec{{Field: "title", Order: commonv1.SortOrder_SORT_ORDER_ASC}}})
		require.Equal(t, []string{past, later, sooner, talk, unscheduled}, ids)
	})
}

func TestPostgresPublicStoreProjectsPostersTypesAndSeriesIntegration(t *testing.T) {
	t.Parallel()
	db, units := newUnits(t)
	seed := seeds{t: t, db: db}
	start := time.Date(2026, time.November, 1, 10, 0, 0, 0, time.UTC)
	liveID := seed.eventType("live", "PROGRAM_EVENT_TYPE_STATUS_ACTIVE", 1, map[string]string{"en": "Live", "ko": "라이브"})
	talkID := seed.eventType("talk", "PROGRAM_EVENT_TYPE_STATUS_ACTIVE", 0, map[string]string{"en": "Talk"})
	retiredID := seed.eventType("retired", "PROGRAM_EVENT_TYPE_STATUS_INACTIVE", 0, map[string]string{"en": "Retired live"})
	withPrimary := seed.event(event{title: "Primary", status: published, mode: online, typeID: liveID, startsAt: start})
	withoutPrimary := seed.event(event{title: "Ordered", status: published, mode: online, typeID: liveID, startsAt: start})
	withoutPoster := seed.event(event{title: "Gallery only", status: published, mode: online, typeID: liveID, startsAt: start})
	media := func(eventID, role string, sortOrder int, primary bool) string {
		fileID := seed.file()
		seed.exec(`INSERT INTO program_event_media (event_id, file_id, role, sort_order, is_primary) VALUES (?, ?, ?, ?, ?)`, eventID, fileID, role, sortOrder, primary)
		return fileID
	}
	media(withPrimary, "poster", 0, false)
	primaryFile := media(withPrimary, "poster", 1, true)
	media(withPrimary, "gallery", 0, true)
	firstFile := media(withoutPrimary, "poster", 0, false)
	media(withoutPrimary, "poster", 1, false)
	media(withoutPoster, "gallery", 0, true)
	festival := seed.series("festival", published, "Festival")
	seed.series("drafted", draft, "Drafted festival")

	read(t, units, func(ctx context.Context, store public.Store) {
		posters, err := store.PosterFileIDs(ctx, []string{withPrimary, withoutPrimary, withoutPoster, " " + withPrimary})
		require.NoError(t, err)
		require.Equal(t, map[string]string{withPrimary: primaryFile, withoutPrimary: firstFile}, posters)
		poster, err := store.PosterFileID(ctx, withPrimary)
		require.NoError(t, err)
		require.Equal(t, primaryFile, poster)
		poster, err = store.PosterFileID(ctx, withoutPrimary)
		require.NoError(t, err)
		require.Equal(t, firstFile, poster)
		poster, err = store.PosterFileID(ctx, withoutPoster)
		require.NoError(t, err)
		require.Empty(t, poster)

		types, err := store.Types(ctx, []string{liveID, retiredID, uuid.NewString()})
		require.NoError(t, err)
		require.Len(t, types, 2)
		require.True(t, types[liveID].RequiresPlace)
		require.Equal(t, programevent.TypeStatusInactive, types[retiredID].Status)
		locales, err := store.TypeLocales(ctx, []string{liveID, talkID})
		require.NoError(t, err)
		require.Equal(t, []string{"en", "ko"}, []string{locales[liveID][0].Locale, locales[liveID][1].Locale})
		require.Equal(t, "라이브", locales[liveID][1].Name)
		require.Len(t, locales[talkID], 1)
		eventType, err := store.Type(ctx, retiredID)
		require.NoError(t, err)
		require.Equal(t, "retired", eventType.Slug)
		_, err = store.Type(ctx, uuid.NewString())
		require.ErrorIs(t, err, programevent.ErrTypeNotFound)

		active, total, err := store.ListActiveTypes(ctx, programevent.ListPage{Limit: 20})
		require.NoError(t, err)
		require.EqualValues(t, 2, total)
		require.Equal(t, []string{talkID, liveID}, []string{active[0].ID, active[1].ID}, "active types by sort order")
		active, total, err = store.ListActiveTypes(ctx, programevent.ListPage{Limit: 20, Filters: []*commonv1.FilterSpec{{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "라이브"}}})
		require.NoError(t, err)
		require.EqualValues(t, 1, total)
		require.Equal(t, liveID, active[0].ID)

		series, ok, err := store.PublishedSeries(ctx, festival)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "Festival", series.Title)
		found, err := store.FindPublishedSeries(ctx, "festival", false)
		require.NoError(t, err)
		require.Equal(t, festival, found.ID)
		found, err = store.FindPublishedSeries(ctx, festival, true)
		require.NoError(t, err)
		require.Equal(t, "festival", found.Slug)
		_, err = store.FindPublishedSeries(ctx, "drafted", false)
		require.ErrorIs(t, err, programevent.ErrSeriesNotFound)
		listed, total, err := store.ListPublishedSeries(ctx, programevent.ListPage{Limit: 20, Filters: []*commonv1.FilterSpec{{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "festival"}}})
		require.NoError(t, err)
		require.EqualValues(t, 1, total)
		require.Equal(t, festival, listed[0].ID)
	})
}

func TestPostgresPublicStoreProjectsParticipantsAndCreditsIntegration(t *testing.T) {
	t.Parallel()
	db, units := newUnits(t)
	seed := seeds{t: t, db: db}
	typeID := seed.eventType("live", "PROGRAM_EVENT_TYPE_STATUS_ACTIVE", 0, map[string]string{"en": "Live"})
	eventID := seed.event(event{title: "Credits", status: published, mode: online, typeID: typeID, startsAt: time.Now().UTC()})
	shownArtist := seed.artist("ARTIST_STATUS_PUBLISHED", "Shown artist")
	draftArtist := seed.artist("ARTIST_STATUS_DRAFT", "Draft artist")
	shownLabel := seed.label("LABEL_STATUS_PUBLISHED", "Shown label")
	draftLabel := seed.label("LABEL_STATUS_DRAFT", "Draft label")
	clientID := uuid.NewString()
	seed.exec(`INSERT INTO client (id, name, website) VALUES (?, 'Client', 'https://client.example')`, clientID)
	for index, artistID := range []string{shownArtist, draftArtist} {
		seed.exec(`INSERT INTO program_event_artist (event_id, artist_id, role, sort_order) VALUES (?, ?, 'performer', ?)`, eventID, artistID, index)
	}
	for index, labelID := range []string{draftLabel, shownLabel} {
		seed.exec(`INSERT INTO program_event_label (event_id, label_id, role, sort_order) VALUES (?, ?, 'presenter', ?)`, eventID, labelID, index)
	}
	seed.exec(`INSERT INTO program_event_client (event_id, client_id, role, sort_order) VALUES (?, ?, 'sponsor', 0)`, eventID, clientID)

	secondFile, firstFile := seed.file(), seed.file()
	seed.exec(`INSERT INTO artist_file (artist_id, file_id, sort_order) VALUES (?, ?, 1), (?, ?, 0)`, shownArtist, secondFile, shownArtist, firstFile)
	memberID := uuid.NewString()
	seed.exec(`INSERT INTO member (id, nickname) VALUES (?, 'Credit member')`, memberID)
	seed.exec(`INSERT INTO program_event_credit (event_id, artist_id, credit_role, description, sort_order) VALUES (?, ?, 'performer', 'Plays', 0)`, eventID, shownArtist)
	seed.exec(`INSERT INTO program_event_credit (event_id, artist_id, sort_order) VALUES (?, ?, 1)`, eventID, draftArtist)
	seed.exec(`INSERT INTO program_event_credit (event_id, member_id, sort_order) VALUES (?, ?, 2)`, eventID, memberID)
	seed.exec(`INSERT INTO program_event_credit (event_id, display_name, sort_order) VALUES (?, 'Guest', 3)`, eventID)

	read(t, units, func(ctx context.Context, store public.Store) {
		artists, err := store.Artists(ctx, eventID)
		require.NoError(t, err)
		require.Equal(t, []public.Participant{{ID: shownArtist, Name: "Shown artist", Slug: stringPtr("artist-" + shownArtist), Role: stringPtr("performer")}}, artists)
		labels, err := store.Labels(ctx, eventID)
		require.NoError(t, err)
		require.Equal(t, []public.Participant{{ID: shownLabel, Name: "Shown label", Slug: stringPtr("label-" + shownLabel), Role: stringPtr("presenter"), SortOrder: 1}}, labels)
		clients, err := store.Clients(ctx, eventID)
		require.NoError(t, err)
		require.Equal(t, []public.Client{{ID: clientID, Name: "Client", Website: stringPtr("https://client.example"), Role: stringPtr("sponsor")}}, clients)

		credits, err := store.Credits(ctx, eventID)
		require.NoError(t, err)
		require.Len(t, credits, 4)
		require.Equal(t, &shownArtist, credits[0].ArtistID)
		require.Equal(t, "Shown artist", *credits[0].ArtistName)
		require.Equal(t, &firstFile, credits[0].ArtistImageFileID, "the artist's first image by sort order")
		require.Equal(t, "performer", *credits[0].CreditRole)
		require.Equal(t, "Plays", *credits[0].Description)
		require.Nil(t, credits[1].ArtistID, "an unpublished artist stays withheld")
		require.Nil(t, credits[1].ArtistImageFileID)
		require.Equal(t, &memberID, credits[2].MemberID)
		require.Equal(t, "Guest", *credits[3].DisplayName)
	})
}

func TestPostgresPublicStoreResolvesLocalizationsIntegration(t *testing.T) {
	t.Parallel()
	db, units := newUnits(t)
	seed := seeds{t: t, db: db}
	typeID := seed.eventType("live", "PROGRAM_EVENT_TYPE_STATUS_ACTIVE", 0, map[string]string{"en": "Live"})
	eventID := seed.event(event{title: "Localized", status: published, mode: online, typeID: typeID, startsAt: time.Now().UTC()})
	seed.exec(`INSERT INTO program_event_translation (entity_id, locale, summary) VALUES (?, 'ko', '한국어 요약')`, eventID)

	readCurrent(t, units, func(ctx context.Context, store public.Store) {
		settings, err := store.TranslationSettings(ctx)
		require.NoError(t, err)
		selection, err := store.ResolveLocalization(ctx, eventID, "ko", settings)
		require.NoError(t, err)
		require.Equal(t, "ko", selection.DisplayedLocale)
		require.False(t, selection.IsFallback)
		require.Equal(t, "한국어 요약", *selection.Summary)
		selection, err = store.ResolveLocalization(ctx, eventID, "ja", settings)
		require.NoError(t, err)
		require.Equal(t, "en", selection.DisplayedLocale)
		require.True(t, selection.IsFallback)
		require.Equal(t, "Localized summary", *selection.Summary)
		selections, err := store.ResolveLocalizations(ctx, []string{eventID}, "ko")
		require.NoError(t, err)
		require.Equal(t, "한국어 요약", *selections[eventID].Summary)
		locales, err := store.AvailableLocales(ctx, eventID, "en", settings)
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"en", "ko"}, locales)
	})
}

func stringPtr(value string) *string { return &value }
