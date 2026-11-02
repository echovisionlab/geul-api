package public_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/memberref"
	optional "github.com/echovisionlab/geul-api/internal/optional"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/programevent/public"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	"github.com/echovisionlab/geul-api/internal/translation"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"

	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// fakeStore serves the public reads from fixed rows and counts each
// method's calls; reads change nothing.
type fakeStore struct {
	mu           sync.Mutex
	calls        map[string]int
	fail         map[string]error
	events       []programevent.Event
	selections   map[string]publiccontent.Selection
	posters      map[string]string
	types        map[string]programevent.Type
	typeLocales  map[string][]programevent.TypeLocale
	places       map[string]public.MapPlace
	series       map[string]programevent.Series
	artists      []public.Participant
	labels       []public.Participant
	clients      []public.Client
	credits      []public.CreditRow
	documents    map[uuid.UUID]contentblock.Snapshot
	media        []*contentv1.ContentBlockMediaItem
	resolved     []string
	settings     translation.RuntimeSettings
	availableErr error
	available    []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		calls: map[string]int{}, selections: map[string]publiccontent.Selection{}, posters: map[string]string{},
		types: map[string]programevent.Type{}, typeLocales: map[string][]programevent.TypeLocale{},
		places: map[string]public.MapPlace{}, series: map[string]programevent.Series{},
		documents: map[uuid.UUID]contentblock.Snapshot{}, settings: translation.DefaultRuntimeSettings(),
	}
}

func (s *fakeStore) Savepoint() func() { return func() {} }

func (s *fakeStore) call(method string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[method]++
	return s.fail[method]
}

func (s *fakeStore) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, count := range s.calls {
		total += count
	}
	return total
}

func (s *fakeStore) PublicEvent(_ context.Context, slugOrID string, byID bool) (programevent.Event, error) {
	if err := s.call("PublicEvent"); err != nil {
		return programevent.Event{}, err
	}
	for _, event := range s.events {
		if event.Status == programevent.StatusDraft {
			continue
		}
		if (byID && event.ID == slugOrID) || (!byID && event.Slug == slugOrID) {
			return event, nil
		}
	}
	return programevent.Event{}, programevent.ErrEventNotFound
}

func (s *fakeStore) ListPublicEvents(_ context.Context, page programevent.ListPage) ([]programevent.Event, int64, error) {
	if err := s.call("ListPublicEvents"); err != nil {
		return nil, 0, err
	}
	end := min(int(page.Offset+page.Limit), len(s.events))
	return slices.Clone(s.events[min(int(page.Offset), end):end]), int64(len(s.events)), nil
}

func (s *fakeStore) ResolveLocalizations(_ context.Context, eventIDs []string, _ string) (map[string]publiccontent.Selection, error) {
	if err := s.call("ResolveLocalizations"); err != nil {
		return nil, err
	}
	result := map[string]publiccontent.Selection{}
	for _, id := range eventIDs {
		result[id] = s.selections[id]
	}
	return result, nil
}

func (s *fakeStore) ResolveLocalization(_ context.Context, eventID, acceptLanguage string, _ translation.RuntimeSettings) (publiccontent.Selection, error) {
	if err := s.call("ResolveLocalization"); err != nil {
		return publiccontent.Selection{}, err
	}
	s.mu.Lock()
	s.resolved = append(s.resolved, acceptLanguage)
	s.mu.Unlock()
	selection := s.selections[eventID]
	if acceptLanguage != "" {
		selection.DisplayedLocale = acceptLanguage
		selection.RequestedLocale = acceptLanguage
	}
	return selection, nil
}

func (s *fakeStore) AvailableLocales(context.Context, string, string, translation.RuntimeSettings) ([]string, error) {
	if err := s.call("AvailableLocales"); err != nil {
		return nil, err
	}
	return s.available, s.availableErr
}

func (s *fakeStore) TranslationSettings(context.Context) (translation.RuntimeSettings, error) {
	if err := s.call("TranslationSettings"); err != nil {
		return translation.DefaultRuntimeSettings(), err
	}
	return s.settings, nil
}

func (s *fakeStore) PosterFileIDs(_ context.Context, eventIDs []string) (map[string]string, error) {
	if err := s.call("PosterFileIDs"); err != nil {
		return nil, err
	}
	result := map[string]string{}
	for _, id := range eventIDs {
		if fileID, ok := s.posters[id]; ok {
			result[id] = fileID
		}
	}
	return result, nil
}

