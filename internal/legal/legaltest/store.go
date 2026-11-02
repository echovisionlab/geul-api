// Package legaltest holds the in-memory legal.Store the Terms and Privacy
// use-case tests run on, and the Store contract it shares with
// legalpostgres. The fake knows no SQL and takes no lock; it keeps the
// sentinels, orderings and row counts the PostgreSQL store answers with, a
// minimal content document whose revision advances like the content
// store's, and records what the OG, notice, Share Link and signal ports
// were asked.
package legaltest

import (
	"context"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// Document is a fake content document: its revision, base Blocks and the
// Block values of each locale.
type Document struct {
	ID       uuid.UUID
	Profile  string
	Revision uuid.UUID
	Blocks   []contentblock.BaseBlock
	Overlays map[string][]contentblock.LocaleBlockUpdate
}

// Record is a stored version: its row, source locale and derived content.
type Record struct {
	legal.Version
	SourceLocale string
	Projection   legal.Projection
}

// TranslationRecord is a stored target locale row with its derived
// content.
type TranslationRecord struct {
	legal.Translation
	Projection legal.Projection
}

// Run is a sealed notice run with what the fake Campaign keeps of it.
type Run struct {
	legal.NoticeRun
	Kind         legal.PolicyKind
	PolicyID     string
	Version      int
	Status       string
	EventKey     string
	Data         map[string]string
	ScheduledAt  time.Time
	StoppedAfter bool
}

// The notice run statuses the fake Campaign moves runs through.
const (
	RunScheduled = "scheduled"
	RunSending   = "sending"
	RunSent      = "sent"
	RunCancelled = "cancelled"
)

// OGRequest is one OG generation the Store requested.
type OGRequest struct {
	Kind       legal.PolicyKind
	ID         string
	Locale     string
	AllLocales bool
	Reason     string
}

type versionKey struct {
	kind legal.PolicyKind
	id   string
}

type translationKey struct {
	kind   legal.PolicyKind
	id     string
	locale string
}

type state struct {
	versions      map[versionKey]Record
	translations  map[translationKey]TranslationRecord
	documents     map[uuid.UUID]Document
	runs          []Run
	previews      map[string]legal.AutomaticPreview
	linkDeletes   []string
	sessions      map[string]*auth.UserInfo
	preferred     map[string]*string
	ogRequests    []OGRequest
	released      []legal.PolicyKind
	updates       []legal.ContentUpdate
	activations   []legal.PolicyKind
	contributions [][]string
}

func (s state) clone() state {
	clone := s
	clone.versions = mutationtest.ClonePersisted(s.versions)
	clone.translations = mutationtest.ClonePersisted(s.translations)
	clone.documents = mutationtest.ClonePersisted(s.documents)
	clone.runs = mutationtest.ClonePersisted(s.runs)
	clone.previews = mutationtest.ClonePersisted(s.previews)
	clone.linkDeletes = mutationtest.ClonePersisted(s.linkDeletes)
	clone.sessions = mutationtest.ClonePersisted(s.sessions)
	clone.preferred = mutationtest.ClonePersisted(s.preferred)
	clone.ogRequests = mutationtest.ClonePersisted(s.ogRequests)
	clone.released = mutationtest.ClonePersisted(s.released)
	clone.updates = mutationtest.ClonePersisted(s.updates)
	clone.activations = slices.Clone(s.activations)
	clone.contributions = slices.Clone(s.contributions)
	return clone
}

// Store is the in-memory legal.Store. Seed it with the Put methods; read it
// back with the getters.
type Store struct {
	mu sync.Mutex
	state
	// Settings are the translation settings TranslationSettings answers.
	Settings translation.RuntimeSettings
	// SiteSettings are the site variables SiteVariables answers.
	SiteSettings sitevariable.Context
	// Now is the time the public route reads as now; nil reads the clock.
	Now func() time.Time
	// Fail fails the Store method of that name with its error.
	Fail map[string]error
}

// NewStore returns an empty fake Store.
func NewStore() *Store {
	return &Store{
		state: state{
			versions: map[versionKey]Record{}, translations: map[translationKey]TranslationRecord{},
			documents: map[uuid.UUID]Document{}, previews: map[string]legal.AutomaticPreview{},
			sessions: map[string]*auth.UserInfo{}, preferred: map[string]*string{},
		},
		Settings: translation.DefaultRuntimeSettings(),
	}
}

// New returns a fake Store and its unit of work.
func New() (*Store, *mutationtest.UnitOfWork[legal.Store]) {
	store := NewStore()
	return store, mutationtest.New[legal.Store](store)
}

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() mutationtest.Checkpoint {
	s.mu.Lock()
	saved := s.clone()
	s.mu.Unlock()
	return mutationtest.Checkpoint{
		State: map[string]any{
			"versions":     saved.versions,
			"translations": saved.translations,
			"documents":    saved.documents,
			"runs":         saved.runs,
			"previews":     saved.previews,
			"linkDeletes":  saved.linkDeletes,
			"sessions":     saved.sessions,
			"preferred":    saved.preferred,
			"ogRequests":   saved.ogRequests,
			"released":     saved.released,
			"updates":      saved.updates,
		},
		Restore: func() {
			s.mu.Lock()
			s.state = saved
			s.mu.Unlock()
		},
	}
}

var _ legal.Store = (*Store)(nil)

func (s *Store) fail(method string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Fail[method]
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

func stamp() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

// status is the stored status of kind in lifecycle state.
func status(kind legal.PolicyKind, state string) legal.Status {
	stored, _ := legal.PolicyStatus(kind, state)
	return stored
}

// PutVersion stores a version of kind with an empty policy document in
// sourceLocale, filling its ID, number, title, status and timestamps when
// unset, and returns it.
func (s *Store) PutVersion(kind legal.PolicyKind, version legal.Version, sourceLocale string) legal.Version {
	s.mu.Lock()
	defer s.mu.Unlock()
	if version.ID == "" {
		version.ID = uuid.NewString()
	}
	if version.Version == 0 {
		version.Version = s.maxVersionLocked(kind) + 1
	}
	if version.Title == "" {
		version.Title = "Policy " + version.ID
	}
	if version.Status == "" {
		version.Status = status(kind, "draft")
	}
	if version.CreatedAt.IsZero() {
		version.CreatedAt = stamp()
	}
	if version.UpdatedAt.IsZero() {
		version.UpdatedAt = version.CreatedAt
	}
	if version.ContentDocumentID == nil {
		document := Document{ID: uuid.New(), Profile: "policy", Revision: uuid.New(), Overlays: map[string][]contentblock.LocaleBlockUpdate{}}
		s.documents[document.ID] = document
		id := document.ID.String()
		version.ContentDocumentID = &id
	}
	if s.holdsSingleStatusLocked(kind, version.Status, version.ID) {
		panic("legaltest: a kind holds one " + string(version.Status) + " version at a time")
	}
	s.versions[versionKey{kind, version.ID}] = Record{Version: version, SourceLocale: sourceLocale}
	return version
}

// holdsSingleStatusLocked reports whether a version of kind other than id
// already holds a status of which a kind keeps one: active or scheduled.
func (s *Store) holdsSingleStatusLocked(kind legal.PolicyKind, state legal.Status, id string) bool {
	if state != status(kind, "active") && state != status(kind, "scheduled") {
		return false
	}
	for key, record := range s.versions {
		if key.kind == kind && key.id != id && record.Status == state {
			return true
		}
	}
	return false
}

func (s *Store) holdsSingleStatus(kind legal.PolicyKind, state legal.Status, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.holdsSingleStatusLocked(kind, state, id)
}

// PutDocument replaces the stored document of a version's ID.
func (s *Store) PutDocument(document Document) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if document.Overlays == nil {
		document.Overlays = map[string][]contentblock.LocaleBlockUpdate{}
	}
	s.documents[document.ID] = document
}

// PutTranslation stores a target locale row.
func (s *Store) PutTranslation(kind legal.PolicyKind, id string, translation legal.Translation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if translation.UpdatedAt.IsZero() {
		translation.UpdatedAt = stamp()
	}
	s.translations[translationKey{kind, id, translation.Locale}] = TranslationRecord{Translation: translation}
}

// PutRun stores a sealed notice run of a version.
func (s *Store) PutRun(run Run) Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run.ID == "" {
		run.ID = uuid.NewString()
	}
	if run.RunKind == "" {
		run.RunKind = legal.EmailDeliveryRunKindLegalNotice
	}
	if run.Status == "" {
		run.Status = RunScheduled
	}
	s.runs = append(s.runs, s.sealLocked(run))
	return s.runs[len(s.runs)-1]
}

