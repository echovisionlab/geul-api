// Package programeventtest holds the in-memory programevent.Store the
// Program Event use-case tests run on, and the Store contract it shares
// with programeventpostgres. The fake knows no SQL and takes no lock; it
// keeps the sentinels, orderings and row counts the PostgreSQL store
// answers with, and a minimal content document whose revision advances like
// the content store's.
package programeventtest

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	"github.com/echovisionlab/geul-api/internal/translation"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"github.com/google/uuid"
)

type localeKey struct{ eventID, locale string }

type typeLocaleKey struct{ typeID, locale string }

type relationKey struct {
	kind    programevent.RelationKind
	eventID string
}

type bindingKey struct{ owner, ownerID, key string }

type contentFileKey struct {
	blockID       uuid.UUID
	referencePath string
	fileID        uuid.UUID
}

// Binding is one public asset binding the Store made.
type Binding struct {
	FileID string
}

type state struct {
	events      map[string]programevent.Event
	locales     map[localeKey]programevent.LocaleMetadata
	media       map[string]programevent.Media
	credits     map[string]programevent.CreditRecord
	relations   map[relationKey][]programevent.Relation
	series      map[string]programevent.Series
	types       map[string]programevent.Type
	typeLocales map[typeLocaleKey]programevent.TypeLocale
	documents   map[uuid.UUID]contentblock.Snapshot
	bindings    map[bindingKey]Binding
	routes      map[string]bool
	members     map[string]bool
	updates     []programevent.ContentUpdate
	lockedFiles []string
	clock       int
}

func (s state) clone() state {
	clone := s
	clone.events = mutationtest.ClonePersisted(s.events)
	clone.locales = mutationtest.ClonePersisted(s.locales)
	clone.media = mutationtest.ClonePersisted(s.media)
	clone.credits = mutationtest.ClonePersisted(s.credits)
	clone.relations = mutationtest.ClonePersisted(s.relations)
	clone.series = mutationtest.ClonePersisted(s.series)
	clone.types = mutationtest.ClonePersisted(s.types)
	clone.typeLocales = mutationtest.ClonePersisted(s.typeLocales)
	clone.documents = mutationtest.ClonePersisted(s.documents)
	clone.bindings = mutationtest.ClonePersisted(s.bindings)
	clone.routes = mutationtest.ClonePersisted(s.routes)
	clone.members = mutationtest.ClonePersisted(s.members)
	clone.updates = mutationtest.ClonePersisted(s.updates)
	clone.lockedFiles = slices.Clone(s.lockedFiles)
	return clone
}

// Store is the in-memory programevent.Store. Seed it with the Put methods
// and the exported fields; read it back with the getters.
type Store struct {
	mu sync.Mutex
	state
	// Fence is the identity fence reads take through LockActivePrincipal;
	// pass the same Fence as Deps.Fence so mutations see the same
	// deactivations.
	Fence *identitystatetest.Fence
	// Fail fails the Store method of that name with its error.
	Fail map[string]error
	// Before runs before the Store method of that name, in its
	// transaction, to play a concurrent writer.
	Before map[string]func(store *Store)
	// Sessions are the principals of collaboration sessions by session ID.
	Sessions map[string]*auth.UserInfo
	// RevokedContributors are the Members who may no longer save an
	// event's room, by event ID.
	RevokedContributors map[string][]string
	// PreferredLocales are Members' preferred locales.
	PreferredLocales map[string]string
	// Settings are the translation runtime settings.
	Settings translation.RuntimeSettings
	// CreditArtists are the credited artists by ID, ArtistImages their
	// first image File.
	CreditArtists map[string]programevent.CreditArtist
	ArtistImages  map[string]string
	// ReadyImages and InlineImages are the ready image assets of Files.
	ReadyImages  map[string]*mediaasset.Reference
	InlineImages map[string]*mediaasset.Reference
	// AttachableFiles are the Files a Program Event may attach; a nil map
	// admits every File.
	AttachableFiles map[string]bool
	// ContentFiles are the MIME types of active Block File references.
	ContentFiles map[contentFileKey]string
	// BlockMedia are the active File usages of a document.
	BlockMedia map[uuid.UUID][]*fileref.BlockMediaItem
	// Hydrated records each principal HydrateBlockMedia served.
	Hydrated []*auth.UserInfo
	// Site is the site settings documents render.
	Site sitevariable.Context
	// ContentResults are the results the next content writes answer with,
	// in order, after their fence and revision check.
	ContentResults []contentblock.Result
}

// NewStore returns an empty fake Store.
func NewStore() *Store {
	return &Store{
		state: state{
			events: map[string]programevent.Event{}, locales: map[localeKey]programevent.LocaleMetadata{},
			media: map[string]programevent.Media{}, credits: map[string]programevent.CreditRecord{},
			relations: map[relationKey][]programevent.Relation{}, series: map[string]programevent.Series{},
			types: map[string]programevent.Type{}, typeLocales: map[typeLocaleKey]programevent.TypeLocale{},
			documents: map[uuid.UUID]contentblock.Snapshot{}, bindings: map[bindingKey]Binding{},
			routes: map[string]bool{}, members: map[string]bool{},
		},
		Fence:    &identitystatetest.Fence{},
		Settings: translation.DefaultRuntimeSettings(),
	}
}

// New returns a fake Store and its unit of work.
func New() (*Store, *mutationtest.UnitOfWork[programevent.Store]) {
	store := NewStore()
	return store, mutationtest.New[programevent.Store](store)
}

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() mutationtest.Checkpoint {
	s.mu.Lock()
	saved := s.clone()
	s.mu.Unlock()
	return mutationtest.Checkpoint{
		State: map[string]any{
			"events":      saved.events,
			"locales":     saved.locales,
			"media":       saved.media,
			"credits":     saved.credits,
			"relations":   saved.relations,
			"series":      saved.series,
			"types":       saved.types,
			"typeLocales": saved.typeLocales,
			"documents":   saved.documents,
			"bindings":    saved.bindings,
			"routes":      saved.routes,
			"members":     saved.members,
			"updates":     saved.updates,
		},
		Restore: func() {
			s.mu.Lock()
			s.state = saved
			s.mu.Unlock()
		},
	}
}

var _ programevent.Store = (*Store)(nil)

// enter runs the hook and the failure of method.
func (s *Store) enter(method string) error {
	if hook := s.Before[method]; hook != nil {
		hook(s)
	}
	if s.Fail == nil {
		return nil
	}
	return s.Fail[method]
}

// tick is the next creation instant, one microsecond after the last, so
// rows order by creation as PostgreSQL orders them.
func (s *Store) tick() time.Time {
	s.clock++
	return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(s.clock) * time.Microsecond)
}

// PutEvent stores event as it is, filling a missing ID.
func (s *Store) PutEvent(event programevent.Event) programevent.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	s.events[event.ID] = event
	return event
}

// StoredEvent returns the event row.
func (s *Store) StoredEvent(eventID string) (programevent.Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	event, ok := s.events[eventID]
	return event, ok
}

// PutLocale stores one locale row.
func (s *Store) PutLocale(eventID string, locale programevent.LocaleMetadata) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.locales[localeKey{eventID, locale.Locale}] = locale
}

// StoredLocale returns one locale row.
func (s *Store) StoredLocale(eventID, locale string) (programevent.LocaleMetadata, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.locales[localeKey{eventID, locale}]
	return row, ok
}

// PutDocument stores a content document snapshot.
func (s *Store) PutDocument(snapshot contentblock.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.documents[snapshot.Document.ID] = snapshot
}