func (s *fakeStore) PosterFileID(_ context.Context, eventID string) (string, error) {
	if err := s.call("PosterFileID"); err != nil {
		return "", err
	}
	return s.posters[eventID], nil
}

func (s *fakeStore) Types(_ context.Context, typeIDs []string) (map[string]programevent.Type, error) {
	if err := s.call("Types"); err != nil {
		return nil, err
	}
	result := map[string]programevent.Type{}
	for _, id := range typeIDs {
		if eventType, ok := s.types[id]; ok {
			result[id] = eventType
		}
	}
	return result, nil
}

func (s *fakeStore) TypeLocales(_ context.Context, typeIDs []string) (map[string][]programevent.TypeLocale, error) {
	if err := s.call("TypeLocales"); err != nil {
		return nil, err
	}
	result := map[string][]programevent.TypeLocale{}
	for _, id := range typeIDs {
		if locales, ok := s.typeLocales[id]; ok {
			result[id] = locales
		}
	}
	return result, nil
}

func (s *fakeStore) Type(_ context.Context, typeID string) (programevent.Type, error) {
	if err := s.call("Type"); err != nil {
		return programevent.Type{}, err
	}
	eventType, ok := s.types[typeID]
	if !ok {
		return programevent.Type{}, programevent.ErrTypeNotFound
	}
	return eventType, nil
}

func (s *fakeStore) ListActiveTypes(_ context.Context, page programevent.ListPage) ([]programevent.Type, int64, error) {
	if err := s.call("ListActiveTypes"); err != nil {
		return nil, 0, err
	}
	var types []programevent.Type
	for _, eventType := range s.types {
		types = append(types, eventType)
	}
	slices.SortFunc(types, func(a, b programevent.Type) int { return int(a.SortOrder - b.SortOrder) })
	end := min(int(page.Offset+page.Limit), len(types))
	return types[min(int(page.Offset), end):end], int64(len(types)), nil
}

func (s *fakeStore) MapPlace(_ context.Context, mapPlaceID string) (public.MapPlace, error) {
	if err := s.call("MapPlace"); err != nil {
		return public.MapPlace{}, err
	}
	place, ok := s.places[mapPlaceID]
	if !ok {
		return public.MapPlace{}, public.ErrMapPlaceNotFound
	}
	return place, nil
}

func (s *fakeStore) PublishedSeries(_ context.Context, seriesID string) (programevent.Series, bool, error) {
	if err := s.call("PublishedSeries"); err != nil {
		return programevent.Series{}, false, err
	}
	series, ok := s.series[seriesID]
	if !ok || series.Status != programevent.StatusPublished {
		return programevent.Series{}, false, nil
	}
	return series, true, nil
}

func (s *fakeStore) FindPublishedSeries(_ context.Context, slugOrID string, byID bool) (programevent.Series, error) {
	if err := s.call("FindPublishedSeries"); err != nil {
		return programevent.Series{}, err
	}
	for _, series := range s.series {
		if series.Status == programevent.StatusPublished && ((byID && series.ID == slugOrID) || (!byID && series.Slug == slugOrID)) {
			return series, nil
		}
	}
	return programevent.Series{}, programevent.ErrSeriesNotFound
}