// PutSession resolves sessionID to principal.
func (s *Store) PutSession(sessionID string, principal *auth.UserInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = principal
}

// PutPreferredLocale stores the preferred locale of a Member.
func (s *Store) PutPreferredLocale(memberID, locale string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.preferred[memberID] = &locale
}

// Record reads a stored version.
func (s *Store) Record(kind legal.PolicyKind, id string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.versions[versionKey{kind, id}]
	return record, ok
}

// StoredTranslation reads a stored target locale row.
func (s *Store) StoredTranslation(kind legal.PolicyKind, id, locale string) (TranslationRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.translations[translationKey{kind, id, locale}]
	return record, ok
}

// Document reads a stored document.
func (s *Store) Document(documentID uuid.UUID) (Document, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, ok := s.documents[documentID]
	return document, ok
}

// Runs returns the notice runs, in seal order.
func (s *Store) Runs() []Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.runs)
}

// SetRunStatus moves a run, as Campaign's dispatcher would.
func (s *Store) SetRunStatus(runID, runStatus string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.runs {
		if s.runs[i].ID == runID {
			s.runs[i].Status = runStatus
		}
	}
}

// Previews returns the automatic preview links by token.
func (s *Store) Previews() map[string]legal.AutomaticPreview {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.previews)
}

// LinkDeletes names the versions whose links were deleted, as
// "entityType/entityID".
func (s *Store) LinkDeletes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.linkDeletes)
}

