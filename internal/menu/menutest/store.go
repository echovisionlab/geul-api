// Package menutest keeps the Menu Store in memory for use-case tests, and
// holds the Store contract both the fake and menupostgres keep.
package menutest

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
)

// Store is the in-memory Menu Store. It knows the menu_name_key and
// content_document_id uniqueness, the content document compare-and-set and
// the locale cascade of a deleted Menu; it takes no lock (the unit of work
// serializes transactions). Every item target exists unless MissTarget
// names it.
type Store struct {
	mu        sync.Mutex
	menus     map[string]menu.Root
	documents map[uuid.UUID]menu.ContentDocument
	locales   map[string]map[string]menu.LocaleValues
	missing   map[menu.TargetReference]bool

	// SiteSettingsErr fails ClearSiteSettingsMenu; ContributorsErr fails
	// RequireContributors.
	SiteSettingsErr error
	ContributorsErr error

	lockedTargets [][]menu.TargetReference
	cleared       []string
	contributors  []ContributorCheck
	signals       []menu.AIDocumentChange
}

// ContributorCheck is one RequireContributors call.
type ContributorCheck struct {
	MenuID       string
	Contributors []string
	Permission   menu.ContributorPermission
}

var _ menu.Store = (*Store)(nil)

// NewStore returns an empty fake.
func NewStore() *Store {
	return &Store{
		menus: map[string]menu.Root{}, documents: map[uuid.UUID]menu.ContentDocument{},
		locales: map[string]map[string]menu.LocaleValues{}, missing: map[menu.TargetReference]bool{},
	}
}

// New returns an empty fake and the unit of work over it.
func New() (*Store, *mutationtest.UnitOfWork[menu.Store]) {
	store := NewStore()
	return store, mutationtest.New[menu.Store](store)
}

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	menus, documents := maps.Clone(s.menus), maps.Clone(s.documents)
	locales := make(map[string]map[string]menu.LocaleValues, len(s.locales))
	for id, rows := range s.locales {
		locales[id] = maps.Clone(rows)
	}
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.menus, s.documents, s.locales = menus, documents, locales
	}
}

// Seed stores root with a compact content document at revision and the
// locale rows given, as a Menu written by an earlier transaction. A zero ID,
// document or timestamp is filled in.
func (s *Store) Seed(root menu.Root, revision uuid.UUID, locales ...menu.LocaleValues) menu.Root {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if root.CreatedAt.IsZero() {
		root.CreatedAt, root.UpdatedAt = now, now
	}
	if root.ID == "" {
		root.ID = uuid.NewString()
	}
	if root.ContentDocumentID == "" {
		root.ContentDocumentID = uuid.NewString()
	}
	id := uuid.MustParse(root.ContentDocumentID)
	s.documents[id] = menu.ContentDocument{ID: id, Profile: "compact", Revision: revision.String()}
	s.menus[root.ID] = cloneRoot(root)
	for _, values := range locales {
		values.MenuID = root.ID
		if values.UpdatedAt.IsZero() {
			values.UpdatedAt = now
		}
		s.putLocale(values)
	}
	return cloneRoot(root)
}

// SetDocument replaces a stored content document, for a test of a corrupt
// or foreign document.
func (s *Store) SetDocument(document menu.ContentDocument) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.documents[document.ID] = document
}

// MissTarget makes LockTargets answer that ref does not exist.
func (s *Store) MissTarget(ref menu.TargetReference) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.missing[ref] = true
}

// Stored returns the stored root of id.
func (s *Store) Stored(id string) (menu.Root, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, ok := s.menus[id]
	return cloneRoot(root), ok
}

// StoredDocument returns the content document of id.
func (s *Store) StoredDocument(id string) (menu.ContentDocument, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	parsed, err := uuid.Parse(id)
	if err != nil {
		return menu.ContentDocument{}, false
	}
	document, ok := s.documents[parsed]
	return document, ok
}

// StoredLocale returns the stored labels of one locale.
func (s *Store) StoredLocale(menuID, locale string) (menu.LocaleValues, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values, ok := s.locales[menuID][locale]
	return cloneValues(values), ok
}

// MenuCount returns how many roots are stored.
func (s *Store) MenuCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.menus)
}