func (s *fakeStore) ListPublishedSeries(_ context.Context, page programevent.ListPage) ([]programevent.Series, int64, error) {
	if err := s.call("ListPublishedSeries"); err != nil {
		return nil, 0, err
	}
	var series []programevent.Series
	for _, value := range s.series {
		if value.Status == programevent.StatusPublished {
			series = append(series, value)
		}
	}
	slices.SortFunc(series, func(a, b programevent.Series) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	end := min(int(page.Offset+page.Limit), len(series))
	return series[min(int(page.Offset), end):end], int64(len(series)), nil
}

func (s *fakeStore) Artists(context.Context, string) ([]public.Participant, error) {
	return s.artists, s.call("Artists")
}

func (s *fakeStore) Labels(context.Context, string) ([]public.Participant, error) {
	return s.labels, s.call("Labels")
}

func (s *fakeStore) Clients(context.Context, string) ([]public.Client, error) {
	return s.clients, s.call("Clients")
}

func (s *fakeStore) Credits(context.Context, string) ([]public.CreditRow, error) {
	return s.credits, s.call("Credits")
}

func (s *fakeStore) ContentSnapshot(_ context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	if err := s.call("ContentSnapshot"); err != nil {
		return contentblock.Snapshot{}, err
	}
	snapshot := s.documents[documentID]
	snapshot.SourceLocale = sourceLocale
	return snapshot, nil
}

func (s *fakeStore) MediaReferences(context.Context, uuid.UUID) ([]*contentv1.ContentBlockMediaItem, error) {
	return s.media, s.call("MediaReferences")
}

func (s *fakeStore) ContentFileMIMEType(context.Context, uuid.UUID, string, uuid.UUID) (string, bool, error) {
	return "", false, s.call("ContentFileMIMEType")
}

func (s *fakeStore) SiteVariables(context.Context) (sitevariable.Context, error) {
	return sitevariable.Context{}, s.call("SiteVariables")
}

var _ public.Store = (*fakeStore)(nil)

// assets resolves the ready assets of Files and counts each call.
type assets struct {
	mu         sync.Mutex
	ready      map[string]*mediaasset.Reference
	batchErr   error
	singles    int
	batches    int
	batchKinds [][]string
}

func (a *assets) ResolveReadyAssetForSourceFile(_ context.Context, fileID string, _ ...string) *mediaasset.Reference {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.singles++
	return a.ready[fileID]
}

func (a *assets) ResolveReadyAssetsForSourceFiles(_ context.Context, fileIDs []string, kinds ...string) (map[string]*mediaasset.Reference, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.batches++
	a.batchKinds = append(a.batchKinds, kinds)
	if a.batchErr != nil {
		return nil, a.batchErr
	}
	result := map[string]*mediaasset.Reference{}
	for _, id := range fileIDs {
		if asset := a.ready[id]; asset != nil {
			result[id] = asset
		}
	}
	return result, nil
}

type files struct {
	authorization mediaasset.ContentDownloadOwnerAuthorization
}

func (f *files) HydrateAuthorizedContentBlockMedia(ctx context.Context, items []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error) {
	f.authorization, _ = mediaasset.ContentDownloadOwnerAuthorizationFromContext(ctx)
	return items, nil
}

type members struct {
	summaries map[string]*memberref.Summary
}

func (m members) LoadPublicCreditMemberSummaries(_ context.Context, memberIDs []string) (map[string]*memberref.Summary, error) {
	result := map[string]*memberref.Summary{}
	for _, id := range memberIDs {
		if summary := m.summaries[id]; summary != nil {
			result[id] = summary
		}
	}
	return result, nil
}

type fixture struct {
	store   *fakeStore
	assets  *assets
	files   *files
	members members
	service *public.Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	store := newFakeStore()
	f := &fixture{
		store: store, assets: &assets{ready: map[string]*mediaasset.Reference{}}, files: &files{},
		members: members{summaries: map[string]*memberref.Summary{}},
	}
	service, err := public.NewService(public.Deps{
		Units: mutationtestUnits(store), Assets: f.assets, Files: f.files, CreditMembers: f.members,
	})
	require.NoError(t, err)
	f.service = service
	return f
}

// publishedEvent stores a published event whose document is complete in
// English.
func (f *fixture) publishedEvent(t *testing.T, change ...func(*programevent.Event)) programevent.Event {
	t.Helper()
	documentID := uuid.New()
	reference := documentID.String()
	event := programevent.Event{
		ID: uuid.NewString(), Title: "Live", Slug: "live-" + uuid.NewString(), Status: programevent.StatusPublished,
		SourceLocale: "en", StartsAt: at(60), Timezone: "UTC", LocationMode: programevent.LocationModeOnline,
		ContentDocumentID: &reference, UpdatedAt: at(1),
	}
	for _, apply := range change {
		apply(&event)
	}
	f.store.events = append(f.store.events, event)
	f.store.documents[documentID] = englishDocument(t, documentID)
	f.store.selections[event.ID] = publiccontent.Selection{DisplayedLocale: "en", SourceLocale: "en", RequestedLocale: "en", Summary: text("Summary")}
	return event
}

func englishDocument(t *testing.T, documentID uuid.UUID) contentblock.Snapshot {
	t.Helper()
	blockID := uuid.NewString()
	revision := uuid.New()
	replacement, err := contentblock.ReplaceFromRichTextProto(documentID, revision, &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_PROGRAM_EVENT,
		SourceLocale:            "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block:     &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}}}},
			Placement: &contentv1.ContentBlockPlacement{},
		}}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{{
			BlockId: blockID,
			Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
				Props:   &contentv1.ParagraphLocaleProps{},
				Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "Body"}}}},
			}},
		}}}},
	})
	require.NoError(t, err)
	return contentblock.Snapshot{
		Document: contentblock.Document{ID: documentID, Profile: "program_event", Revision: revision},
		Blocks:   replacement.Blocks, LocaleOverlays: replacement.LocaleOverlays,
	}
}

