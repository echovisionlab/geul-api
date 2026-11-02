// Package translationtest is the in-memory Translation Store, its fake
// owning-domain registry, and the Store contract both the fake and the
// PostgreSQL store keep.
package translationtest

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
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
	jobs      map[string]application.Job
	providers map[string]application.Provider
	settings  *translation.RuntimeSettings
	siteTitle string
	company   string
	locales   []application.Locale
	entities  map[string]*Entity
	enqueued  []string
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

// Signal is one content.updated the Store scheduled.
type Signal struct {
	EntityType string
	EntityID   string
	// SourceLocaleChanged tells a source-locale switch from an applied
	// provider result.
	SourceLocaleChanged bool
	DocumentRevision    string
	Job                 application.Job
	Target              application.AppliedTranslationTarget
}

// New returns an empty Store and its unit of work for the use cases.
func New() (*Store, *mutationtest.UnitOfWork[application.Store]) {
	store := NewStore()
	return store, mutationtest.New[application.Store](store)
}

// NewStore returns an empty Store with every catalog locale enabled.
func NewStore() *Store {
	store := &Store{
		jobs: map[string]application.Job{}, providers: map[string]application.Provider{},
		entities: map[string]*Entity{}, now: func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) },
		siteTitle: "Site", company: "Company",
	}
	store.domains = &Domains{store: store}
	for index, code := range []string{"en", "ko", "ja", "fr"} {
		store.locales = append(store.locales, application.Locale{
			Code: code, DisplayName: code, Enabled: true, IsPublic: true, Dir: "ltr",
			MachineTranslationAllowed: true, SortOrder: int32(index + 1),
		})
	}
	return store
}

// JobUnits returns a unit of work of the job runner over the same Store.
func (s *Store) JobUnits() *mutationtest.UnitOfWork[application.JobStore] {
	return mutationtest.New[application.JobStore](s)
}

// FakeDomains is the fake owning-domain registry, for a test to configure.
func (s *Store) FakeDomains() *Domains { return s.domains }

// Savepoint captures the persisted fields and the rollback of the Store.
func (s *Store) Savepoint() mutationtest.Checkpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs := mutationtest.ClonePersisted(s.jobs)
	providers := mutationtest.ClonePersisted(s.providers)
	settings := mutationtest.ClonePersisted(s.settings)
	entities := mutationtest.ClonePersisted(s.entities)
	enqueued := mutationtest.ClonePersisted(s.enqueued)
	signals := mutationtest.ClonePersisted(s.signals)
	return mutationtest.Checkpoint{
		State: map[string]any{
			"jobs":      jobs,
			"providers": providers,
			"settings":  settings,
			"entities":  entities,
			"enqueued":  enqueued,
			"signals":   signals,
		},
		Restore: func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.jobs = jobs
			s.providers = providers
			s.settings = settings
			s.entities = entities
			s.enqueued = enqueued
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

// SeedJob stores job as it is.
func (s *Store) SeedJob(job application.Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = job
}

// StoredJob returns the stored job, request artifact included.
func (s *Store) StoredJob(jobID string) (application.Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	return job, ok
}

// Jobs returns every stored job by request time.
func (s *Store) Jobs() []application.Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sortedJobs(slices.Collect(maps.Values(s.jobs)))
}

// SeedProvider stores provider, assigning an ID when it has none.
func (s *Store) SeedProvider(provider application.Provider) application.Provider {
	s.mu.Lock()
	defer s.mu.Unlock()
	if provider.ID == "" {
		provider.ID = uuid.NewString()
	}
	s.providers[provider.ID] = provider
	return provider
}

// StoredProvider returns a stored provider.
func (s *Store) StoredProvider(providerID string) (application.Provider, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	provider, ok := s.providers[providerID]
	return provider, ok
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

// SetSiteNames sets the site title and company name.
func (s *Store) SetSiteNames(siteTitle, companyName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.siteTitle, s.company = siteTitle, companyName
}

// SetLocales replaces the runtime locale catalog.
func (s *Store) SetLocales(locales ...application.Locale) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.locales = slices.Clone(locales)
}

// Enqueued returns the job IDs whose generate command was enqueued.
func (s *Store) Enqueued() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.enqueued)
}