// DocumentCount returns how many content documents are stored.
func (s *Store) DocumentCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.documents)
}

// LockedTargets returns the targets of every LockTargets call, in order.
func (s *Store) LockedTargets() [][]menu.TargetReference {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.lockedTargets)
}

// ClearedSiteSettings returns the Menus whose Site Settings slots were
// cleared, in order.
func (s *Store) ClearedSiteSettings() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.cleared)
}

// Contributors returns every RequireContributors call, in order.
func (s *Store) Contributors() []ContributorCheck {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.contributors)
}

// Signals returns every scheduled AI document change, in order. A signal of
// a transaction that rolled back stays here.
func (s *Store) Signals() []menu.AIDocumentChange {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.signals)
}

func (s *Store) Menu(_ context.Context, id string) (menu.Root, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, ok := s.menus[id]
	if !ok {
		return menu.Root{}, menu.ErrMenuNotFound
	}
	return cloneRoot(root), nil
}

func (s *Store) LockMenu(ctx context.Context, id string) (menu.Root, error) {
	return s.Menu(ctx, id)
}

func (s *Store) LockMenus(context.Context) ([]menu.Root, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	roots := s.rootsBy(func(a, b menu.Root) bool { return a.ID < b.ID })
	return roots, nil
}

func (s *Store) MenuExists(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.menus[id]
	return ok, nil
}

func (s *Store) ListMenus(_ context.Context, limit, offset int) ([]menu.Root, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	roots := s.rootsBy(func(a, b menu.Root) bool { return a.Name < b.Name })
	total := int64(len(roots))
	if offset >= len(roots) {
		return []menu.Root{}, total, nil
	}
	roots = roots[offset:]
	if limit < len(roots) {
		roots = roots[:limit]
	}
	return roots, total, nil
}

func (s *Store) MenuNameTaken(_ context.Context, name, exceptID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nameTaken(name, exceptID), nil
}

func (s *Store) InsertMenu(_ context.Context, root menu.Root) (menu.Root, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nameTaken(root.Name, "") {
		return menu.Root{}, menu.ErrMenuNameTaken
	}
	document, err := uuid.Parse(root.ContentDocumentID)
	if err != nil || s.documents[document].Profile != "compact" {
		return menu.Root{}, errors.New("menu content document must be an existing compact document")
	}
	for _, other := range s.menus {
		if other.ContentDocumentID == root.ContentDocumentID {
			return menu.Root{}, errors.New("menu content document already has an owner")
		}
	}
	root.ID = uuid.NewString()
	s.menus[root.ID] = cloneRoot(root)
	return cloneRoot(root), nil
}

func (s *Store) UpdateMenu(_ context.Context, id string, change menu.RootChange) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, ok := s.menus[id]
	if !ok {
		return menu.ErrMenuNotFound
	}
	if change.Name != nil {
		if s.nameTaken(*change.Name, id) {
			return menu.ErrMenuNameTaken
		}
		root.Name = *change.Name
	}
	if change.ItemsJSON != nil {
		root.ItemsJSON = slices.Clone(change.ItemsJSON)
	}
	root.UpdatedAt = change.UpdatedAt
	s.menus[id] = root
	return nil
}

func (s *Store) DeleteMenu(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.menus, id)
	delete(s.locales, id)
	return nil
}

func (s *Store) ContentDocument(_ context.Context, id uuid.UUID) (menu.ContentDocument, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, ok := s.documents[id]
	if !ok {
		return menu.ContentDocument{}, menu.ErrContentDocumentNotFound
	}
	return document, nil
}

func (s *Store) LockContentDocument(ctx context.Context, id uuid.UUID) (menu.ContentDocument, error) {
	return s.ContentDocument(ctx, id)
}

func (s *Store) InsertContentDocument(_ context.Context, id uuid.UUID, profile string, revision uuid.UUID, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.documents[id]; exists {
		return errors.New("content document already exists")
	}
	s.documents[id] = menu.ContentDocument{ID: id, Profile: profile, Revision: revision.String()}
	return nil
}

func (s *Store) AdvanceContentDocument(_ context.Context, menuID string, id, expected, next uuid.UUID, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, ok := s.documents[id]
	if !ok || document.Revision != expected.String() || s.menus[menuID].ContentDocumentID != id.String() {
		return menu.ErrRevisionChanged
	}
	document.Revision = next.String()
	s.documents[id] = document
	return nil
}

