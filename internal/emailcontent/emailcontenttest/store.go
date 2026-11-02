// Package emailcontenttest holds the in-memory emailcontent.Store an email
// content owner's fake Store embeds: locale rows and a minimal content
// document whose revision advances like the content store's. It knows no
// SQL and takes no lock; Block validation stays the content store's.
package emailcontenttest

import (
	"context"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
)

// Document is a fake content document: its revision, its source Blocks and
// the Blocks each target locale overlays.
type Document struct {
	ID       uuid.UUID
	Profile  string
	Revision uuid.UUID
	Blocks   []contentblock.BaseBlock
	Overlays map[string][]contentblock.LocaleBlockUpdate
}

// Locale is one stored locale row with its materialized content.
type Locale struct {
	emailcontent.LocaleMetadata
	HTML *string
	Text *string
}

type localeKey struct{ id, locale string }

type state struct {
	documents map[uuid.UUID]Document
	locales   map[localeKey]Locale
	touched   map[string]time.Time
	updates   []emailcontent.ContentUpdate
	results   []contentblock.Result
}

func (s state) clone() state {
	clone := s
	clone.documents = mutationtest.ClonePersisted(s.documents)
	clone.locales = mutationtest.ClonePersisted(s.locales)
	clone.touched = mutationtest.ClonePersisted(s.touched)
	clone.updates = mutationtest.ClonePersisted(s.updates)
	clone.results = slices.Clone(s.results)
	return clone
}

// Store is the in-memory emailcontent.Store. Seed it with the Put methods
// and read it back with the getters.
type Store struct {
	mu sync.Mutex
	state
	failures map[string]error
	// Touched, when set, also receives every TouchOwner, so the owner's
	// fake moves its own row.
	Touched func(id string, now time.Time)
}

var _ emailcontent.Store = (*Store)(nil)

// New returns an empty Store.
func New() *Store {
	return &Store{state: state{
		documents: map[uuid.UUID]Document{}, locales: map[localeKey]Locale{}, touched: map[string]time.Time{},
	}}
}

// Savepoint captures the persisted fields and the rollback of the Store.
func (s *Store) Savepoint() mutationtest.Checkpoint {
	s.mu.Lock()
	saved := s.state.clone()
	s.mu.Unlock()
	return mutationtest.Checkpoint{
		State: map[string]any{
			"documents": saved.documents,
			"locales":   saved.locales,
			"touched":   saved.touched,
			"updates":   saved.updates,
		},
		Restore: func() {
			s.mu.Lock()
			s.state = saved
			s.mu.Unlock()
		},
	}
}

// FailOn makes method answer err.
func (s *Store) FailOn(method string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failures == nil {
		s.failures = map[string]error{}
	}
	s.failures[method] = err
}

func (s *Store) fail(method string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failures[method]
}

// PutDocument stores document; a nil revision gets a fresh one.
func (s *Store) PutDocument(document Document) Document {
	if document.ID == uuid.Nil {
		document.ID = uuid.New()
	}
	if document.Revision == uuid.Nil {
		document.Revision = uuid.New()
	}
	if document.Profile == "" {
		document.Profile = emailcontent.Profile
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.documents[document.ID] = document
	return document
}

// Document returns the stored document.
func (s *Store) Document(id uuid.UUID) (Document, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, ok := s.documents[id]
	return document, ok
}

// PutLocale stores the locale row of id.
func (s *Store) PutLocale(id string, locale Locale) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.locales[localeKey{id, locale.Locale}] = locale
}

// Locale returns the stored locale row of id.
func (s *Store) Locale(id, locale string) (Locale, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.locales[localeKey{id, locale}]
	return row, ok
}

// TouchedAt reports when TouchOwner last moved id.
func (s *Store) TouchedAt(id string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.touched[id]
	return at, ok
}

// ContentUpdates returns the content.updated signals the Store scheduled.
func (s *Store) ContentUpdates() []emailcontent.ContentUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.updates)
}

// NextResult makes the next content batch answer result, as a concurrent
// writer or the content store's own change detection would.
func (s *Store) NextResult(result contentblock.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results = append(s.results, result)
}

func (s *Store) document(id uuid.UUID) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, ok := s.documents[id]
	if !ok {
		return Document{}, contentblock.ErrDocumentNotFound
	}
	return document, nil
}

