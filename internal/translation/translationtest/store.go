// Package translationtest is the in-memory Translation Store, its fake
// owning-domain registry, and the Store contract both the fake and the
// PostgreSQL store keep.
package translationtest

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
)

// Store is the in-memory Translation Store. It knows no schema and takes no
// lock; the contract pins what it shares with PostgreSQL.
type Store struct {
	mu        sync.Mutex
	settings  *translation.RuntimeSettings
	locales   []application.Locale
	entities  map[string]*Entity
	signals   []Signal
	domains   *Domains
	now       func() time.Time
	signalErr error
}

// Entity is one translatable root with its Content Document and stored
// locale entries.
type Entity struct {
	EntityType       string
	EntityID         string
	SourceLocale     string
	DocumentID       uuid.UUID
	DocumentRevision uuid.UUID
	Entries          map[string]application.Entry
	// BlockLocales are the locales the Content Blocks store.
	BlockLocales []string
	// Source is what the owning domain's loader answers.
	Source *translation.SourceDocument
}

// Signal is one source-locale content.updated the Store scheduled.
type Signal struct {
	EntityType       string
	EntityID         string
	DocumentRevision string
}

// New returns an empty Store and its unit of work for the use cases.
func New() (*Store, *mutationtest.UnitOfWork[application.Store]) {
	store := NewStore()
	return store, mutationtest.New[application.Store](store)
}

// NewStore returns an empty Store with every catalog locale enabled.
func NewStore() *Store {
	store := &Store{
		entities: map[string]*Entity{}, now: func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) },
	}
	store.domains = &Domains{store: store}
	for index, code := range []string{"en", "ko", "ja", "fr"} {
		store.locales = append(store.locales, application.Locale{
			Code: code, DisplayName: code, Enabled: true, IsPublic: true, Dir: "ltr", SortOrder: int32(index + 1),
		})
	}
	return store
}

// FakeDomains is the fake owning-domain registry, for a test to configure.
func (s *Store) FakeDomains() *Domains { return s.domains }

// Savepoint captures the persisted fields and the rollback of the Store.
func (s *Store) Savepoint() mutationtest.Checkpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings := mutationtest.ClonePersisted(s.settings)
	entities := mutationtest.ClonePersisted(s.entities)
	signals := mutationtest.ClonePersisted(s.signals)
	return mutationtest.Checkpoint{
		State: map[string]any{
			"settings": settings,
			"entities": entities,
			"signals":  signals,
		},
		Restore: func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.settings = settings
			s.entities = entities
			s.signals = signals
		},
	}
}

func entityKey(entityType, entityID string) string { return entityType + "/" + entityID }

// SeedEntity stores entity, its Content Document and its source entry.
func (s *Store) SeedEntity(entity Entity) *Entity {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entity.DocumentID == uuid.Nil {
		entity.DocumentID = uuid.New()
	}
	if entity.DocumentRevision == uuid.Nil {
		entity.DocumentRevision = uuid.New()
	}
	if entity.Entries == nil {
		entity.Entries = map[string]application.Entry{}
	}
	if _, ok := entity.Entries[entity.SourceLocale]; !ok && entity.SourceLocale != "" {
		entity.Entries[entity.SourceLocale] = application.Entry{
			EntityType: entity.EntityType, EntityID: entity.EntityID, Locale: entity.SourceLocale, UpdatedAt: s.now(),
		}
	}
	stored := entity
	s.entities[entityKey(entity.EntityType, entity.EntityID)] = &stored
	return &stored
}

// DeleteEntity removes a seeded entity, as its owning domain would.
func (s *Store) DeleteEntity(entityType, entityID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entities, entityKey(entityType, entityID))
}

// FailSignals makes every content.updated schedule answer err.
func (s *Store) FailSignals(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.signalErr = err
}

// Entity returns a copy of a seeded entity.
func (s *Store) Entity(entityType, entityID string) (Entity, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entity, ok := s.entities[entityKey(entityType, entityID)]
	if !ok {
		return Entity{}, false
	}
	copied := *entity
	copied.Entries = maps.Clone(entity.Entries)
	return copied, true
}

// SeedSettings stores the settings singleton.
func (s *Store) SeedSettings(settings translation.RuntimeSettings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if settings.UpdatedAt == nil {
		now := s.now()
		settings.UpdatedAt = &now
	}
	s.settings = &settings
}

// Settings returns the stored settings singleton, if any.
func (s *Store) Settings() (translation.RuntimeSettings, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		return translation.RuntimeSettings{}, false
	}
	return *s.settings, true
}

// SetLocales replaces the runtime locale catalog.
func (s *Store) SetLocales(locales ...application.Locale) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.locales = slices.Clone(locales)
}

// Signals returns the scheduled content.updated signals.
func (s *Store) Signals() []Signal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.signals)
}

func (s *Store) RuntimeSettings(_ context.Context) (translation.RuntimeSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		defaults := translation.DefaultRuntimeSettings()
		now := s.now()
		defaults.UpdatedAt = &now
		s.settings = &defaults
	}
	return translation.NormalizeRuntimeSettings(*s.settings)
}

func (s *Store) LockRuntimeSettings(_ context.Context) (translation.RuntimeSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		return translation.RuntimeSettings{}, errors.New("record not found")
	}
	return *s.settings, nil
}

func (s *Store) UpdateRuntimeSettings(_ context.Context, change application.SettingsChange) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		return time.Time{}, errors.New("record not found")
	}
	settings := *s.settings
	if change.DefaultLocale != nil {
		settings.DefaultLocale = *change.DefaultLocale
	}
	now := s.now()
	settings.UpdatedAt = &now
	s.settings = &settings
	return now, nil
}