func (s *Store) DeleteContentDocument(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.documents[id]; !ok {
		return menu.ErrContentDocumentNotFound
	}
	delete(s.documents, id)
	return nil
}

func (s *Store) Locale(_ context.Context, menuID, locale string) (menu.LocaleValues, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values, ok := s.locales[menuID][locale]
	if !ok {
		return menu.LocaleValues{}, menu.ErrLocaleNotFound
	}
	return cloneValues(values), nil
}

func (s *Store) LockLocale(ctx context.Context, menuID, locale string) (menu.LocaleValues, error) {
	return s.Locale(ctx, menuID, locale)
}

func (s *Store) Locales(_ context.Context, menuID string) ([]menu.LocaleValues, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := make([]menu.LocaleValues, 0, len(s.locales[menuID]))
	for _, value := range s.locales[menuID] {
		values = append(values, cloneValues(value))
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Locale < values[j].Locale })
	return values, nil
}

func (s *Store) InsertLocale(_ context.Context, values menu.LocaleValues) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.menus[values.MenuID]; !ok {
		return errors.New("menu locale values need their Menu")
	}
	if _, exists := s.locales[values.MenuID][values.Locale]; exists {
		return errors.New("menu locale values already exist")
	}
	s.putLocale(values)
	return nil
}

func (s *Store) UpdateLocale(_ context.Context, values menu.LocaleValues) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.locales[values.MenuID][values.Locale]; !exists {
		return menu.ErrLocaleNotFound
	}
	s.putLocale(values)
	return nil
}

func (s *Store) UpsertLocale(_ context.Context, values menu.LocaleValues) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.menus[values.MenuID]; !ok {
		return errors.New("menu locale values need their Menu")
	}
	s.putLocale(values)
	return nil
}

func (s *Store) DeleteLocale(_ context.Context, menuID, locale string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.locales[menuID][locale]; !exists {
		return menu.ErrLocaleNotFound
	}
	delete(s.locales[menuID], locale)
	return nil
}

func (s *Store) LockTargets(_ context.Context, targets []menu.TargetReference) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lockedTargets = append(s.lockedTargets, slices.Clone(targets))
	for _, target := range targets {
		if s.missing[target] {
			return menu.TargetNotFoundError{LinkType: target.LinkType}
		}
	}
	return nil
}

func (s *Store) ClearSiteSettingsMenu(_ context.Context, menuID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.SiteSettingsErr != nil {
		return s.SiteSettingsErr
	}
	s.cleared = append(s.cleared, menuID)
	return nil
}

func (s *Store) RequireContributors(_ context.Context, menuID string, contributors []string, permission menu.ContributorPermission) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contributors = append(s.contributors, ContributorCheck{MenuID: menuID, Contributors: slices.Clone(contributors), Permission: permission})
	return s.ContributorsErr
}

func (s *Store) PublishAIDocumentChange(_ context.Context, change menu.AIDocumentChange) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.signals = append(s.signals, change)
	return nil
}

func (s *Store) nameTaken(name, exceptID string) bool {
	for id, root := range s.menus {
		if root.Name == name && id != exceptID {
			return true
		}
	}
	return false
}

func (s *Store) rootsBy(less func(a, b menu.Root) bool) []menu.Root {
	roots := make([]menu.Root, 0, len(s.menus))
	for _, root := range s.menus {
		roots = append(roots, cloneRoot(root))
	}
	sort.Slice(roots, func(i, j int) bool { return less(roots[i], roots[j]) })
	return roots
}

func (s *Store) putLocale(values menu.LocaleValues) {
	if s.locales[values.MenuID] == nil {
		s.locales[values.MenuID] = map[string]menu.LocaleValues{}
	}
	s.locales[values.MenuID][values.Locale] = cloneValues(values)
}

func cloneRoot(root menu.Root) menu.Root {
	root.ItemsJSON = slices.Clone(root.ItemsJSON)
	return root
}

func cloneValues(values menu.LocaleValues) menu.LocaleValues {
	values.Labels = maps.Clone(values.Labels)
	if values.Labels == nil {
		values.Labels = map[string]string{}
	}
	return values
}