func (s *Store) nextResult() (contentblock.Result, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.results) == 0 {
		return contentblock.Result{}, false
	}
	result := s.results[0]
	s.results = s.results[1:]
	return result, true
}

func snapshotOf(document Document, sourceLocale string) contentblock.Snapshot {
	snapshot := contentblock.Snapshot{
		Document:     contentblock.Document{ID: document.ID, Profile: document.Profile, Revision: document.Revision},
		SourceLocale: sourceLocale, Blocks: slices.Clone(document.Blocks),
	}
	for _, locale := range slices.Sorted(maps.Keys(document.Overlays)) {
		snapshot.LocaleOverlays = append(snapshot.LocaleOverlays, contentblock.LocaleOverlay{
			Locale: locale, Blocks: slices.Clone(document.Overlays[locale]),
		})
	}
	return snapshot
}

// CreateContentDocument implements emailcontent.Store.
func (s *Store) CreateContentDocument(_ context.Context, input contentblock.CreateInput) (contentblock.Snapshot, error) {
	if err := s.fail("CreateContentDocument"); err != nil {
		return contentblock.Snapshot{}, err
	}
	document := s.PutDocument(Document{ID: input.ID, Profile: input.Profile})
	return snapshotOf(document, input.SourceLocale), nil
}

// ContentSnapshot implements emailcontent.Store.
func (s *Store) ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	return s.LockedContentSnapshot(ctx, documentID, sourceLocale)
}

// LockedContentSnapshot implements emailcontent.Store.
func (s *Store) LockedContentSnapshot(_ context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	if err := s.fail("LockedContentSnapshot"); err != nil {
		return contentblock.Snapshot{}, err
	}
	document, err := s.document(documentID)
	if err != nil {
		return contentblock.Snapshot{}, err
	}
	return snapshotOf(document, sourceLocale), nil
}

// admit runs the fence and the revision compare-and-set of a write.
func (s *Store) admit(ctx context.Context, documentID, expected uuid.UUID, fence emailcontent.Fence) (Document, contentblock.DomainContext, error) {
	domain, err := fence(ctx, documentID)
	if err != nil {
		return Document{}, contentblock.DomainContext{}, err
	}
	document, err := s.document(documentID)
	if err != nil {
		return Document{}, contentblock.DomainContext{}, err
	}
	if expected != document.Revision {
		return Document{}, contentblock.DomainContext{}, &contentblock.StaleRevisionError{CurrentRevision: document.Revision}
	}
	return document, domain, nil
}

func batchLocales(batch contentblock.Batch) []string {
	locales := make([]string, 0, len(batch.LocaleGroups))
	for _, group := range batch.LocaleGroups {
		locales = append(locales, group.Locale)
	}
	return locales
}

func batchChanges(batch contentblock.Batch) bool {
	return len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 || len(batch.LocaleGroups) != 0
}

// overlay records the locale groups of batch other than the source locale.
func overlay(document Document, batch contentblock.Batch, sourceLocale string) Document {
	document.Overlays = maps.Clone(document.Overlays)
	for _, group := range batch.LocaleGroups {
		if group.Locale == sourceLocale {
			continue
		}
		if document.Overlays == nil {
			document.Overlays = map[string][]contentblock.LocaleBlockUpdate{}
		}
		blocks := slices.Clone(document.Overlays[group.Locale])
		for _, deleted := range group.Deletes {
			blocks = slices.DeleteFunc(blocks, func(block contentblock.LocaleBlockUpdate) bool { return block.BlockID == deleted })
		}
		blocks = append(blocks, group.Upserts...)
		if len(blocks) == 0 {
			delete(document.Overlays, group.Locale)
			continue
		}
		document.Overlays[group.Locale] = blocks
	}
	return document
}

// commit stores the document after a result, advancing its revision when
// the result changed the shared document and names no revision.
func (s *Store) commit(document Document, result contentblock.Result, advance bool) contentblock.Result {
	if result.DocumentRevision == uuid.Nil {
		result.DocumentRevision = document.Revision
		if result.Changed && advance {
			result.DocumentRevision = uuid.New()
		}
	}
	document.Revision = result.DocumentRevision
	s.mu.Lock()
	s.documents[document.ID] = document
	s.mu.Unlock()
	return result
}