// OGRequests returns the OG generations requested, in order.
func (s *Store) OGRequests() []OGRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ogRequests)
}

// Released names the kinds whose route was released, in order.
func (s *Store) Released() []legal.PolicyKind {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.released)
}

// Updates returns the content.updated signals published.
func (s *Store) Updates() []legal.ContentUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.updates)
}

// ActivationLocks names the kinds whose activation lock was taken, in
// order.
func (s *Store) ActivationLocks() []legal.PolicyKind {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.activations)
}

// Contributions returns the contributor lists a collaboration fence
// checked.
func (s *Store) Contributions() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.contributions)
}

func (s *Store) maxVersionLocked(kind legal.PolicyKind) int {
	highest := 0
	for key, record := range s.versions {
		if key.kind == kind && record.Version.Version > highest {
			highest = record.Version.Version
		}
	}
	return highest
}

func (s *Store) record(kind legal.PolicyKind, id string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.versions[versionKey{kind, id}]
	if !ok {
		return Record{}, legal.ErrPolicyNotFound
	}
	return record, nil
}

func (s *Store) update(kind legal.PolicyKind, id string, change func(*Record)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := versionKey{kind, id}
	record, ok := s.versions[key]
	if !ok {
		return
	}
	change(&record)
	s.versions[key] = record
}

// LockVersionAssignment implements legal.Store.
func (s *Store) LockVersionAssignment(context.Context, legal.PolicyKind) error {
	return s.fail("LockVersionAssignment")
}

// Version implements legal.Store.
func (s *Store) Version(_ context.Context, kind legal.PolicyKind, id string, _ legal.RowLock) (legal.Version, error) {
	if err := s.fail("Version"); err != nil {
		return legal.Version{}, err
	}
	record, err := s.record(kind, id)
	return record.Version, err
}