// Signals returns the scheduled content.updated signals.
func (s *Store) Signals() []Signal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.signals)
}

func sortedJobs(jobs []application.Job) []application.Job {
	sort.Slice(jobs, func(i, j int) bool {
		if !jobs[i].RequestedAt.Equal(jobs[j].RequestedAt) {
			return jobs[i].RequestedAt.Before(jobs[j].RequestedAt)
		}
		return jobs[i].ID < jobs[j].ID
	})
	return jobs
}

func withoutArtifact(job application.Job) application.Job {
	job.RequestXLIFF, job.RequestManifest = nil, nil
	return job
}

func (s *Store) Job(_ context.Context, jobID string) (application.Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return application.Job{}, false, nil
	}
	return withoutArtifact(job), true, nil
}

func (s *Store) JobForCommand(_ context.Context, jobID string, _ bool) (application.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return application.Job{}, application.ErrJobNotFound
	}
	return job, nil
}

func (s *Store) RequestArtifact(_ context.Context, jobID string) (application.StoredRequestArtifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return application.StoredRequestArtifact{}, application.ErrJobNotFound
	}
	return application.StoredRequestArtifact{XLIFF: job.RequestXLIFF, Manifest: job.RequestManifest, Digest: job.RequestArtifactDigest}, nil
}

// pinnedTo is the claim and resume condition: no stored provider document,
// or a whole one of the same provider and model.
func pinnedTo(job application.Job, provider, modelName string) bool {
	if job.ProviderDocumentID == nil && job.ProviderDocumentKey == nil && job.ProviderDocumentSubmittedAt == nil {
		return true
	}
	return job.ProviderDocumentID != nil && job.ProviderDocumentKey != nil && job.ProviderDocumentSubmittedAt != nil &&
		job.Provider != nil && *job.Provider == provider && job.Model != nil && *job.Model == modelName
}

func (s *Store) ClaimJob(_ context.Context, jobID, provider, modelName string, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok || job.Status != application.JobQueued || !pinnedTo(job, provider, modelName) {
		return false, nil
	}
	job.Status = application.JobRunning
	job.Provider, job.Model = &provider, &modelName
	if job.StartedAt == nil {
		started := now
		job.StartedAt = &started
	}
	job.UpdatedAt = now
	s.jobs[jobID] = job
	return true, nil
}

func (s *Store) ResumeJob(_ context.Context, jobID, provider, modelName string, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok || job.Status != application.JobRunning || !pinnedTo(job, provider, modelName) {
		return false, nil
	}
	job.Provider, job.Model = &provider, &modelName
	job.UpdatedAt = now
	s.jobs[jobID] = job
	return true, nil
}

func (s *Store) LockJobStatus(_ context.Context, jobID string) (application.JobStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return "", application.ErrJobNotFound
	}
	return job.Status, nil
}

func (s *Store) LockJobProviderDocument(_ context.Context, jobID string) (application.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return application.Job{}, application.ErrJobNotFound
	}
	return application.Job{
		ID: job.ID, Status: job.Status, Provider: job.Provider, Model: job.Model,
		ProviderDocumentID: job.ProviderDocumentID, ProviderDocumentKey: job.ProviderDocumentKey,
		ProviderDocumentSubmittedAt: job.ProviderDocumentSubmittedAt,
	}, nil
}

func (s *Store) SetJobProvider(_ context.Context, jobID, provider, modelName string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok || job.Status != application.JobRunning {
		return nil
	}
	job.Provider, job.Model = &provider, &modelName
	job.UpdatedAt = now
	s.jobs[jobID] = job
	return nil
}

func (s *Store) SetJobProviderDocument(_ context.Context, jobID string, document application.ProviderDocument, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok || job.Status != application.JobRunning || job.Provider == nil || *job.Provider != document.Provider ||
		job.Model == nil || *job.Model != document.Model {
		return false, nil
	}
	id, key, submitted := document.DocumentID, document.DocumentKey, document.SubmittedAt
	job.ProviderDocumentID, job.ProviderDocumentKey, job.ProviderDocumentSubmittedAt = &id, &key, &submitted
	job.UpdatedAt = now
	s.jobs[jobID] = job
	return true, nil
}