func (s *Store) applyBatch(ctx context.Context, batch contentblock.Batch, fence emailcontent.Fence, effect emailcontent.MetadataEffect) (contentblock.Result, error) {
	document, domain, err := s.admit(ctx, batch.DocumentID, batch.ExpectedRevision, fence)
	if err != nil {
		return contentblock.Result{}, err
	}
	if result, ok := s.nextResult(); ok {
		return s.commit(overlay(document, batch, domain.SourceLocale), result, true), nil
	}
	var metadata contentblock.MetadataEffect
	if effect != nil {
		if metadata, err = effect(ctx); err != nil {
			return contentblock.Result{}, err
		}
	}
	if !batchChanges(batch) && !metadata.Changed {
		return contentblock.Result{DocumentRevision: document.Revision}, nil
	}
	sourceChanged := len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 || metadata.AffectsTranslationSource
	for _, locale := range batchLocales(batch) {
		sourceChanged = sourceChanged || locale == domain.SourceLocale
	}
	return s.commit(overlay(document, batch, domain.SourceLocale), contentblock.Result{
		Changed: true, ContentChanged: batchChanges(batch), MetadataChanged: metadata.Changed,
		TranslationSourceChanged: sourceChanged, ChangedLocales: append(batchLocales(batch), metadata.ChangedLocales...),
	}, true), nil
}

// ApplyContentBatch implements emailcontent.Store. A batch with any
// mutation changes the document and advances its revision; touching the
// source locale or the shared graph changes the translation source.
func (s *Store) ApplyContentBatch(ctx context.Context, batch contentblock.Batch, fence emailcontent.Fence) (contentblock.Result, error) {
	if err := s.fail("ApplyContentBatch"); err != nil {
		return contentblock.Result{}, err
	}
	return s.applyBatch(ctx, batch, fence, nil)
}

// ApplyContentBatchWithMetadata implements emailcontent.Store.
func (s *Store) ApplyContentBatchWithMetadata(ctx context.Context, batch contentblock.Batch, fence emailcontent.Fence, effect emailcontent.MetadataEffect) (contentblock.Result, error) {
	if err := s.fail("ApplyContentBatchWithMetadata"); err != nil {
		return contentblock.Result{}, err
	}
	return s.applyBatch(ctx, batch, fence, effect)
}

// ApplyTargetLocaleContentBatch implements emailcontent.Store. A
// target-locale batch never advances the shared revision.
func (s *Store) ApplyTargetLocaleContentBatch(ctx context.Context, batch contentblock.Batch, locale string, fence emailcontent.Fence, effect emailcontent.TargetMetadataEffect) (contentblock.Result, error) {
	if err := s.fail("ApplyTargetLocaleContentBatch"); err != nil {
		return contentblock.Result{}, err
	}
	document, domain, err := s.admit(ctx, batch.DocumentID, batch.ExpectedRevision, fence)
	if err != nil {
		return contentblock.Result{}, err
	}
	contentChanged := len(batch.LocaleGroups) != 0
	metadata, err := effect(ctx, contentChanged)
	if err != nil {
		return contentblock.Result{}, err
	}
	result := contentblock.Result{
		DocumentRevision: document.Revision, Changed: contentChanged || metadata.Changed,
		ContentChanged: contentChanged, MetadataChanged: metadata.Changed,
	}
	if result.Changed {
		result.ChangedLocales = []string{locale}
	}
	return s.commit(overlay(document, batch, domain.SourceLocale), result, false), nil
}

// AdvanceContentRevision implements emailcontent.Store.
func (s *Store) AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence emailcontent.Fence, effect emailcontent.MetadataEffect) (contentblock.AdvanceResult, error) {
	if err := s.fail("AdvanceContentRevision"); err != nil {
		return contentblock.AdvanceResult{}, err
	}
	document, _, err := s.admit(ctx, input.DocumentID, input.ExpectedRevision, fence)
	if err != nil {
		return contentblock.AdvanceResult{}, err
	}
	metadata, err := effect(ctx)
	if err != nil {
		return contentblock.AdvanceResult{}, err
	}
	if !metadata.Changed {
		return contentblock.AdvanceResult{DocumentRevision: document.Revision}, nil
	}
	result := s.commit(document, contentblock.Result{Changed: true}, true)
	return contentblock.AdvanceResult{
		DocumentRevision: result.DocumentRevision, Changed: true, TranslationSourceChanged: metadata.AffectsTranslationSource,
	}, nil
}