// StoredDocument returns a content document snapshot.
func (s *Store) StoredDocument(documentID uuid.UUID) (contentblock.Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok := s.documents[documentID]
	return snapshot, ok
}

// StoredMedia returns the media relations of an event by role, primary
// first.
func (s *Store) StoredMedia(eventID string) []programevent.Media {
	media, _ := s.EventMedia(context.Background(), eventID)
	return media
}

// StoredCredits returns the credits of an event by position.
func (s *Store) StoredCredits(eventID string) []programevent.CreditRecord {
	credits, _ := s.CreditsByID(context.Background(), eventID)
	return credits
}

// StoredRelations returns the relations of kind.
func (s *Store) StoredRelations(kind programevent.RelationKind, eventID string) []programevent.Relation {
	relations, _ := s.Relations(context.Background(), kind, eventID)
	return relations
}

// PutSeries stores series as it is, filling a missing ID.
func (s *Store) PutSeries(series programevent.Series) programevent.Series {
	s.mu.Lock()
	defer s.mu.Unlock()
	if series.ID == "" {
		series.ID = uuid.NewString()
	}
	s.series[series.ID] = series
	return series
}

// StoredSeries returns the series row.
func (s *Store) StoredSeries(seriesID string) (programevent.Series, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	series, ok := s.series[seriesID]
	return series, ok
}

// PutType stores eventType as it is, filling a missing ID, with its
// locales.
func (s *Store) PutType(eventType programevent.Type, locales ...programevent.TypeLocale) programevent.Type {
	s.mu.Lock()
	defer s.mu.Unlock()
	if eventType.ID == "" {
		eventType.ID = uuid.NewString()
	}
	s.types[eventType.ID] = eventType
	for _, locale := range locales {
		locale.TypeID = eventType.ID
		s.typeLocales[typeLocaleKey{eventType.ID, locale.Locale}] = locale
	}
	return eventType
}

// StoredType returns the type row.
func (s *Store) StoredType(typeID string) (programevent.Type, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	eventType, ok := s.types[typeID]
	return eventType, ok
}

// StoredTypeLocales returns the locales of a type by locale.
func (s *Store) StoredTypeLocales(typeID string) []programevent.TypeLocale {
	locales, _ := s.TypeLocales(context.Background(), typeID)
	return locales
}

// PutMember stores a Member credits and contributors may name.
func (s *Store) PutMember(memberID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.members[memberID] = true
}

// OccupyRoute makes a Page hold the slug's route.
func (s *Store) OccupyRoute(kind programevent.RouteKind, slug string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[routeKey(kind, slug)] = true
}

// Bindings returns the public asset bindings of an owner by binding key.
func (s *Store) Bindings(owner, ownerID string) map[string]Binding {
	s.mu.Lock()
	defer s.mu.Unlock()
	bindings := map[string]Binding{}
	for key, binding := range s.bindings {
		if key.owner == owner && key.ownerID == ownerID {
			bindings[key.key] = binding
		}
	}
	return bindings
}

// LockedFiles returns the Files the Store locked for attachment.
func (s *Store) LockedFiles() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.lockedFiles)
}

// Updates returns the content.updated signals the Store scheduled.
func (s *Store) Updates() []programevent.ContentUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.updates)
}

func routeKey(kind programevent.RouteKind, slug string) string {
	if kind == programevent.SeriesRoute {
		return "event-series/" + slug
	}
	return "events/" + slug
}

func (s *Store) slugTaken(table string, slug, exceptID string) bool {
	switch table {
	case "event":
		for id, event := range s.events {
			if id != exceptID && event.Slug == slug {
				return true
			}
		}
	case "series":
		for id, series := range s.series {
			if id != exceptID && series.Slug == slug {
				return true
			}
		}
	case "type":
		for id, eventType := range s.types {
			if id != exceptID && eventType.Slug == slug {
				return true
			}
		}
	}
	return false
}

func (s *Store) event(eventID string) (programevent.Event, error) {
	event, ok := s.events[eventID]
	if !ok {
		return programevent.Event{}, programevent.ErrEventNotFound
	}
	return event, nil
}