func mutationtestUnits(store *fakeStore) public.Units {
	return mutationtest.New[public.Store](store)
}

func text(value string) *string { return &value }

// at is a fixed instant at the precision PostgreSQL stores.
func at(minutes int) time.Time {
	return time.Date(2026, time.October, 1, 10, 0, 0, 0, time.UTC).Add(time.Duration(minutes) * time.Minute)
}

func requireCode(t *testing.T, err error, code connect.Code, message string) {
	t.Helper()
	require.Error(t, err)
	wrapped := errs.Wrap(err)
	require.Equal(t, code, wrapped.Code(), wrapped.Error())
	if message != "" {
		require.Equal(t, message, wrapped.Message())
	}
}

func TestGetProgramEventAssemblesThePublishedEvent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	place, typeID, seriesID, member, artist := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	event := f.publishedEvent(t, func(event *programevent.Event) {
		event.LocationMode, event.MapPlaceID, event.TypeID, event.SeriesID = programevent.LocationModeHybrid, &place, typeID, &seriesID
	})
	f.store.places[place] = public.MapPlace{ID: place, Name: "Hall", GooglePlaceID: text("g")}
	f.store.types[typeID] = programevent.Type{ID: typeID, Slug: "concert", Status: programevent.TypeStatusActive}
	f.store.typeLocales[typeID] = []programevent.TypeLocale{{TypeID: typeID, Locale: "en", Name: "Concert"}, {TypeID: typeID, Locale: "ko", Name: "공연"}}
	f.store.series[seriesID] = programevent.Series{ID: seriesID, Slug: "festival", Status: programevent.StatusPublished, Title: "Festival", PosterFileID: text("series-poster")}
	f.store.posters[event.ID] = "event-poster"
	f.assets.ready["event-poster"] = &mediaasset.Reference{AssetID: "poster"}
	f.assets.ready["series-poster"] = &mediaasset.Reference{AssetID: "series"}
	f.assets.ready["artist-image"] = &mediaasset.Reference{AssetID: "artist"}
	f.members.summaries[member] = &memberref.Summary{ID: member}
	f.store.credits = []public.CreditRow{
		{ID: "artist-credit", ArtistID: &artist, ArtistName: text("Artist"), ArtistImageFileID: text("artist-image")},
		{ID: "member-credit", MemberID: &member},
		{ID: "unnamed-artist", ArtistID: &artist},
	}
	f.store.media = []*contentv1.ContentBlockMediaItem{{Selector: &contentv1.ContentBlockMediaSelector{}}}

	got, err := f.service.GetProgramEvent(t.Context(), public.GetProgramEventQuery{SlugOrID: " " + event.Slug + " ", AcceptLanguage: "ko"})

	require.NoError(t, err)
	detail := got.Event
	require.Equal(t, event.ID, detail.ID)
	require.Equal(t, "<p>Body</p>", detail.ContentHTML)
	require.Equal(t, "Body", detail.ContentText)
	require.Equal(t, &public.MapPlace{ID: place, Name: "Hall", GooglePlaceID: text("g")}, detail.LocationPlace)
	require.Equal(t, "Concert", detail.Type.Name, "the displayed locale names the type")
	require.Equal(t, "series", optional.Value(detail.Series.PosterAsset).AssetID)
	require.Equal(t, "poster", optional.Value(detail.PosterAsset).AssetID)
	require.Equal(t, "artist", optional.Value(detail.Credits[0].Artist.ImageAsset).AssetID)
	require.Equal(t, f.members.summaries[member], detail.Credits[1].Member)
	require.Equal(t, "", detail.Credits[2].Artist.Name)
	require.Len(t, got.BlockMedia, 1)
	require.Equal(t, mediaasset.ContentDownloadOwnerAuthorization{
		ResourceType: "program_event", ResourceID: event.ID, Status: string(programevent.StatusPublished),
		DocumentID: *event.ContentDocumentID, Mode: mediaasset.ContentDownloadOwnerAccessPublic,
	}, f.files.authorization)
}