func (s *Store) Locales(_ context.Context, enabledOnly bool) ([]application.Locale, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var locales []application.Locale
	for _, locale := range s.locales {
		if !enabledOnly || locale.Enabled {
			locales = append(locales, locale)
		}
	}
	return locales, nil
}

func (s *Store) Locale(_ context.Context, code string) (application.Locale, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, locale := range s.locales {
		if locale.Code == strings.TrimSpace(code) {
			return locale, nil
		}
	}
	return application.Locale{}, application.ErrLocaleNotFound
}

// OverviewStats counts what the fake holds; the coverage SQL across every
// catalog table is the PostgreSQL store's.
func (s *Store) OverviewStats(context.Context) (application.OverviewStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stats := application.OverviewStats{TotalLocales: int64(len(s.locales)), SourceEntities: int64(len(s.entities))}
	for _, locale := range s.locales {
		if locale.Enabled {
			stats.EnabledLocales++
		}
		if locale.IsPublic {
			stats.PublicLocales++
		}
	}
	for _, entity := range s.entities {
		for locale := range entity.Entries {
			if locale != entity.SourceLocale {
				stats.ExistingEntries++
			}
		}
	}
	return stats, nil
}

func (s *Store) LocaleHealth(context.Context) ([]application.LocaleHealth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	health := make([]application.LocaleHealth, 0, len(s.locales))
	for _, locale := range s.locales {
		health = append(health, application.LocaleHealth{Locale: locale})
	}
	return health, nil
}

func (s *Store) EntityHealth(context.Context) ([]application.EntityHealth, error) {
	definitions := translation.Definitions()
	health := make([]application.EntityHealth, 0, len(definitions))
	for _, definition := range definitions {
		health = append(health, application.EntityHealth{EntityType: string(definition.Kind)})
	}
	return health, nil
}

func (s *Store) entity(entityType, entityID string) (*Entity, error) {
	entity, ok := s.entities[entityKey(entityType, entityID)]
	if !ok {
		return nil, application.ErrEntityNotFound
	}
	return entity, nil
}

func (s *Store) DocumentAuthority(_ context.Context, entityType, entityID string) (application.DocumentAuthority, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entity, err := s.entity(entityType, entityID)
	if err != nil {
		return application.DocumentAuthority{}, err
	}
	return application.DocumentAuthority{
		SourceLocale: entity.SourceLocale, DocumentID: entity.DocumentID, DocumentRevision: entity.DocumentRevision,
	}, nil
}

func (s *Store) Entries(_ context.Context, entityType, entityID string) ([]application.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entity, ok := s.entities[entityKey(entityType, entityID)]
	if !ok {
		return nil, nil
	}
	locales := slices.Sorted(maps.Keys(entity.Entries))
	entries := make([]application.Entry, 0, len(locales))
	for _, locale := range locales {
		entries = append(entries, entity.Entries[locale])
	}
	return entries, nil
}

func (s *Store) Entry(_ context.Context, entityType, entityID, locale string) (application.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entity, ok := s.entities[entityKey(entityType, entityID)]
	if !ok {
		return application.Entry{}, application.ErrEntryNotFound
	}
	entry, ok := entity.Entries[locale]
	if !ok {
		return application.Entry{}, application.ErrEntryNotFound
	}
	return entry, nil
}

func (s *Store) SourceSwitchLocales(_ context.Context, entityType, entityID string, _ uuid.UUID, previous, next string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	locales := []string{previous, next}
	if entity, ok := s.entities[entityKey(entityType, entityID)]; ok {
		locales = append(locales, slices.Collect(maps.Keys(entity.Entries))...)
		locales = append(locales, entity.BlockLocales...)
	}
	locales = slices.DeleteFunc(locales, func(locale string) bool { return locale == "" })
	slices.Sort(locales)
	return slices.Compact(locales), nil
}

// SwitchSourceLocale moves the document under its expected revision; an
// unchanged locale leaves the revision as it is.
func (s *Store) SwitchSourceLocale(_ context.Context, change application.SourceLocaleSwitch) (application.SwitchedSourceLocale, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entity, err := s.entity(change.EntityType, change.EntityID)
	if err != nil {
		return application.SwitchedSourceLocale{}, err
	}
	if entity.DocumentRevision != change.ExpectedRevision {
		return application.SwitchedSourceLocale{}, domainerr.Precondition("Content Document revision changed")
	}
	if entity.SourceLocale != change.Authority.SourceLocale {
		return application.SwitchedSourceLocale{}, translation.ErrSourceNoLongerCurrent
	}
	if entity.SourceLocale == change.Requested {
		return application.SwitchedSourceLocale{DocumentRevision: entity.DocumentRevision}, nil
	}
	if _, ok := entity.Entries[change.Requested]; !ok {
		entity.Entries[change.Requested] = application.Entry{
			EntityType: entity.EntityType, EntityID: entity.EntityID, Locale: change.Requested, UpdatedAt: change.Now,
		}
	}
	entity.SourceLocale = change.Requested
	entity.DocumentRevision = uuid.New()
	return application.SwitchedSourceLocale{DocumentRevision: entity.DocumentRevision, Changed: true}, nil
}

func (s *Store) PublishSourceLocaleChanged(_ context.Context, entityType, entityID, documentRevision string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.signalErr != nil {
		return s.signalErr
	}
	s.signals = append(s.signals, Signal{
		EntityType: entityType, EntityID: entityID, DocumentRevision: documentRevision,
	})
	return nil
}

func (s *Store) Domains() application.Domains { return s.domains }

var _ application.Store = (*Store)(nil)