func (s *Store) ClearJobProviderDocument(_ context.Context, jobID string, document application.ProviderDocument, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok || job.Status != application.JobRunning || job.Provider == nil || *job.Provider != document.Provider ||
		job.Model == nil || *job.Model != document.Model ||
		job.ProviderDocumentID == nil || *job.ProviderDocumentID != document.DocumentID ||
		job.ProviderDocumentKey == nil || *job.ProviderDocumentKey != document.DocumentKey ||
		job.ProviderDocumentSubmittedAt == nil || !job.ProviderDocumentSubmittedAt.Equal(document.SubmittedAt) {
		return false, nil
	}
	job.ProviderDocumentID, job.ProviderDocumentKey, job.ProviderDocumentSubmittedAt = nil, nil, nil
	job.UpdatedAt = now
	s.jobs[jobID] = job
	return true, nil
}

func (s *Store) DeleteJob(_ context.Context, jobID string, statuses ...application.JobStatus) (int64, error) {
	if len(statuses) == 0 {
		return 0, errors.New("translation job delete requires a status")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok || !slices.Contains(statuses, job.Status) {
		return 0, nil
	}
	delete(s.jobs, jobID)
	return 1, nil
}

func activeFor(job application.Job, key application.ActiveJobKey) bool {
	return job.EntityType == key.EntityType && job.EntityID == key.EntityID && job.TargetLocale == key.TargetLocale &&
		job.RequestArtifactDigest == key.RequestArtifactDigest &&
		(job.Status == application.JobQueued || job.Status == application.JobRunning)
}

func (s *Store) LockActiveJobs(_ context.Context, key application.ActiveJobKey) ([]application.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var active []application.Job
	for _, job := range s.jobs {
		if activeFor(job, key) {
			active = append(active, withoutArtifact(job))
		}
	}
	return sortedJobs(active), nil
}

// InsertJob keeps the unique indexes: one job per entity, target locale and
// request artifact, and one per entity, target locale and operation.
func (s *Store) InsertJob(_ context.Context, job application.Job) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := application.ActiveJobKey{
		EntityType: job.EntityType, EntityID: job.EntityID, TargetLocale: job.TargetLocale,
		RequestArtifactDigest: job.RequestArtifactDigest,
	}
	for _, existing := range s.jobs {
		sameOperation := existing.EntityType == job.EntityType && existing.EntityID == job.EntityID &&
			existing.TargetLocale == job.TargetLocale && existing.OperationID == job.OperationID
		if existing.ID == job.ID || activeFor(existing, key) || sameOperation {
			return false, nil
		}
	}
	s.jobs[job.ID] = job
	return true, nil
}

func (s *Store) EnqueueGenerate(_ context.Context, jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enqueued = append(s.enqueued, jobID)
	return nil
}

// ListJobs honors the equality filters and status IN of the job browser and
// orders by updated_at DESC, id ASC; sorting by other fields is the
// PostgreSQL store's.
func (s *Store) ListJobs(_ context.Context, query application.JobListQuery) ([]application.Job, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var matched []application.Job
	for _, job := range s.jobs {
		if jobMatches(job, query) {
			matched = append(matched, withoutArtifact(job))
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		if !matched[i].UpdatedAt.Equal(matched[j].UpdatedAt) {
			return matched[i].UpdatedAt.After(matched[j].UpdatedAt)
		}
		return matched[i].ID < matched[j].ID
	})
	total := int64(len(matched))
	start := min(int(query.Offset), len(matched))
	end := min(start+int(query.Limit), len(matched))
	return matched[start:end], total, nil
}

func jobMatches(job application.Job, query application.JobListQuery) bool {
	for _, filter := range query.Filters {
		if filter == nil {
			continue
		}
		value := strings.TrimSpace(filter.GetValue())
		var field string
		switch filter.GetField() {
		case "entity_type":
			field = job.EntityType
		case "entity_id":
			field = job.EntityID
		case "target_locale":
			field = job.TargetLocale
		case "source_locale":
			field = job.SourceLocale
		case "status":
			field = string(job.Status)
			if len(filter.GetValues()) != 0 {
				if !slices.Contains(filter.GetValues(), field) {
					return false
				}
				continue
			}
		}
		if field != value {
			return false
		}
	}
	return true
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
	settings := *s.settings
	settings.ProtectedTerms = slices.Clone(settings.ProtectedTerms)
	return settings, nil
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
	if change.TermsChanged {
		settings.ProtectedTerms = slices.Clone(change.ProtectedTerms)
	}
	now := s.now()
	settings.UpdatedAt = &now
	s.settings = &settings
	return now, nil
}