// Event implements programevent.Store.
func (s *Store) Event(_ context.Context, eventID string, _ programevent.RowLock) (programevent.Event, error) {
	if err := s.enter("Event"); err != nil {
		return programevent.Event{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.event(eventID)
}

// RequireEvent implements programevent.Store.
func (s *Store) RequireEvent(_ context.Context, eventID string) error {
	if err := s.enter("RequireEvent"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.event(eventID)
	return err
}

// LockEventID implements programevent.Store.
func (s *Store) LockEventID(_ context.Context, eventID string) error {
	if err := s.enter("LockEventID"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.event(eventID)
	return err
}

// EventStatus implements programevent.Store.
func (s *Store) EventStatus(_ context.Context, eventID string, _ programevent.RowLock) (programevent.Status, error) {
	if err := s.enter("EventStatus"); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	event, err := s.event(eventID)
	return event.Status, err
}

func lifecycleOf(event programevent.Event) programevent.Lifecycle {
	return programevent.Lifecycle{ID: event.ID, Status: event.Status, PublishedAt: event.PublishedAt, UpdatedAt: event.UpdatedAt}
}

// LockLifecycle implements programevent.Store.
func (s *Store) LockLifecycle(_ context.Context, eventID string) (programevent.Lifecycle, error) {
	if err := s.enter("LockLifecycle"); err != nil {
		return programevent.Lifecycle{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	event, err := s.event(eventID)
	return lifecycleOf(event), err
}

// SetLifecycle implements programevent.Store.
func (s *Store) SetLifecycle(_ context.Context, eventID string, status programevent.Status, publishedAt *time.Time, updatedAt time.Time) (programevent.Lifecycle, error) {
	if err := s.enter("SetLifecycle"); err != nil {
		return programevent.Lifecycle{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	event, err := s.event(eventID)
	if err != nil {
		return programevent.Lifecycle{}, err
	}
	event.Status, event.UpdatedAt = status, updatedAt
	if publishedAt != nil {
		value := *publishedAt
		event.PublishedAt = &value
	}
	s.events[eventID] = event
	return lifecycleOf(event), nil
}

// InsertEvent implements programevent.Store.
func (s *Store) InsertEvent(_ context.Context, event *programevent.Event) error {
	if err := s.enter("InsertEvent"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.slugTaken("event", event.Slug, "") {
		return programevent.ErrSlugTaken
	}
	if event.ContentDocumentID != nil {
		for _, other := range s.events {
			if other.ContentDocumentID != nil && *other.ContentDocumentID == *event.ContentDocumentID {
				return programevent.ErrSlugTaken
			}
		}
	}
	event.ID = uuid.NewString()
	s.events[event.ID] = *event
	return nil
}

func applyNullable[T any](target **T, update programevent.Nullable[T]) {
	if !update.Set {
		return
	}
	if update.Value == nil {
		*target = nil
		return
	}
	value := *update.Value
	*target = &value
}

// UpdateSettings implements programevent.Store.
func (s *Store) UpdateSettings(_ context.Context, eventID string, update programevent.SettingsUpdate) error {
	if err := s.enter("UpdateSettings"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	event, ok := s.events[eventID]
	if !ok {
		return nil
	}
	if update.Slug != nil && s.slugTaken("event", *update.Slug, eventID) {
		return programevent.ErrSlugTaken
	}
	event.UpdatedAt = update.UpdatedAt
	if update.TypeID != nil {
		event.TypeID = *update.TypeID
	}
	if update.StartsAt != nil {
		event.StartsAt = *update.StartsAt
	}
	applyNullable(&event.EndsAt, update.EndsAt)
	if update.Timezone != nil {
		event.Timezone = *update.Timezone
	}
	if update.AllDay != nil {
		event.AllDay = *update.AllDay
	}
	if update.LocationMode != nil {
		event.LocationMode = *update.LocationMode
	}
	if update.Slug != nil {
		event.Slug = *update.Slug
	}
	applyNullable(&event.MapPlaceID, update.MapPlaceID)
	applyNullable(&event.SeriesID, update.SeriesID)
	applyNullable(&event.SeriesOrder, update.SeriesOrder)
	applyNullable(&event.TicketURL, update.TicketURL)
	applyNullable(&event.StreamURL, update.StreamURL)
	applyNullable(&event.ExternalURL, update.ExternalURL)
	s.events[eventID] = event
	return nil
}

// TouchEvent implements programevent.Store.
func (s *Store) TouchEvent(_ context.Context, eventID string, at time.Time) error {
	if err := s.enter("TouchEvent"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if event, ok := s.events[eventID]; ok {
		event.UpdatedAt = at
		s.events[eventID] = event
	}
	return nil
}

// EventUpdatedAt implements programevent.Store.
func (s *Store) EventUpdatedAt(_ context.Context, eventID string) (time.Time, error) {
	if err := s.enter("EventUpdatedAt"); err != nil {
		return time.Time{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	event, err := s.event(eventID)
	return event.UpdatedAt, err
}

// DeleteEvent implements programevent.Store. The child rows go with the
// event, as their foreign keys cascade.
func (s *Store) DeleteEvent(_ context.Context, eventID string) (bool, error) {
	if err := s.enter("DeleteEvent"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.events[eventID]; !ok {
		return false, nil
	}
	delete(s.events, eventID)
	for key := range s.locales {
		if key.eventID == eventID {
			delete(s.locales, key)
		}
	}
	for id, media := range s.media {
		if media.EventID == eventID {
			delete(s.media, id)
		}
	}
	for id, credit := range s.credits {
		if credit.EventID == eventID {
			delete(s.credits, id)
		}
	}
	for key := range s.relations {
		if key.eventID == eventID {
			delete(s.relations, key)
		}
	}
	return true, nil
}

// matches reports whether value passes the equality filters the fake
// knows: status and slug.
func matches(filters []*commonv1.FilterSpec, values map[string]string) bool {
	for _, filter := range filters {
		if filter == nil {
			continue
		}
		value, known := values[filter.GetField()]
		if !known {
			continue
		}
		switch filter.GetOp() {
		case commonv1.FilterOp_FILTER_OP_EQ:
			if value != filter.GetValue() {
				return false
			}
		case commonv1.FilterOp_FILTER_OP_IN:
			if !slices.Contains(filter.GetValues(), value) {
				return false
			}
		}
	}
	return true
}

func page[T any](rows []T, page programevent.ListPage) []T {
	offset := min(int(page.Offset), len(rows))
	end := min(offset+int(page.Limit), len(rows))
	return slices.Clone(rows[offset:end])
}

// ListEvents implements programevent.Store with the status and slug
// filters, newest start first.
func (s *Store) ListEvents(_ context.Context, list programevent.ListPage) ([]programevent.Event, int64, error) {
	if err := s.enter("ListEvents"); err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []programevent.Event
	for _, event := range s.events {
		if matches(list.Filters, map[string]string{"status": string(event.Status), "slug": event.Slug}) {
			rows = append(rows, event)
		}
	}
	slices.SortFunc(rows, func(a, b programevent.Event) int {
		if c := b.StartsAt.Compare(a.StartsAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return page(rows, list), int64(len(rows)), nil
}

// ContentDocumentID implements programevent.Store.
func (s *Store) ContentDocumentID(_ context.Context, eventID string, _ programevent.RowLock) (*string, error) {
	if err := s.enter("ContentDocumentID"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	event, err := s.event(eventID)
	return event.ContentDocumentID, err
}

// SourceLocale implements programevent.Store.
func (s *Store) SourceLocale(_ context.Context, eventID string, _ programevent.RowLock) (string, error) {
	if err := s.enter("SourceLocale"); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	event, err := s.event(eventID)
	return event.SourceLocale, err
}

// DocumentRoot implements programevent.Store.
func (s *Store) DocumentRoot(_ context.Context, eventID string, _ programevent.RowLock) (programevent.DocumentRoot, error) {
	if err := s.enter("DocumentRoot"); err != nil {
		return programevent.DocumentRoot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	event, err := s.event(eventID)
	if err != nil {
		return programevent.DocumentRoot{}, err
	}
	return programevent.DocumentRoot{
		ID: event.ID, Title: event.Title, ContentDocumentID: event.ContentDocumentID,
		Status: event.Status, SourceLocale: event.SourceLocale,
	}, nil
}

// LockTitle implements programevent.Store.
func (s *Store) LockTitle(_ context.Context, eventID string) (string, string, error) {
	if err := s.enter("LockTitle"); err != nil {
		return "", "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	event, err := s.event(eventID)
	return event.Title, event.SourceLocale, err
}

// Title implements programevent.Store.
func (s *Store) Title(_ context.Context, eventID string) (string, error) {
	if err := s.enter("Title"); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	event, err := s.event(eventID)
	return event.Title, err
}

// SetTitle implements programevent.Store.
func (s *Store) SetTitle(_ context.Context, eventID, title string, updatedAt time.Time) error {
	if err := s.enter("SetTitle"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if event, ok := s.events[eventID]; ok {
		event.Title, event.UpdatedAt = title, updatedAt
		s.events[eventID] = event
	}
	return nil
}

// TranslationSource implements programevent.Store.
func (s *Store) TranslationSource(_ context.Context, eventID, locale string) (string, *string, bool, error) {
	if err := s.enter("TranslationSource"); err != nil {
		return "", nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	event, ok := s.events[eventID]
	row, found := s.locales[localeKey{eventID, locale}]
	if !ok || !found {
		return "", nil, false, nil
	}
	return event.Title, row.Summary, true, nil
}

// CheckRoute implements programevent.Store with the answer
// routeregistry gives.
func (s *Store) CheckRoute(_ context.Context, kind programevent.RouteKind, slug string) error {
	if err := s.enter("CheckRoute"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.routes[routeKey(kind, slug)] {
		entity := "program event"
		if kind == programevent.SeriesRoute {
			entity = "program event series"
		}
		return errs.SlugAlreadyExists(entity, slug)
	}
	return nil
}

// LockRoute implements programevent.Store.
func (s *Store) LockRoute(ctx context.Context, kind programevent.RouteKind, slug string) error {
	if err := s.enter("LockRoute"); err != nil {
		return err
	}
	return s.CheckRoute(ctx, kind, slug)
}

// SeriesOrderTaken implements programevent.Store.
func (s *Store) SeriesOrderTaken(_ context.Context, seriesID string, order int32, excludeEventID string) (bool, error) {
	if err := s.enter("SeriesOrderTaken"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, event := range s.events {
		if strings.TrimSpace(excludeEventID) != "" && id == excludeEventID {
			continue
		}
		if event.SeriesID != nil && *event.SeriesID == seriesID && event.SeriesOrder != nil && *event.SeriesOrder == order {
			return true, nil
		}
	}
	return false, nil
}

// LockSeriesEvents implements programevent.Store, ordered by ID.
func (s *Store) LockSeriesEvents(_ context.Context, seriesID string) ([]string, error) {
	if err := s.enter("LockSeriesEvents"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for id, event := range s.events {
		if event.SeriesID != nil && *event.SeriesID == seriesID {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// DetachFromSeries implements programevent.Store.
func (s *Store) DetachFromSeries(_ context.Context, eventID string, updatedAt time.Time) error {
	if err := s.enter("DetachFromSeries"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if event, ok := s.events[eventID]; ok {
		event.SeriesID, event.SeriesOrder, event.UpdatedAt = nil, nil, updatedAt
		s.events[eventID] = event
	}
	return nil
}

// CountTypeEvents implements programevent.Store.
func (s *Store) CountTypeEvents(_ context.Context, typeID string) (int64, error) {
	if err := s.enter("CountTypeEvents"); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for _, event := range s.events {
		if event.TypeID == typeID {
			count++
		}
	}
	return count, nil
}

// InsertSourceLocale implements programevent.Store.
func (s *Store) InsertSourceLocale(_ context.Context, eventID, locale string, summary *string, at time.Time) error {
	if err := s.enter("InsertSourceLocale"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{eventID, locale}
	if _, exists := s.locales[key]; exists {
		return errors.New("duplicate program_event_translation row")
	}
	s.locales[key] = programevent.LocaleMetadata{Locale: locale, Summary: summary, UpdatedAt: at}
	return nil
}

// LocaleMetadata implements programevent.Store.
func (s *Store) LocaleMetadata(_ context.Context, eventID, locale string, _ bool) (programevent.LocaleMetadata, bool, error) {
	if err := s.enter("LocaleMetadata"); err != nil {
		return programevent.LocaleMetadata{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.locales[localeKey{eventID, locale}]
	return row, ok, nil
}

// LockLocale implements programevent.Store.
func (s *Store) LockLocale(_ context.Context, eventID, locale string) (programevent.LocaleMetadata, error) {
	if err := s.enter("LockLocale"); err != nil {
		return programevent.LocaleMetadata{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.locales[localeKey{eventID, locale}]
	if !ok {
		return programevent.LocaleMetadata{}, programevent.ErrLocaleNotFound
	}
	return row, nil
}

// SetLocaleSummary implements programevent.Store.
func (s *Store) SetLocaleSummary(_ context.Context, eventID, locale string, summary *string, updatedAt time.Time) error {
	if err := s.enter("SetLocaleSummary"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{eventID, locale}
	if row, ok := s.locales[key]; ok {
		row.Summary, row.UpdatedAt = summary, updatedAt
		s.locales[key] = row
	}
	return nil
}

// InsertTargetLocale implements programevent.Store.
func (s *Store) InsertTargetLocale(_ context.Context, eventID, locale string, summary *string, at time.Time) (int64, error) {
	if err := s.enter("InsertTargetLocale"); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{eventID, locale}
	if _, exists := s.locales[key]; exists {
		return 0, errors.New("duplicate program_event_translation row")
	}
	s.locales[key] = programevent.LocaleMetadata{Locale: locale, Summary: summary, UpdatedAt: at}
	return 1, nil
}

// UpdateTargetLocale implements programevent.Store.
func (s *Store) UpdateTargetLocale(_ context.Context, eventID, locale string, summary *string, updatedAt time.Time) (int64, error) {
	if err := s.enter("UpdateTargetLocale"); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{eventID, locale}
	row, ok := s.locales[key]
	if !ok {
		return 0, nil
	}
	row.Summary, row.UpdatedAt = summary, updatedAt
	s.locales[key] = row
	return 1, nil
}

// DeleteTargetLocale implements programevent.Store.
func (s *Store) DeleteTargetLocale(_ context.Context, eventID, locale string) (int64, error) {
	if err := s.enter("DeleteTargetLocale"); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{eventID, locale}
	if _, ok := s.locales[key]; !ok {
		return 0, nil
	}
	delete(s.locales, key)
	return 1, nil
}

// LocaleSummaries implements programevent.Store.
func (s *Store) LocaleSummaries(_ context.Context, eventID string) ([]programevent.LocaleSummary, error) {
	if err := s.enter("LocaleSummaries"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	event, ok := s.events[eventID]
	if !ok {
		return []programevent.LocaleSummary{}, nil
	}
	rows := []programevent.LocaleSummary{}
	for key, row := range s.locales {
		if key.eventID == eventID {
			rows = append(rows, programevent.LocaleSummary{Locale: row.Locale, Summary: row.Summary})
		}
	}
	slices.SortFunc(rows, func(a, b programevent.LocaleSummary) int {
		aSource, bSource := a.Locale == event.SourceLocale, b.Locale == event.SourceLocale
		if aSource != bSource {
			if aSource {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Locale, b.Locale)
	})
	return rows, nil
}

// LocaleSummary implements programevent.Store.
func (s *Store) LocaleSummary(_ context.Context, eventID, locale string) (*string, bool, error) {
	if err := s.enter("LocaleSummary"); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.locales[localeKey{eventID, locale}]
	return row.Summary, ok, nil
}

// roleMedia lists the relations of a role by sort order, then creation.
func (s *Store) roleMedia(eventID, role string) []programevent.Media {
	var rows []programevent.Media
	for _, media := range s.media {
		if media.EventID == eventID && media.Role == role {
			rows = append(rows, media)
		}
	}
	slices.SortFunc(rows, func(a, b programevent.Media) int {
		return cmp.Or(cmp.Compare(a.SortOrder, b.SortOrder), a.CreatedAt.Compare(b.CreatedAt))
	})
	return rows
}

// errDuplicatePrimary is the unique violation of a second primary relation
// of one role.
var errDuplicatePrimary = errors.New("duplicate primary program_event_media of a role")

// primaryTaken reports whether a relation other than exceptID is the
// role's primary.
func (s *Store) primaryTaken(eventID, role, exceptID string) bool {
	for id, media := range s.media {
		if id != exceptID && media.EventID == eventID && media.Role == role && media.IsPrimary {
			return true
		}
	}
	return false
}

// primaryFirst orders relations primary first, then by sort order and
// creation.
func primaryFirst(a, b programevent.Media) int {
	if a.IsPrimary != b.IsPrimary {
		if a.IsPrimary {
			return -1
		}
		return 1
	}
	return cmp.Or(cmp.Compare(a.SortOrder, b.SortOrder), a.CreatedAt.Compare(b.CreatedAt))
}

// MediaOfFile implements programevent.Store.
func (s *Store) MediaOfFile(_ context.Context, eventID, role, fileID string) (programevent.Media, bool, error) {
	if err := s.enter("MediaOfFile"); err != nil {
		return programevent.Media{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, media := range s.roleMedia(eventID, role) {
		if media.FileID == fileID {
			return media, true, nil
		}
	}
	return programevent.Media{}, false, nil
}

// MediaByID implements programevent.Store.
func (s *Store) MediaByID(_ context.Context, eventID, mediaID string) (programevent.Media, error) {
	if err := s.enter("MediaByID"); err != nil {
		return programevent.Media{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	media, ok := s.media[mediaID]
	if !ok || media.EventID != eventID {
		return programevent.Media{}, programevent.ErrMediaNotFound
	}
	return media, nil
}

// PrimaryMediaFileID implements programevent.Store.
func (s *Store) PrimaryMediaFileID(_ context.Context, eventID, role string) (string, error) {
	if err := s.enter("PrimaryMediaFileID"); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := s.roleMedia(eventID, role)
	if len(rows) == 0 {
		return "", nil
	}
	slices.SortStableFunc(rows, primaryFirst)
	return rows[0].FileID, nil
}

// PosterFileIDs implements programevent.Store.
func (s *Store) PosterFileIDs(_ context.Context, eventIDs []string) (map[string]string, error) {
	if err := s.enter("PosterFileIDs"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[string]string, len(eventIDs))
	for _, eventID := range eventIDs {
		rows := s.roleMedia(eventID, "poster")
		if len(rows) == 0 {
			continue
		}
		slices.SortStableFunc(rows, primaryFirst)
		result[eventID] = rows[0].FileID
	}
	return result, nil
}

// HasPrimaryMedia implements programevent.Store.
func (s *Store) HasPrimaryMedia(_ context.Context, eventID, role string) (bool, error) {
	if err := s.enter("HasPrimaryMedia"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, media := range s.roleMedia(eventID, role) {
		if media.IsPrimary {
			return true, nil
		}
	}
	return false, nil
}

// ClearPrimaryMedia implements programevent.Store.
func (s *Store) ClearPrimaryMedia(_ context.Context, eventID, role string, updatedAt time.Time) error {
	if err := s.enter("ClearPrimaryMedia"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, media := range s.roleMedia(eventID, role) {
		media.IsPrimary, media.UpdatedAt = false, updatedAt
		s.media[media.ID] = media
	}
	return nil
}

// UpdateMedia implements programevent.Store.
func (s *Store) UpdateMedia(_ context.Context, mediaID string, alt, caption *string, makePrimary bool, updatedAt time.Time) error {
	if err := s.enter("UpdateMedia"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	media, ok := s.media[mediaID]
	if !ok {
		return nil
	}
	if makePrimary && !media.IsPrimary && s.primaryTaken(media.EventID, media.Role, mediaID) {
		return errDuplicatePrimary
	}
	media.Alt, media.Caption, media.UpdatedAt = alt, caption, updatedAt
	if makePrimary {
		media.IsPrimary = true
	}
	s.media[mediaID] = media
	return nil
}

// NextMediaSortOrder implements programevent.Store.
func (s *Store) NextMediaSortOrder(_ context.Context, eventID, role string) (int32, error) {
	if err := s.enter("NextMediaSortOrder"); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := int32(0)
	for _, media := range s.roleMedia(eventID, role) {
		next = max(next, media.SortOrder+1)
	}
	return next, nil
}

// InsertMedia implements programevent.Store. Its created_at orders it
// after every relation stored before it.
func (s *Store) InsertMedia(_ context.Context, media *programevent.Media) error {
	if err := s.enter("InsertMedia"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.events[media.EventID]; !ok {
		return errors.New("program_event_media references a missing event")
	}
	for _, other := range s.media {
		if other.EventID == media.EventID && other.Role == media.Role && other.FileID == media.FileID {
			return errors.New("duplicate program_event_media row")
		}
	}
	if media.IsPrimary && s.primaryTaken(media.EventID, media.Role, "") {
		return errDuplicatePrimary
	}
	media.ID = uuid.NewString()
	if media.CreatedAt.IsZero() {
		media.CreatedAt = s.tick()
		media.UpdatedAt = media.CreatedAt
	}
	s.media[media.ID] = *media
	return nil
}

// DeleteMedia implements programevent.Store.
func (s *Store) DeleteMedia(_ context.Context, eventID, mediaID string) (bool, error) {
	if err := s.enter("DeleteMedia"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	media, ok := s.media[mediaID]
	if !ok || media.EventID != eventID {
		return false, nil
	}
	delete(s.media, mediaID)
	return true, nil
}

// FirstMedia implements programevent.Store.
func (s *Store) FirstMedia(_ context.Context, eventID, role string) (programevent.Media, bool, error) {
	if err := s.enter("FirstMedia"); err != nil {
		return programevent.Media{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := s.roleMedia(eventID, role)
	if len(rows) == 0 {
		return programevent.Media{}, false, nil
	}
	return rows[0], true, nil
}

// MarkMediaPrimary implements programevent.Store.
func (s *Store) MarkMediaPrimary(_ context.Context, mediaID string, updatedAt time.Time) error {
	if err := s.enter("MarkMediaPrimary"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if media, ok := s.media[mediaID]; ok {
		if s.primaryTaken(media.EventID, media.Role, mediaID) {
			return errDuplicatePrimary
		}
		media.IsPrimary, media.UpdatedAt = true, updatedAt
		s.media[mediaID] = media
	}
	return nil
}

// RoleMedia implements programevent.Store.
func (s *Store) RoleMedia(_ context.Context, eventID, role string) ([]programevent.Media, error) {
	if err := s.enter("RoleMedia"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.roleMedia(eventID, role), nil
}

// PlaceMedia implements programevent.Store.
func (s *Store) PlaceMedia(_ context.Context, eventID, role, mediaID string, position int32, updatedAt time.Time) (bool, error) {
	if err := s.enter("PlaceMedia"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	media, ok := s.media[mediaID]
	if !ok || media.EventID != eventID || media.Role != role {
		return false, nil
	}
	if position == 0 && s.primaryTaken(eventID, role, mediaID) {
		return false, errDuplicatePrimary
	}
	media.SortOrder, media.IsPrimary, media.UpdatedAt = position, position == 0, updatedAt
	s.media[mediaID] = media
	return true, nil
}

// DeleteRoleMedia implements programevent.Store.
func (s *Store) DeleteRoleMedia(_ context.Context, eventID, role string) ([]programevent.Media, error) {
	if err := s.enter("DeleteRoleMedia"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := s.roleMedia(eventID, role)
	for _, media := range rows {
		delete(s.media, media.ID)
	}
	return rows, nil
}

// EventMedia implements programevent.Store.
func (s *Store) EventMedia(_ context.Context, eventID string) ([]programevent.Media, error) {
	if err := s.enter("EventMedia"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := []programevent.Media{}
	for _, media := range s.media {
		if media.EventID == eventID {
			rows = append(rows, media)
		}
	}
	slices.SortFunc(rows, func(a, b programevent.Media) int {
		return cmp.Or(strings.Compare(a.Role, b.Role), primaryFirst(a, b))
	})
	return rows, nil
}

// credits lists the event's credits by order.
func (s *Store) creditsBy(eventID string, compare func(a, b programevent.CreditRecord) int) []programevent.CreditRecord {
	rows := []programevent.CreditRecord{}
	for _, credit := range s.credits {
		if credit.EventID == eventID {
			rows = append(rows, credit)
		}
	}
	slices.SortFunc(rows, compare)
	return rows
}

func byPositionAndCreation(a, b programevent.CreditRecord) int {
	return cmp.Or(cmp.Compare(a.SortOrder, b.SortOrder), a.CreatedAt.Compare(b.CreatedAt))
}

func byPositionAndID(a, b programevent.CreditRecord) int {
	return cmp.Or(cmp.Compare(a.SortOrder, b.SortOrder), strings.Compare(a.ID, b.ID))
}

// NextCreditSortOrder implements programevent.Store.
func (s *Store) NextCreditSortOrder(_ context.Context, eventID string) (int32, error) {
	if err := s.enter("NextCreditSortOrder"); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := int32(0)
	for _, credit := range s.creditsBy(eventID, byPositionAndCreation) {
		next = max(next, credit.SortOrder+1)
	}
	return next, nil
}

// InsertCredit implements programevent.Store.
func (s *Store) InsertCredit(_ context.Context, credit *programevent.CreditRecord) error {
	if err := s.enter("InsertCredit"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.events[credit.EventID]; !ok {
		return errors.New("program_event_credit references a missing event")
	}
	credit.ID = uuid.NewString()
	if credit.CreatedAt.IsZero() {
		credit.CreatedAt = s.tick()
	}
	s.credits[credit.ID] = *credit
	return nil
}

// Credit implements programevent.Store.
func (s *Store) Credit(_ context.Context, eventID, creditID string) (programevent.CreditRecord, error) {
	if err := s.enter("Credit"); err != nil {
		return programevent.CreditRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	credit, ok := s.credits[creditID]
	if !ok || credit.EventID != eventID {
		return programevent.CreditRecord{}, programevent.ErrCreditNotFound
	}
	return credit, nil
}

// UpdateCredit implements programevent.Store.
func (s *Store) UpdateCredit(_ context.Context, creditID string, update programevent.CreditUpdate) error {
	if err := s.enter("UpdateCredit"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	credit, ok := s.credits[creditID]
	if !ok {
		return nil
	}
	applyNullable(&credit.ArtistID, update.ArtistID)
	applyNullable(&credit.MemberID, update.MemberID)
	applyNullable(&credit.DisplayName, update.DisplayName)
	applyNullable(&credit.CreditRole, update.CreditRole)
	applyNullable(&credit.Description, update.Description)
	if update.SortOrder != nil {
		credit.SortOrder = *update.SortOrder
	}
	s.credits[creditID] = credit
	return nil
}

// DeleteCredit implements programevent.Store.
func (s *Store) DeleteCredit(_ context.Context, eventID, creditID string) (bool, error) {
	if err := s.enter("DeleteCredit"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	credit, ok := s.credits[creditID]
	if !ok || credit.EventID != eventID {
		return false, nil
	}
	delete(s.credits, creditID)
	return true, nil
}

// RemoveCredit implements programevent.Store.
func (s *Store) RemoveCredit(_ context.Context, creditID string) error {
	if err := s.enter("RemoveCredit"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.credits, creditID)
	return nil
}

// Credits implements programevent.Store.
func (s *Store) Credits(_ context.Context, eventID string) ([]programevent.CreditRecord, error) {
	if err := s.enter("Credits"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.creditsBy(eventID, byPositionAndCreation), nil
}

// LockCredits implements programevent.Store.
func (s *Store) LockCredits(ctx context.Context, eventID string) ([]programevent.CreditRecord, error) {
	if err := s.enter("LockCredits"); err != nil {
		return nil, err
	}
	return s.Credits(ctx, eventID)
}

// CreditsByID implements programevent.Store.
func (s *Store) CreditsByID(_ context.Context, eventID string) ([]programevent.CreditRecord, error) {
	if err := s.enter("CreditsByID"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.creditsBy(eventID, byPositionAndID), nil
}

// CountCredits implements programevent.Store.
func (s *Store) CountCredits(_ context.Context, eventID string, creditIDs []string) (int64, error) {
	if err := s.enter("CountCredits"); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for _, id := range slices.Compact(slices.Sorted(slices.Values(creditIDs))) {
		if credit, ok := s.credits[id]; ok && credit.EventID == eventID {
			count++
		}
	}
	return count, nil
}

// PlaceCredit implements programevent.Store.
func (s *Store) PlaceCredit(_ context.Context, eventID, creditID string, position int) error {
	if err := s.enter("PlaceCredit"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if credit, ok := s.credits[creditID]; ok && credit.EventID == eventID {
		credit.SortOrder = int32(position)
		s.credits[creditID] = credit
	}
	return nil
}

// LockCreditMembers implements programevent.Store with the answers of
// authorizationtarget.LockReferences.
func (s *Store) LockCreditMembers(_ context.Context, references []programevent.CreditMemberReference) error {
	if err := s.enter("LockCreditMembers"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, reference := range references {
		if _, err := uuid.Parse(reference.MemberID); err != nil {
			return domainerr.InvalidField(reference.Field, "must be a canonical UUID")
		}
		if !s.members[reference.MemberID] {
			return domainerr.InvalidField(reference.Field, "not found: "+reference.MemberID)
		}
	}
	return nil
}

// CreditArtist implements programevent.Store.
func (s *Store) CreditArtist(_ context.Context, artistID string) (programevent.CreditArtist, error) {
	if err := s.enter("CreditArtist"); err != nil {
		return programevent.CreditArtist{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.CreditArtists[artistID], nil
}

// ArtistImageFileID implements programevent.Store.
func (s *Store) ArtistImageFileID(_ context.Context, artistID string) (string, error) {
	if err := s.enter("ArtistImageFileID"); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ArtistImages[artistID], nil
}

// Relations implements programevent.Store.
func (s *Store) Relations(_ context.Context, kind programevent.RelationKind, eventID string) ([]programevent.Relation, error) {
	if err := s.enter("Relations"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := slices.Clone(s.relations[relationKey{kind, eventID}])
	if rows == nil {
		rows = []programevent.Relation{}
	}
	slices.SortFunc(rows, func(a, b programevent.Relation) int {
		return cmp.Or(cmp.Compare(a.SortOrder, b.SortOrder), strings.Compare(a.ID, b.ID))
	})
	return rows, nil
}

// DeleteRelations implements programevent.Store.
func (s *Store) DeleteRelations(_ context.Context, kind programevent.RelationKind, eventID string) error {
	if err := s.enter("DeleteRelations"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.relations, relationKey{kind, eventID})
	return nil
}

func (s *Store) insertRelation(kind programevent.RelationKind, eventID string, relation programevent.Relation) error {
	key := relationKey{kind, eventID}
	for _, existing := range s.relations[key] {
		if existing.ID == relation.ID {
			return errors.New("duplicate program event relation")
		}
	}
	s.relations[key] = append(s.relations[key], relation)
	return nil
}

// InsertRelations implements programevent.Store.
func (s *Store) InsertRelations(_ context.Context, kind programevent.RelationKind, eventID string, relations []programevent.Relation, _ time.Time) error {
	if err := s.enter("InsertRelations"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, relation := range relations {
		if err := s.insertRelation(kind, eventID, relation); err != nil {
			return err
		}
	}
	return nil
}

// InsertRelation implements programevent.Store.
func (s *Store) InsertRelation(_ context.Context, kind programevent.RelationKind, eventID string, relation programevent.Relation) error {
	if err := s.enter("InsertRelation"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.insertRelation(kind, eventID, relation)
}

// DeleteRelation implements programevent.Store.
func (s *Store) DeleteRelation(_ context.Context, kind programevent.RelationKind, eventID, relatedID string) (int64, error) {
	if err := s.enter("DeleteRelation"); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := relationKey{kind, eventID}
	rows := s.relations[key]
	for index, relation := range rows {
		if relation.ID == relatedID {
			s.relations[key] = slices.Delete(slices.Clone(rows), index, index+1)
			return 1, nil
		}
	}
	return 0, nil
}

// SetRelationRole implements programevent.Store.
func (s *Store) SetRelationRole(_ context.Context, kind programevent.RelationKind, eventID, relatedID string, role *string) (int64, error) {
	if err := s.enter("SetRelationRole"); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := relationKey{kind, eventID}
	rows := slices.Clone(s.relations[key])
	for index := range rows {
		if rows[index].ID == relatedID {
			rows[index].Role = role
			s.relations[key] = rows
			return 1, nil
		}
	}
	return 0, nil
}

func (s *Store) seriesRow(seriesID string) (programevent.Series, error) {
	series, ok := s.series[seriesID]
	if !ok {
		return programevent.Series{}, programevent.ErrSeriesNotFound
	}
	return series, nil
}

// InsertSeries implements programevent.Store.
func (s *Store) InsertSeries(_ context.Context, series *programevent.Series) error {
	if err := s.enter("InsertSeries"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.slugTaken("series", series.Slug, "") {
		return programevent.ErrSlugTaken
	}
	series.ID = uuid.NewString()
	s.series[series.ID] = *series
	return nil
}

// Series implements programevent.Store.
func (s *Store) Series(_ context.Context, seriesID string, _ programevent.RowLock) (programevent.Series, error) {
	if err := s.enter("Series"); err != nil {
		return programevent.Series{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seriesRow(seriesID)
}

// SeriesBySlug implements programevent.Store.
func (s *Store) SeriesBySlug(_ context.Context, slug string) (programevent.Series, error) {
	if err := s.enter("SeriesBySlug"); err != nil {
		return programevent.Series{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, series := range s.series {
		if series.Slug == slug {
			return series, nil
		}
	}
	return programevent.Series{}, programevent.ErrSeriesNotFound
}

// LockSeriesID implements programevent.Store.
func (s *Store) LockSeriesID(_ context.Context, seriesID string) error {
	if err := s.enter("LockSeriesID"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.seriesRow(seriesID)
	return err
}

// UpdateSeries implements programevent.Store.
func (s *Store) UpdateSeries(_ context.Context, seriesID string, change programevent.SeriesChange, updatedAt time.Time) error {
	if err := s.enter("UpdateSeries"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	series, ok := s.series[seriesID]
	if !ok {
		return nil
	}
	if change.Slug != nil && s.slugTaken("series", *change.Slug, seriesID) {
		return programevent.ErrSlugTaken
	}
	series.UpdatedAt = updatedAt
	if change.Title != nil {
		series.Title = *change.Title
	}
	if change.Summary != nil {
		value := *change.Summary
		series.Summary = &value
	}
	if change.Description != nil {
		value := *change.Description
		series.Description = &value
	}
	if change.Slug != nil {
		series.Slug = *change.Slug
	}
	if change.Status != nil {
		series.Status = *change.Status
	}
	if change.PosterChanged {
		series.PosterFileID = change.PosterFileID
	}
	s.series[seriesID] = series
	return nil
}

// DeleteSeries implements programevent.Store.
func (s *Store) DeleteSeries(_ context.Context, seriesID string) (bool, error) {
	if err := s.enter("DeleteSeries"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.series[seriesID]; !ok {
		return false, nil
	}
	delete(s.series, seriesID)
	return true, nil
}

// ListSeries implements programevent.Store with the status filter, most
// recently updated first.
func (s *Store) ListSeries(_ context.Context, list programevent.ListPage) ([]programevent.Series, int64, error) {
	if err := s.enter("ListSeries"); err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []programevent.Series
	for _, series := range s.series {
		if matches(list.Filters, map[string]string{"status": string(series.Status)}) {
			rows = append(rows, series)
		}
	}
	slices.SortFunc(rows, func(a, b programevent.Series) int {
		return cmp.Or(b.UpdatedAt.Compare(a.UpdatedAt), strings.Compare(a.ID, b.ID))
	})
	return page(rows, list), int64(len(rows)), nil
}

// InsertType implements programevent.Store.
func (s *Store) InsertType(_ context.Context, eventType *programevent.Type) error {
	if err := s.enter("InsertType"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.slugTaken("type", eventType.Slug, "") {
		return programevent.ErrSlugTaken
	}
	eventType.ID = uuid.NewString()
	s.types[eventType.ID] = *eventType
	return nil
}

// InsertTypeLocale implements programevent.Store.
func (s *Store) InsertTypeLocale(_ context.Context, locale programevent.TypeLocale) error {
	if err := s.enter("InsertTypeLocale"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := typeLocaleKey{locale.TypeID, locale.Locale}
	if _, exists := s.typeLocales[key]; exists {
		return errors.New("duplicate program_event_type_locale row")
	}
	s.typeLocales[key] = locale
	return nil
}

// Type implements programevent.Store.
func (s *Store) Type(_ context.Context, typeID string, _ programevent.RowLock) (programevent.Type, error) {
	if err := s.enter("Type"); err != nil {
		return programevent.Type{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	eventType, ok := s.types[typeID]
	if !ok {
		return programevent.Type{}, programevent.ErrTypeNotFound
	}
	return eventType, nil
}

// UpdateType implements programevent.Store.
func (s *Store) UpdateType(_ context.Context, typeID string, change programevent.TypeChange, updatedAt time.Time) error {
	if err := s.enter("UpdateType"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	eventType, ok := s.types[typeID]
	if !ok {
		return nil
	}
	if change.Slug != nil && s.slugTaken("type", *change.Slug, typeID) {
		return programevent.ErrSlugTaken
	}
	eventType.UpdatedAt = updatedAt
	if change.Slug != nil {
		eventType.Slug = *change.Slug
	}
	if change.Status != nil {
		eventType.Status = *change.Status
	}
	if change.SortOrder != nil {
		eventType.SortOrder = *change.SortOrder
	}
	if change.RequiresPlace != nil {
		eventType.RequiresPlace = *change.RequiresPlace
	}
	if change.RequiresStreamURL != nil {
		eventType.RequiresStreamURL = *change.RequiresStreamURL
	}
	s.types[typeID] = eventType
	return nil
}

// TypeLocale implements programevent.Store.
func (s *Store) TypeLocale(_ context.Context, typeID, locale string) (programevent.TypeLocale, bool, error) {
	if err := s.enter("TypeLocale"); err != nil {
		return programevent.TypeLocale{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.typeLocales[typeLocaleKey{typeID, locale}]
	return row, ok, nil
}

// UpdateTypeLocale implements programevent.Store.
func (s *Store) UpdateTypeLocale(_ context.Context, typeID, locale string, change programevent.TypeLocaleChange, updatedAt time.Time) error {
	if err := s.enter("UpdateTypeLocale"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := typeLocaleKey{typeID, locale}
	row, ok := s.typeLocales[key]
	if !ok {
		return nil
	}
	if change.Name != nil {
		row.Name = *change.Name
	}
	applyNullable(&row.Description, change.Description)
	row.UpdatedAt = updatedAt
	s.typeLocales[key] = row
	return nil
}

// DeleteType implements programevent.Store; an event of the type is the
// foreign key violation PostgreSQL answers.
func (s *Store) DeleteType(_ context.Context, typeID string) error {
	if err := s.enter("DeleteType"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, event := range s.events {
		if event.TypeID == typeID {
			return programevent.ErrTypeInUse
		}
	}
	delete(s.types, typeID)
	for key := range s.typeLocales {
		if key.typeID == typeID {
			delete(s.typeLocales, key)
		}
	}
	return nil
}

// TypeLocales implements programevent.Store.
func (s *Store) TypeLocales(_ context.Context, typeID string) ([]programevent.TypeLocale, error) {
	if err := s.enter("TypeLocales"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := []programevent.TypeLocale{}
	for key, row := range s.typeLocales {
		if key.typeID == typeID {
			rows = append(rows, row)
		}
	}
	slices.SortFunc(rows, func(a, b programevent.TypeLocale) int { return strings.Compare(a.Locale, b.Locale) })
	return rows, nil
}

// ListTypes implements programevent.Store with the status filter, by sort
// order and slug.
func (s *Store) ListTypes(_ context.Context, list programevent.ListPage) ([]programevent.Type, int64, error) {
	if err := s.enter("ListTypes"); err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []programevent.Type
	for _, eventType := range s.types {
		if matches(list.Filters, map[string]string{"status": string(eventType.Status)}) {
			rows = append(rows, eventType)
		}
	}
	slices.SortFunc(rows, func(a, b programevent.Type) int {
		return cmp.Or(cmp.Compare(a.SortOrder, b.SortOrder), strings.Compare(a.Slug, b.Slug))
	})
	return page(rows, list), int64(len(rows)), nil
}

// LockAttachableFiles implements programevent.Store.
func (s *Store) LockAttachableFiles(_ context.Context, fileIDs []string) error {
	if err := s.enter("LockAttachableFiles"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, fileID := range fileIDs {
		if s.AttachableFiles != nil && !s.AttachableFiles[fileID] {
			return domainerr.ResourceNotFound("file", fileID)
		}
	}
	s.lockedFiles = append(s.lockedFiles, fileIDs...)
	return nil
}

func (s *Store) bind(owner, ownerID, key, fileID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindings[bindingKey{owner, ownerID, key}] = Binding{FileID: fileID}
}

func (s *Store) release(owner, ownerID, prefix string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.bindings {
		if key.owner == owner && key.ownerID == ownerID && strings.HasPrefix(key.key, prefix) {
			delete(s.bindings, key)
		}
	}
}

// BindPosterMedia implements programevent.Store.
func (s *Store) BindPosterMedia(_ context.Context, eventID, mediaID, fileID string) error {
	if err := s.enter("BindPosterMedia"); err != nil {
		return err
	}
	s.bind("program_event", eventID, "media_"+mediaID, fileID)
	return nil
}

// ReleaseMediaBinding implements programevent.Store.
func (s *Store) ReleaseMediaBinding(_ context.Context, eventID, mediaID string) error {
	if err := s.enter("ReleaseMediaBinding"); err != nil {
		return err
	}
	s.release("program_event", eventID, "media_"+mediaID)
	return nil
}

// ReleaseEventMedia implements programevent.Store.
func (s *Store) ReleaseEventMedia(_ context.Context, eventID string) error {
	if err := s.enter("ReleaseEventMedia"); err != nil {
		return err
	}
	s.release("program_event", eventID, "media")
	return nil
}

// BindSeriesPoster implements programevent.Store.
func (s *Store) BindSeriesPoster(_ context.Context, seriesID, fileID string) error {
	if err := s.enter("BindSeriesPoster"); err != nil {
		return err
	}
	s.bind("program_event_series", seriesID, "poster", fileID)
	return nil
}

// ReleaseSeriesPoster implements programevent.Store.
func (s *Store) ReleaseSeriesPoster(_ context.Context, seriesID string) error {
	if err := s.enter("ReleaseSeriesPoster"); err != nil {
		return err
	}
	s.release("program_event_series", seriesID, "poster")
	return nil
}

// ReadyImageAsset implements programevent.Store.
func (s *Store) ReadyImageAsset(_ context.Context, fileID string) (*mediaasset.Reference, error) {
	if err := s.enter("ReadyImageAsset"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ReadyImages[fileID], nil
}

// InlineImageAsset implements programevent.Store.
func (s *Store) InlineImageAsset(_ context.Context, fileID string) (*mediaasset.Reference, error) {
	if err := s.enter("InlineImageAsset"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, ok := s.InlineImages[fileID]
	if !ok {
		return nil, errors.New("program event Content Block image File has 0 ready public assets")
	}
	return asset, nil
}

// HydrateBlockMedia implements programevent.Store; the fake marks each item
// served by recording the principal.
func (s *Store) HydrateBlockMedia(_ context.Context, _ string, _ uuid.UUID, principal *auth.UserInfo, items []*fileref.BlockMediaItem) ([]*fileref.BlockMediaItem, error) {
	if err := s.enter("HydrateBlockMedia"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Hydrated = append(s.Hydrated, principal)
	return items, nil
}

// ResolveSessionPrincipal implements programevent.Store.
func (s *Store) ResolveSessionPrincipal(_ context.Context, sessionID string) (*auth.UserInfo, error) {
	if err := s.enter("ResolveSessionPrincipal"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	principal, ok := s.Sessions[sessionID]
	if !ok {
		return nil, auth.ErrSessionPrincipalInvalid
	}
	return principal, nil
}

// LockSession implements programevent.Store.
func (s *Store) LockSession(_ context.Context, sessionID string, principal *auth.UserInfo) error {
	if err := s.enter("LockSession"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.Sessions[sessionID]
	if !ok || current.IdentityID != principal.IdentityID {
		return auth.ErrSessionPrincipalInvalid
	}
	return nil
}

// LockActivePrincipal implements programevent.Store through Fence.
func (s *Store) LockActivePrincipal(ctx context.Context, principal *auth.UserInfo) (bool, error) {
	if err := s.enter("LockActivePrincipal"); err != nil {
		return false, err
	}
	return s.Fence.LockActivePrincipal(ctx, nil, principal)
}

// RequireCurrentContributors implements programevent.Store with the denial
// the collaboration checkpoint answers.
func (s *Store) RequireCurrentContributors(_ context.Context, eventID string, contributors []string) error {
	if err := s.enter("RequireCurrentContributors"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, contributor := range contributors {
		if slices.Contains(s.RevokedContributors[eventID], contributor) {
			return domainerr.Denied("collaboration contributor no longer has permission")
		}
	}
	return nil
}

// LockMembers implements programevent.Store.
func (s *Store) LockMembers(_ context.Context, memberIDs []string) (int, error) {
	if err := s.enter("LockMembers"); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, memberID := range memberIDs {
		if s.members[memberID] {
			count++
		}
	}
	return count, nil
}

// PreferredLocale implements programevent.Store.
func (s *Store) PreferredLocale(_ context.Context, memberID string) (*string, error) {
	if err := s.enter("PreferredLocale"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	locale, ok := s.PreferredLocales[memberID]
	if !ok {
		return nil, nil
	}
	return &locale, nil
}

// TranslationSettings implements programevent.Store.
func (s *Store) TranslationSettings(context.Context) (translation.RuntimeSettings, error) {
	if err := s.enter("TranslationSettings"); err != nil {
		return translation.DefaultRuntimeSettings(), err
	}
	return s.Settings, nil
}

// PublishContentUpdate implements programevent.Store by recording update.
func (s *Store) PublishContentUpdate(_ context.Context, update programevent.ContentUpdate) error {
	if err := s.enter("PublishContentUpdate"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, update)
	return nil
}

// ContentFileMIMEType implements programevent.Store.
func (s *Store) ContentFileMIMEType(_ context.Context, blockID uuid.UUID, referencePath string, fileID uuid.UUID) (string, bool, error) {
	if err := s.enter("ContentFileMIMEType"); err != nil {
		return "", false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mimeType, ok := s.ContentFiles[contentFileKey{blockID, referencePath, fileID}]
	return mimeType, ok, nil
}

// SetContentFile makes a Block reference name an active File of mimeType.
func (s *Store) SetContentFile(blockID uuid.UUID, referencePath string, fileID uuid.UUID, mimeType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ContentFiles == nil {
		s.ContentFiles = map[contentFileKey]string{}
	}
	s.ContentFiles[contentFileKey{blockID, referencePath, fileID}] = mimeType
}

// SiteVariables implements programevent.Store.
func (s *Store) SiteVariables(context.Context) (sitevariable.Context, error) {
	if err := s.enter("SiteVariables"); err != nil {
		return sitevariable.Context{}, err
	}
	return s.Site, nil
}