func TestGetProgramEventFallsBackToTheSourceForAnIncompleteLocale(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	event := f.publishedEvent(t, func(event *programevent.Event) { event.LocationMode = programevent.LocationModeTBA })
	f.store.selections[event.ID] = publiccontent.Selection{DisplayedLocale: "ko", RequestedLocale: "ko", SourceLocale: "en"}
	f.store.available = []string{"en", "ko"}

	got, err := f.service.GetProgramEvent(t.Context(), public.GetProgramEventQuery{SlugOrID: event.ID, AcceptLanguage: "ko"})

	require.NoError(t, err)
	selection := got.Event.Localization
	require.Equal(t, "en", selection.DisplayedLocale)
	require.Equal(t, "ko", selection.RequestedLocale)
	require.True(t, selection.IsFallback)
	require.Equal(t, openv1.LocalizationFallbackReason_LOCALIZATION_FALLBACK_REASON_SOURCE, selection.FallbackReason)
	require.Equal(t, []string{"en"}, selection.AvailableLocales, "a locale without a complete document is not offered")
	require.Equal(t, []string{"ko", "en"}, f.store.resolved, "the request, then the source")
	require.Nil(t, got.Event.MapPlaceID)
}

func TestGetProgramEventAnswersAMissingOrDraftEvent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.service.GetProgramEvent(t.Context(), public.GetProgramEventQuery{SlugOrID: " "})
	requireCode(t, err, connect.CodeInvalidArgument, "slug is required")
	draft := f.publishedEvent(t, func(event *programevent.Event) { event.Status = programevent.StatusDraft })
	_, err = f.service.GetProgramEvent(t.Context(), public.GetProgramEventQuery{SlugOrID: draft.Slug})
	requireCode(t, err, connect.CodeNotFound, "program event not found")
}

func TestListProgramEventsQueriesDoNotGrowPerRow(t *testing.T) {
	t.Parallel()
	calls := map[int]int{}
	for _, size := range []int{1, 6, 20} {
		f := newFixture(t)
		typeID := uuid.NewString()
		f.store.types[typeID] = programevent.Type{ID: typeID, Slug: "concert"}
		f.store.typeLocales[typeID] = []programevent.TypeLocale{{TypeID: typeID, Locale: "en", Name: "Concert"}}
		for index := range size {
			event := f.publishedEvent(t, func(event *programevent.Event) { event.TypeID = typeID })
			if index%2 == 0 {
				f.store.posters[event.ID] = "poster-" + event.ID
				f.assets.ready["poster-"+event.ID] = &mediaasset.Reference{AssetID: event.ID}
			}
		}
		list, err := f.service.ListProgramEvents(t.Context(), public.ListProgramEventsQuery{Limit: int32(size)})
		require.NoError(t, err)
		require.Len(t, list.Events, size)
		require.Equal(t, list.Events[0].ID, optional.Value(list.Events[0].PosterAsset).AssetID)
		require.Equal(t, "Concert", list.Events[0].Type.Name)
		require.Equal(t, 1, f.assets.batches)
		require.Zero(t, f.assets.singles)
		calls[size] = f.store.total()
	}
	require.Equal(t, calls[1], calls[6])
	require.Equal(t, calls[1], calls[20])
}

func TestListProgramEventsResolvesPostersOneByOneWhenABatchFails(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	first, second := f.publishedEvent(t), f.publishedEvent(t)
	f.store.posters[first.ID], f.store.posters[second.ID] = "first", "second"
	f.assets.ready["first"] = &mediaasset.Reference{AssetID: "first"}
	f.assets.batchErr = errors.New("batch down")
	list, err := f.service.ListProgramEvents(t.Context(), public.ListProgramEventsQuery{})
	require.NoError(t, err)
	require.Equal(t, "first", optional.Value(list.Events[0].PosterAsset).AssetID)
	require.Nil(t, list.Events[1].PosterAsset)
	require.Equal(t, 2, f.assets.singles)

	f.assets.batchErr, f.assets.singles = nil, 0
	f.store.fail = map[string]error{"PosterFileIDs": errors.New("poster query down")}
	list, err = f.service.ListProgramEvents(t.Context(), public.ListProgramEventsQuery{})
	require.NoError(t, err)
	require.Equal(t, "first", optional.Value(list.Events[0].PosterAsset).AssetID)
	require.Equal(t, 2, f.store.calls["PosterFileID"], "each event's poster is read on its own")
}