// Root implements legal.Store.
func (s *Store) Root(_ context.Context, kind legal.PolicyKind, id string, _ legal.RowLock) (legal.Root, error) {
	if err := s.fail("Root"); err != nil {
		return legal.Root{}, err
	}
	record, err := s.record(kind, id)
	if err != nil {
		return legal.Root{}, err
	}
	root := legal.Root{
		ID: record.ID, Title: record.Title, Status: record.Status, Version: record.Version.Version,
		SourceLocale: record.SourceLocale,
	}
	if record.ContentDocumentID != nil {
		if documentID, err := uuid.Parse(*record.ContentDocumentID); err == nil {
			root.ContentDocumentID = &documentID
		}
	}
	return root, nil
}

// LockStatus implements legal.Store.
func (s *Store) LockStatus(_ context.Context, kind legal.PolicyKind, id string) (legal.Status, error) {
	if err := s.fail("LockStatus"); err != nil {
		return "", err
	}
	record, err := s.record(kind, id)
	return record.Status, err
}

// ScheduleState implements legal.Store.
func (s *Store) ScheduleState(_ context.Context, kind legal.PolicyKind, id string) (legal.Lifecycle, error) {
	record, err := s.record(kind, id)
	if err != nil {
		return legal.Lifecycle{}, err
	}
	return legal.Lifecycle{Status: record.Status, EffectiveFrom: record.EffectiveFrom}, nil
}

// ListVersions implements legal.Store.
func (s *Store) ListVersions(_ context.Context, kind legal.PolicyKind, filter *legal.Status, limit, offset int32) ([]legal.Version, int64, error) {
	if err := s.fail("ListVersions"); err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var matched []legal.Version
	for key, record := range s.versions {
		if key.kind == kind && (filter == nil || record.Status == *filter) {
			matched = append(matched, record.Version)
		}
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Version > matched[j].Version })
	total := int64(len(matched))
	start := min(int(max(offset, 0)), len(matched))
	end := min(start+int(limit), len(matched))
	return slices.Clone(matched[start:end]), total, nil
}