func (s *Store) SiteNames(context.Context) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.siteTitle, s.company, nil
}

func sortedProviders(providers []application.Provider) []application.Provider {
	sort.SliceStable(providers, func(i, j int) bool {
		if providers[i].Priority != providers[j].Priority {
			return providers[i].Priority < providers[j].Priority
		}
		return providers[i].CreatedAt.Before(providers[j].CreatedAt)
	})
	return providers
}

func (s *Store) ActiveProviders(context.Context) ([]application.Provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var active []application.Provider
	for _, provider := range s.providers {
		if provider.IsActive {
			active = append(active, provider)
		}
	}
	return sortedProviders(active), nil
}

func (s *Store) ListProviders(_ context.Context, query application.ProviderListQuery) ([]application.Provider, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var matched []application.Provider
	for _, provider := range s.providers {
		if !query.ActiveOnly || provider.IsActive {
			matched = append(matched, provider)
		}
	}
	matched = sortedProviders(matched)
	start := min(int(query.Offset), len(matched))
	end := min(start+int(query.Limit), len(matched))
	return matched[start:end], int64(len(matched)), nil
}

func (s *Store) LockProvider(_ context.Context, providerID string) (application.Provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	provider, ok := s.providers[providerID]
	if !ok {
		return application.Provider{}, application.ErrProviderNotFound
	}
	return provider, nil
}

func (s *Store) InsertProvider(_ context.Context, provider application.Provider) (application.Provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	provider.ID = uuid.NewString()
	s.providers[provider.ID] = provider
	return provider, nil
}

func (s *Store) UpdateProvider(_ context.Context, providerID string, change application.ProviderChange) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	provider, ok := s.providers[providerID]
	if !ok {
		return time.Time{}, application.ErrProviderNotFound
	}
	if change.Name != nil {
		provider.Name = *change.Name
	}
	if change.IsActive != nil {
		provider.IsActive = *change.IsActive
	}
	if change.Priority != nil {
		provider.Priority = *change.Priority
	}
	if change.Type != nil {
		provider.Type = *change.Type
	}
	if change.Config != nil {
		provider.Config = slices.Clone(change.Config)
	}
	provider.UpdatedAt = s.now()
	s.providers[providerID] = provider
	return provider.UpdatedAt, nil
}

func (s *Store) DeleteProvider(_ context.Context, providerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.providers, providerID)
	return nil
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

func (s *Store) MachineTranslationTargets(context.Context) ([]application.Locale, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var locales []application.Locale
	for _, locale := range s.locales {
		if locale.Enabled && locale.MachineTranslationAllowed {
			locales = append(locales, locale)
		}
	}
	return locales, nil
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
	for _, job := range s.jobs {
		if job.Status == application.JobQueued || job.Status == application.JobRunning {
			stats.ActiveJobs++
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
		EntityType: entityType, EntityID: entityID, SourceLocaleChanged: true, DocumentRevision: documentRevision,
	})
	return nil
}

func (s *Store) PublishTranslationApplied(_ context.Context, job application.Job, target application.AppliedTranslationTarget, _ time.Time) error {
	if strings.TrimSpace(target.DocumentRevision) == "" {
		return fmt.Errorf("translation document revision is not canonical")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.signalErr != nil {
		return fmt.Errorf("publish applied translation content update: %w", s.signalErr)
	}
	s.signals = append(s.signals, Signal{
		EntityType: job.EntityType, EntityID: job.EntityID, DocumentRevision: target.DocumentRevision,
		Job: job, Target: target,
	})
	return nil
}

func (s *Store) Domains() application.Domains { return s.domains }

var _ application.Store = (*Store)(nil)