func TestListProgramEventTypesNamesEachTypeInTheRequestedLocale(t *testing.T) {
	t.Parallel()
	calls := map[int]int{}
	for _, size := range []int{1, 6, 20} {
		f := newFixture(t)
		for index := range size {
			typeID := uuid.NewString()
			f.store.types[typeID] = programevent.Type{ID: typeID, Slug: typeID, SortOrder: int32(index), Status: programevent.TypeStatusActive}
			f.store.typeLocales[typeID] = []programevent.TypeLocale{{TypeID: typeID, Locale: "en", Name: "English"}, {TypeID: typeID, Locale: "fr", Name: "Français"}}
		}
		list, err := f.service.ListProgramEventTypes(t.Context(), public.ListProgramEventTypesQuery{Limit: int32(size), AcceptLanguage: "fr-CA"})
		require.NoError(t, err)
		require.Len(t, list.Types, size)
		require.Equal(t, "Français", list.Types[0].Name)
		require.Equal(t, programevent.Pagination{Total: int32(size), Limit: int32(size)}, list.Pagination)
		calls[size] = f.store.total()
	}
	require.Equal(t, calls[1], calls[20])

	f := newFixture(t)
	typeID := uuid.NewString()
	f.store.types[typeID] = programevent.Type{ID: typeID, Slug: "untranslated"}
	f.store.typeLocales[typeID] = []programevent.TypeLocale{{TypeID: typeID, Locale: "ja", Name: "日本語"}, {TypeID: typeID, Locale: "ko", Name: "한국어"}}
	list, err := f.service.ListProgramEventTypes(t.Context(), public.ListProgramEventTypesQuery{AcceptLanguage: "de"})
	require.NoError(t, err)
	require.Equal(t, "日本語", list.Types[0].Name, "without the requested or default locale the first locale names the type")

	f.store.typeLocales[typeID] = nil
	_, err = f.service.ListProgramEventTypes(t.Context(), public.ListProgramEventTypesQuery{})
	requireCode(t, err, connect.CodeNotFound, "program event type locale not found: "+typeID)
}

func TestProgramEventSeriesReadsServePublishedSeriesWithTheirPosters(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	published := programevent.Series{ID: uuid.NewString(), Slug: "festival", Status: programevent.StatusPublished, Title: "Festival", PosterFileID: text("poster"), UpdatedAt: at(2)}
	f.store.series[published.ID] = published
	f.store.series["draft"] = programevent.Series{ID: "draft", Slug: "draft", Status: programevent.StatusDraft, Title: "Draft"}
	f.assets.ready["poster"] = &mediaasset.Reference{AssetID: "poster"}

	bySlug, err := f.service.GetProgramEventSeries(t.Context(), public.GetProgramEventSeriesQuery{SlugOrID: "festival"})
	require.NoError(t, err)
	require.Equal(t, "poster", optional.Value(bySlug.PosterAsset).AssetID)
	byID, err := f.service.GetProgramEventSeries(t.Context(), public.GetProgramEventSeriesQuery{SlugOrID: published.ID})
	require.NoError(t, err)
	require.Equal(t, bySlug, byID)
	_, err = f.service.GetProgramEventSeries(t.Context(), public.GetProgramEventSeriesQuery{SlugOrID: "draft"})
	requireCode(t, err, connect.CodeNotFound, "program event series not found")
	_, err = f.service.GetProgramEventSeries(t.Context(), public.GetProgramEventSeriesQuery{})
	requireCode(t, err, connect.CodeInvalidArgument, "slug is required")

	list, err := f.service.ListProgramEventSeries(t.Context(), public.ListProgramEventSeriesQuery{})
	require.NoError(t, err)
	require.Len(t, list.Series, 1)
	require.Equal(t, "poster", optional.Value(list.Series[0].PosterAsset).AssetID)
	require.Equal(t, [][]string{{"image", "poster", "map_image"}}, f.assets.batchKinds)
}