// InsertDraft implements legal.Store: the number follows the highest of
// the history and the sealed version.
func (s *Store) InsertDraft(_ context.Context, kind legal.PolicyKind, draft legal.Draft) (legal.Version, error) {
	if err := s.fail("InsertDraft"); err != nil {
		return legal.Version{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	documentID := draft.ContentDocumentID
	version := legal.Version{
		ID: uuid.NewString(), Version: max(s.maxVersionLocked(kind), draft.SealedVersion) + 1,
		Title: draft.Title, Status: status(kind, "draft"), ContentDocumentID: &documentID,
		CreatedAt: draft.CreatedAt, UpdatedAt: draft.CreatedAt,
	}
	s.versions[versionKey{kind, version.ID}] = Record{Version: version, SourceLocale: draft.SourceLocale}
	return version, nil
}

// Schedule implements legal.Store.
func (s *Store) Schedule(_ context.Context, kind legal.PolicyKind, id string, effectiveFrom, at time.Time) error {
	if err := s.fail("Schedule"); err != nil {
		return err
	}
	if s.holdsSingleStatus(kind, status(kind, "scheduled"), id) {
		return errSingleStatus
	}
	s.update(kind, id, func(record *Record) {
		record.Status, record.EffectiveFrom, record.UpdatedAt = status(kind, "scheduled"), &effectiveFrom, at
	})
	return nil
}

// Unschedule implements legal.Store.
func (s *Store) Unschedule(_ context.Context, kind legal.PolicyKind, id string, at time.Time) error {
	if err := s.fail("Unschedule"); err != nil {
		return err
	}
	s.update(kind, id, func(record *Record) {
		record.Status, record.EffectiveFrom, record.UpdatedAt = status(kind, "draft"), nil, at
	})
	return nil
}

func (s *Store) idsInStatus(kind legal.PolicyKind, state legal.Status, exceptID string) []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	var records []Record
	for key, record := range s.versions {
		if key.kind == kind && key.id != exceptID && record.Status == state {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	return records
}

// LockScheduledIDs implements legal.Store.
func (s *Store) LockScheduledIDs(_ context.Context, kind legal.PolicyKind, exceptID string) ([]string, error) {
	if err := s.fail("LockScheduledIDs"); err != nil {
		return nil, err
	}
	var ids []string
	for _, record := range s.idsInStatus(kind, status(kind, "scheduled"), exceptID) {
		ids = append(ids, record.ID)
	}
	return ids, nil
}

// LockLifecycle implements legal.Store.
func (s *Store) LockLifecycle(_ context.Context, kind legal.PolicyKind, id string) (legal.Lifecycle, bool, error) {
	if err := s.fail("LockLifecycle"); err != nil {
		return legal.Lifecycle{}, false, err
	}
	record, found := s.Record(kind, id)
	if !found {
		return legal.Lifecycle{}, false, nil
	}
	return legal.Lifecycle{Status: record.Status, EffectiveFrom: record.EffectiveFrom, Version: record.Version.Version}, true, nil
}

// LockActiveVersions implements legal.Store.
func (s *Store) LockActiveVersions(_ context.Context, kind legal.PolicyKind, exceptID string) ([]legal.VersionRef, error) {
	if err := s.fail("LockActiveVersions"); err != nil {
		return nil, err
	}
	records := s.idsInStatus(kind, status(kind, "active"), exceptID)
	refs := make([]legal.VersionRef, len(records))
	for i, record := range records {
		refs[i] = legal.VersionRef{ID: record.ID, Version: record.Version.Version}
	}
	return refs, nil
}

// Archive implements legal.Store.
func (s *Store) Archive(_ context.Context, kind legal.PolicyKind, id string, at time.Time) error {
	if err := s.fail("Archive"); err != nil {
		return err
	}
	s.update(kind, id, func(record *Record) {
		record.Status, record.EffectiveUntil, record.UpdatedAt = status(kind, "archived"), &at, at
	})
	return nil
}

// Activate implements legal.Store.
func (s *Store) Activate(_ context.Context, kind legal.PolicyKind, id string, from []legal.Status, effectiveFrom *time.Time, at time.Time) error {
	if err := s.fail("Activate"); err != nil {
		return err
	}
	record, err := s.record(kind, id)
	if err != nil || !slices.Contains(from, record.Status) {
		return legal.ErrPolicyNotFound
	}
	if s.holdsSingleStatus(kind, status(kind, "active"), id) {
		return errSingleStatus
	}
	s.update(kind, id, func(record *Record) {
		record.Status, record.UpdatedAt = status(kind, "active"), at
		if effectiveFrom != nil {
			value := *effectiveFrom
			record.EffectiveFrom = &value
		}
	})
	return nil
}

// DeleteVersion implements legal.Store; the version's target locale rows
// cascade.
func (s *Store) DeleteVersion(_ context.Context, kind legal.PolicyKind, id string) error {
	if err := s.fail("DeleteVersion"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.versions, versionKey{kind, id})
	for key := range s.translations {
		if key.kind == kind && key.id == id {
			delete(s.translations, key)
		}
	}
	return nil
}

// SetTitle implements legal.Store.
func (s *Store) SetTitle(_ context.Context, kind legal.PolicyKind, id, title string, at time.Time) error {
	if err := s.fail("SetTitle"); err != nil {
		return err
	}
	s.update(kind, id, func(record *Record) { record.Title, record.UpdatedAt = title, at })
	return nil
}

// ReplaceTitle implements legal.Store.
func (s *Store) ReplaceTitle(_ context.Context, kind legal.PolicyKind, id, title string, at time.Time) (bool, error) {
	if err := s.fail("ReplaceTitle"); err != nil {
		return false, err
	}
	record, found := s.Record(kind, id)
	if !found || record.Title == title {
		return false, nil
	}
	s.update(kind, id, func(record *Record) { record.Title, record.UpdatedAt = title, at })
	return true, nil
}

// StoreSourceProjection implements legal.Store.
func (s *Store) StoreSourceProjection(_ context.Context, kind legal.PolicyKind, id string, projection legal.Projection, at time.Time) error {
	if err := s.fail("StoreSourceProjection"); err != nil {
		return err
	}
	s.update(kind, id, func(record *Record) { record.Projection, record.UpdatedAt = projection, at })
	return nil
}

// CountTranslations implements legal.Store.
func (s *Store) CountTranslations(_ context.Context, kind legal.PolicyKind, id, locale string) (int64, error) {
	if _, ok := s.StoredTranslation(kind, id, locale); ok {
		return 1, nil
	}
	return 0, nil
}

// Translation implements legal.Store.
func (s *Store) Translation(_ context.Context, kind legal.PolicyKind, id, locale string, _ bool) (legal.Translation, bool, error) {
	if err := s.fail("Translation"); err != nil {
		return legal.Translation{}, false, err
	}
	record, ok := s.StoredTranslation(kind, id, locale)
	return record.Translation, ok, nil
}

// TranslationTitle implements legal.Store.
func (s *Store) TranslationTitle(_ context.Context, kind legal.PolicyKind, id, locale string) (*string, error) {
	record, ok := s.StoredTranslation(kind, id, locale)
	if !ok {
		return nil, legal.ErrTranslationNotFound
	}
	return record.Title, nil
}

// TranslationLocales implements legal.Store; the fake lists them by
// locale.
func (s *Store) TranslationLocales(_ context.Context, kind legal.PolicyKind, id string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var locales []string
	for key := range s.translations {
		if key.kind == kind && key.id == id {
			locales = append(locales, key.locale)
		}
	}
	sort.Strings(locales)
	return locales, nil
}

// InsertTranslation implements legal.Store; an existing locale is a
// primary key violation, which the fake answers as an error.
func (s *Store) InsertTranslation(_ context.Context, kind legal.PolicyKind, id, locale string, title *string, at time.Time) (int64, error) {
	if err := s.fail("InsertTranslation"); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := translationKey{kind, id, locale}
	if _, exists := s.translations[key]; exists {
		return 0, errDuplicateTranslation
	}
	s.translations[key] = TranslationRecord{Translation: legal.Translation{Locale: locale, Title: title, UpdatedAt: at}}
	return 1, nil
}

// UpdateTranslation implements legal.Store.
func (s *Store) UpdateTranslation(_ context.Context, kind legal.PolicyKind, id, locale string, title *string, at time.Time) (int64, error) {
	if err := s.fail("UpdateTranslation"); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := translationKey{kind, id, locale}
	record, ok := s.translations[key]
	if !ok {
		return 0, nil
	}
	record.Title, record.UpdatedAt = title, at
	s.translations[key] = record
	return 1, nil
}

// DeleteTranslation implements legal.Store.
func (s *Store) DeleteTranslation(_ context.Context, kind legal.PolicyKind, id, locale string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := translationKey{kind, id, locale}
	if _, ok := s.translations[key]; !ok {
		return 0, nil
	}
	delete(s.translations, key)
	return 1, nil
}

// StoreTargetProjection implements legal.Store.
func (s *Store) StoreTargetProjection(_ context.Context, kind legal.PolicyKind, id, locale string, projection legal.Projection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := translationKey{kind, id, locale}
	if record, ok := s.translations[key]; ok {
		record.Projection = projection
		s.translations[key] = record
	}
	return nil
}

// currentRoute is the version the kind's public route shows: the newest
// effective active version, else the first scheduled one.
func (s *Store) currentRoute(kind legal.PolicyKind) *legal.CurrentRoute {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var active, scheduled []Record
	for key, record := range s.versions {
		switch {
		case key.kind != kind:
		case record.Status == status(kind, "active") && (record.EffectiveFrom == nil || !record.EffectiveFrom.After(now)):
			active = append(active, record)
		case record.Status == status(kind, "scheduled"):
			scheduled = append(scheduled, record)
		}
	}
	effective := func(record Record) time.Time {
		if record.EffectiveFrom == nil {
			return time.Time{}
		}
		return *record.EffectiveFrom
	}
	if len(active) != 0 {
		sort.Slice(active, func(i, j int) bool {
			if !effective(active[i]).Equal(effective(active[j])) {
				return effective(active[i]).After(effective(active[j]))
			}
			return active[i].Version.Version > active[j].Version.Version
		})
		return &legal.CurrentRoute{ID: active[0].ID, Title: active[0].Title}
	}
	if len(scheduled) != 0 {
		sort.Slice(scheduled, func(i, j int) bool {
			if !effective(scheduled[i]).Equal(effective(scheduled[j])) {
				return effective(scheduled[i]).Before(effective(scheduled[j]))
			}
			return scheduled[i].Version.Version > scheduled[j].Version.Version
		})
		return &legal.CurrentRoute{ID: scheduled[0].ID, Title: scheduled[0].Title}
	}
	return nil
}

// LockActivation implements legal.Store.
func (s *Store) LockActivation(_ context.Context, kind legal.PolicyKind) error {
	if err := s.fail("LockActivation"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activations = append(s.activations, kind)
	return nil
}

// CurrentRoute implements legal.Store.
func (s *Store) CurrentRoute(_ context.Context, kind legal.PolicyKind) (*legal.CurrentRoute, error) {
	if err := s.fail("CurrentRoute"); err != nil {
		return nil, err
	}
	return s.currentRoute(kind), nil
}

// RequestOG implements legal.Store: like the OG runtime it plans only a
// version the route shows.
func (s *Store) RequestOG(_ context.Context, kind legal.PolicyKind, id, locale string, allLocales bool, reason string) error {
	if err := s.fail("RequestOG"); err != nil {
		return err
	}
	if current := s.currentRoute(kind); current == nil || current.ID != id {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ogRequests = append(s.ogRequests, OGRequest{Kind: kind, ID: id, Locale: locale, AllLocales: allLocales, Reason: reason})
	return nil
}

// ReleaseRoute implements legal.Store.
func (s *Store) ReleaseRoute(_ context.Context, kind legal.PolicyKind) error {
	if err := s.fail("ReleaseRoute"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.released = append(s.released, kind)
	return nil
}

// sealLocked completes a run's references from its kind and policy.
func (s *Store) sealLocked(run Run) Run {
	policyID := run.PolicyID
	switch run.Kind {
	case legal.Terms:
		run.TermsID = &policyID
	case legal.Privacy:
		run.PrivacyID = &policyID
	}
	eventKey := run.EventKey
	run.TemplateEventKey = &eventKey
	run.TemplateData = map[string]any{}
	for key, value := range run.Data {
		run.TemplateData[key] = value
	}
	if record, ok := s.versions[versionKey{run.Kind, run.PolicyID}]; ok && run.Version == 0 {
		run.Version = record.Version.Version
	}
	return run
}

// SealNoticeRun implements legal.Store; Fail["SealNoticeRun"] plays an
// unavailable notice template.
func (s *Store) SealNoticeRun(_ context.Context, kind legal.PolicyKind, id, eventKey string, templateData map[string]string, scheduledAt time.Time) (*legal.NoticeRun, error) {
	if err := s.fail("SealNoticeRun"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.sealLocked(Run{
		NoticeRun: legal.NoticeRun{ID: uuid.NewString(), RunKind: legal.EmailDeliveryRunKindLegalNotice},
		Kind:      kind, PolicyID: id, Status: RunScheduled, EventKey: eventKey,
		Data: maps.Clone(templateData), ScheduledAt: scheduledAt,
	})
	s.runs = append(s.runs, run)
	sealed := run.NoticeRun
	return &sealed, nil
}

// LatestSealedVersion implements legal.Store.
func (s *Store) LatestSealedVersion(_ context.Context, kind legal.PolicyKind) (int, error) {
	if err := s.fail("LatestSealedVersion"); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	highest := 0
	for _, run := range s.runs {
		if run.Kind == kind && run.Version > highest {
			highest = run.Version
		}
	}
	return highest, nil
}

// CancelScheduledNotices implements legal.Store, with the started-run
// rejection Campaign answers.
func (s *Store) CancelScheduledNotices(_ context.Context, kind legal.PolicyKind, id string) error {
	if err := s.fail("CancelScheduledNotices"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, run := range s.runs {
		if run.Kind == kind && run.PolicyID == id && (run.Status == RunSending || run.Status == RunSent) {
			return errNoticeStarted
		}
	}
	for i := range s.runs {
		if s.runs[i].Kind == kind && s.runs[i].PolicyID == id && s.runs[i].Status == RunScheduled {
			s.runs[i].Status = RunCancelled
		}
	}
	return nil
}

// StopNoticesOfDeletedVersion implements legal.Store.
func (s *Store) StopNoticesOfDeletedVersion(_ context.Context, kind legal.PolicyKind, id string) error {
	if err := s.fail("StopNoticesOfDeletedVersion"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.runs {
		if s.runs[i].Kind != kind || s.runs[i].PolicyID != id {
			continue
		}
		s.runs[i].StoppedAfter = true
		if s.runs[i].Status == RunScheduled || s.runs[i].Status == RunSending {
			s.runs[i].Status = RunCancelled
		}
	}
	return nil
}

// UpdateNoticeRuns implements legal.Store.
func (s *Store) UpdateNoticeRuns(_ context.Context, kind legal.PolicyKind, id string) ([]legal.NoticeRun, error) {
	if err := s.fail("UpdateNoticeRuns"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var runs []legal.NoticeRun
	for _, run := range s.runs {
		if run.Kind == kind && run.PolicyID == id && strings.HasSuffix(run.EventKey, "_update") {
			runs = append(runs, run.NoticeRun)
		}
	}
	return runs, nil
}

// CreateAutomaticPreview implements legal.Store: a retry finds the link it
// stored, a token bound to any other link fails.
func (s *Store) CreateAutomaticPreview(_ context.Context, preview legal.AutomaticPreview) error {
	if err := s.fail("CreateAutomaticPreview"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.previews[preview.Token]; ok {
		if existing.EntityType != preview.EntityType || existing.EntityID != preview.EntityID || !existing.ExpiresAt.Equal(preview.ExpiresAt) {
			return errPreviewTokenBound
		}
		return nil
	}
	s.previews[preview.Token] = preview
	return nil
}

// DeleteAutomaticPreview implements legal.Store.
func (s *Store) DeleteAutomaticPreview(_ context.Context, token, entityType, entityID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if preview, ok := s.previews[token]; ok && preview.EntityType == entityType && preview.EntityID == entityID {
		delete(s.previews, token)
	}
	return nil
}

// DeletePreviewLinks implements legal.Store.
func (s *Store) DeletePreviewLinks(_ context.Context, entityType, entityID string) error {
	if err := s.fail("DeletePreviewLinks"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, preview := range s.previews {
		if preview.EntityType == entityType && preview.EntityID == entityID {
			delete(s.previews, token)
		}
	}
	s.linkDeletes = append(s.linkDeletes, entityType+"/"+entityID)
	return nil
}

// ResolveSessionPrincipal implements legal.Store.
func (s *Store) ResolveSessionPrincipal(_ context.Context, sessionID string) (*auth.UserInfo, error) {
	if err := s.fail("ResolveSessionPrincipal"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	principal, ok := s.sessions[sessionID]
	if !ok {
		return nil, auth.ErrSessionPrincipalInvalid
	}
	return principal, nil
}

// RequireCurrentContributors implements legal.Store; Fail plays a
// contributor who may no longer save.
func (s *Store) RequireCurrentContributors(_ context.Context, _ legal.PolicyKind, _ string, contributors []string) error {
	if err := s.fail("RequireCurrentContributors"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contributions = append(s.contributions, slices.Clone(contributors))
	return nil
}

// PreferredLocale implements legal.Store.
func (s *Store) PreferredLocale(_ context.Context, memberID string) (*string, error) {
	if err := s.fail("PreferredLocale"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.preferred[memberID], nil
}

// TranslationSettings implements legal.Store.
func (s *Store) TranslationSettings(context.Context) (translation.RuntimeSettings, error) {
	if err := s.fail("TranslationSettings"); err != nil {
		return translation.RuntimeSettings{}, err
	}
	return s.Settings, nil
}

// PublishContentUpdate implements legal.Store.
func (s *Store) PublishContentUpdate(_ context.Context, update legal.ContentUpdate) error {
	if err := s.fail("PublishContentUpdate"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, update)
	return nil
}