// DeleteContentDocument implements emailcontent.Store.
func (s *Store) DeleteContentDocument(ctx context.Context, documentID uuid.UUID, fence emailcontent.Fence) error {
	if err := s.fail("DeleteContentDocument"); err != nil {
		return err
	}
	if _, err := fence(ctx, documentID); err != nil {
		return err
	}
	if _, err := s.document(documentID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.documents, documentID)
	return nil
}

// LocaleRow implements emailcontent.Store.
func (s *Store) LocaleRow(_ context.Context, id, locale string, _ emailcontent.RowLock) (emailcontent.LocaleMetadata, bool, error) {
	if err := s.fail("LocaleRow"); err != nil {
		return emailcontent.LocaleMetadata{}, false, err
	}
	row, ok := s.Locale(id, locale)
	return row.LocaleMetadata, ok, nil
}

// LocaleNames implements emailcontent.Store.
func (s *Store) LocaleNames(_ context.Context, id string) ([]string, error) {
	if err := s.fail("LocaleNames"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var locales []string
	for key := range s.locales {
		if key.id == id {
			locales = append(locales, key.locale)
		}
	}
	slices.Sort(locales)
	return locales, nil
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// InsertLocale implements emailcontent.Store; an existing row is a
// constraint violation in PostgreSQL and inserts nothing here.
func (s *Store) InsertLocale(_ context.Context, id, locale string, subject *string, now time.Time) (bool, error) {
	if err := s.fail("InsertLocale"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{id, locale}
	if _, exists := s.locales[key]; exists {
		return false, nil
	}
	s.locales[key] = Locale{LocaleMetadata: emailcontent.LocaleMetadata{Locale: locale, Subject: cloneString(subject), UpdatedAt: now}}
	return true, nil
}

// UpdateLocale implements emailcontent.Store.
func (s *Store) UpdateLocale(_ context.Context, id, locale string, subject *string, updatedAt time.Time) (bool, error) {
	if err := s.fail("UpdateLocale"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{id, locale}
	row, exists := s.locales[key]
	if !exists {
		return false, nil
	}
	row.Subject, row.UpdatedAt = cloneString(subject), updatedAt
	s.locales[key] = row
	return true, nil
}

// UpsertLocaleSubject implements emailcontent.Store.
func (s *Store) UpsertLocaleSubject(_ context.Context, id, locale string, subject *string, now time.Time) error {
	if err := s.fail("UpsertLocaleSubject"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{id, locale}
	row := s.locales[key]
	row.Locale, row.Subject, row.UpdatedAt = locale, cloneString(subject), now
	s.locales[key] = row
	return nil
}

// DeleteLocale implements emailcontent.Store.
func (s *Store) DeleteLocale(_ context.Context, id, locale string) (bool, error) {
	if err := s.fail("DeleteLocale"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{id, locale}
	if _, exists := s.locales[key]; !exists {
		return false, nil
	}
	delete(s.locales, key)
	return true, nil
}

// ProjectLocale implements emailcontent.Store.
func (s *Store) ProjectLocale(_ context.Context, id, locale string, projection emailcontent.LocaleProjection) (bool, error) {
	if err := s.fail("ProjectLocale"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{id, locale}
	row, exists := s.locales[key]
	if !exists {
		return false, nil
	}
	html, text := projection.HTML, projection.Text
	row.HTML, row.Text = &html, &text
	if projection.UpdatedAt != nil {
		row.UpdatedAt = *projection.UpdatedAt
	}
	s.locales[key] = row
	return true, nil
}

// TouchOwner implements emailcontent.Store.
func (s *Store) TouchOwner(_ context.Context, id string, now time.Time) error {
	if err := s.fail("TouchOwner"); err != nil {
		return err
	}
	s.mu.Lock()
	s.touched[id] = now
	touched := s.Touched
	s.mu.Unlock()
	if touched != nil {
		touched(id, now)
	}
	return nil
}

// PublishContentUpdate implements emailcontent.Store; like the content
// signal publisher, it schedules nothing for an unchanged write.
func (s *Store) PublishContentUpdate(_ context.Context, update emailcontent.ContentUpdate) error {
	if err := s.fail("PublishContentUpdate"); err != nil {
		return err
	}
	if !update.Changed {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, update)
	return nil
}
