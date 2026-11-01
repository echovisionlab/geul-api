// Package mapthemetest provides the in-memory Map Theme Store, its unit of
// work and the Store contract the PostgreSQL store keeps too.
package mapthemetest

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/maptheme"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

// Store implements maptheme.Store in memory. Like the schema it starts with
// one theme that is the site default. It takes no lock: its unit of work
// serializes transactions.
type Store struct {
	mu           sync.Mutex
	themes       map[string]maptheme.Theme
	defaultID    string
	contributors map[string]string
	contributor  error
}

var _ maptheme.Store = (*Store)(nil)

// New returns an empty Store with its default theme, and the unit of work
// over it.
func New() (*Store, *mutationtest.UnitOfWork[maptheme.Store]) {
	store := &Store{themes: map[string]maptheme.Theme{}, contributors: map[string]string{}}
	store.defaultID = store.Seed(Snapshot("Built-in")).ID
	return store, mutationtest.New[maptheme.Store](store)
}

// Snapshot is a complete, normalized snapshot named name.
func Snapshot(name string) maptheme.Snapshot {
	variant := func(background string) maptheme.Variant {
		return maptheme.Variant{
			BackgroundColor: background, WaterColor: "#112233", LandColor: "#223344",
			RoadColor: "#334455", BuildingFillColor: "#445566", BuildingStrokeEnabled: true,
			BuildingStrokeColor: "#556677", CalloutLineColor: "#667788",
			CalloutTextColor: "#778899", CalloutBackgroundColor: "rgba(10,20,30,0.5)",
			CalloutDescriptionColor: "#8899aa", AttributionColor: "transparent",
			LabelTextColor: "#99aabb", ClusterColor: "#aabbcc",
			ClusterHoverColor: "#bbccdd", ClusterTextColor: "#ccddee",
			ClusterTextHoverColor: "#ddeeff", CalloutHoverLineColor: "#123",
			CalloutHoverTextColor: "#1234", CalloutHoverDescriptionColor: "#123456",
			CalloutHoverBackgroundColor: "#12345678",
		}
	}
	return maptheme.Snapshot{
		Name: name,
		Settings: maptheme.Settings{
			CalloutScale: 1.25, CalloutOffsetX: 2, CalloutOffsetY: 3,
			CalloutFields: []string{"name", "address"}, AttributionFontSize: 12, ShowAreaLabels: true,
		},
		LightVariant: variant("#ffffff"),
		DarkVariant:  variant("#000000"),
	}
}

// Seed stores snapshot as a new theme, outside any transaction.
func (s *Store) Seed(snapshot maptheme.Snapshot) maptheme.Theme {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.insert(snapshot)
}

// SeedContributor makes memberID a live Member linked to identityID, as
// LockContributor resolves it.
func (s *Store) SeedContributor(memberID, identityID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contributors[memberID] = identityID
}

// FailContributors makes every later LockContributor fail with err, as a
// failed Member lookup does; nil restores the seeded contributors.
func (s *Store) FailContributors(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contributor = err
}

// Stored returns the stored theme id.
func (s *Store) Stored(id string) (maptheme.Theme, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	theme, ok := s.themes[id]
	return clone(theme), ok
}

// Default returns the site default theme.
func (s *Store) Default() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.defaultID
}

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	themes, defaultID := maps.Clone(s.themes), s.defaultID
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.themes, s.defaultID = themes, defaultID
	}
}

// now is a stored timestamp: PostgreSQL keeps microseconds and no
// monotonic reading.
func now() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

// clone copies the one slice a theme holds, so a caller cannot change the
// store through it.
func clone(theme maptheme.Theme) maptheme.Theme {
	theme.Snapshot.Settings.CalloutFields = slices.Clone(theme.Snapshot.Settings.CalloutFields)
	return theme
}

func (s *Store) insert(snapshot maptheme.Snapshot) maptheme.Theme {
	at := now()
	theme := maptheme.Theme{ID: uuid.NewString(), Revision: 1, Snapshot: stored(snapshot), CreatedAt: at, UpdatedAt: at}
	s.themes[theme.ID] = theme
	return clone(theme)
}

// stored is snapshot as the map_theme row keeps it: the scale as float32.
func stored(snapshot maptheme.Snapshot) maptheme.Snapshot {
	snapshot.Settings.CalloutScale = float64(float32(snapshot.Settings.CalloutScale))
	snapshot.Settings.CalloutFields = slices.Clone(snapshot.Settings.CalloutFields)
	return snapshot
}

// DefaultThemeID implements maptheme.Store.
func (s *Store) DefaultThemeID(context.Context) (string, error) {
	return s.Default(), nil
}

// LockDefaultThemeID implements maptheme.Store.
func (s *Store) LockDefaultThemeID(context.Context) (string, error) {
	return s.Default(), nil
}

// SetDefaultThemeID implements maptheme.Store; like the foreign key, the
// theme must exist.
func (s *Store) SetDefaultThemeID(_ context.Context, themeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.themes[themeID]; !ok {
		return errors.New("default map theme does not exist")
	}
	s.defaultID = themeID
	return nil
}

// Themes implements maptheme.Store.
func (s *Store) Themes(context.Context) ([]maptheme.Theme, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	themes := make([]maptheme.Theme, 0, len(s.themes))
	for _, theme := range s.themes {
		themes = append(themes, clone(theme))
	}
	slices.SortFunc(themes, func(a, b maptheme.Theme) int {
		return cmp.Or(cmp.Compare(a.Snapshot.Name, b.Snapshot.Name), cmp.Compare(a.ID, b.ID))
	})
	return themes, nil
}

// ThemesByID implements maptheme.Store.
func (s *Store) ThemesByID(_ context.Context, ids []string) ([]maptheme.Theme, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	themes := []maptheme.Theme{}
	for _, id := range slices.Compact(slices.Sorted(slices.Values(ids))) {
		if theme, ok := s.themes[id]; ok {
			themes = append(themes, clone(theme))
		}
	}
	return themes, nil
}

// Theme implements maptheme.Store.
func (s *Store) Theme(_ context.Context, id string) (maptheme.Theme, error) {
	theme, ok := s.Stored(id)
	if !ok {
		return maptheme.Theme{}, maptheme.ErrThemeNotFound
	}
	return theme, nil
}

// LockTheme implements maptheme.Store.
func (s *Store) LockTheme(ctx context.Context, id string) (maptheme.Theme, error) {
	return s.Theme(ctx, id)
}

// InsertTheme implements maptheme.Store.
func (s *Store) InsertTheme(_ context.Context, snapshot maptheme.Snapshot) (maptheme.Theme, error) {
	return s.Seed(snapshot), nil
}

// UpdateSnapshot implements maptheme.Store.
func (s *Store) UpdateSnapshot(_ context.Context, id string, expectedRevision int64, snapshot maptheme.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	theme, ok := s.themes[id]
	if !ok || theme.Revision != expectedRevision {
		return maptheme.ErrRevisionChanged
	}
	theme.Snapshot, theme.Revision, theme.UpdatedAt = stored(snapshot), theme.Revision+1, now()
	s.themes[id] = theme
	return nil
}

// DeleteTheme implements maptheme.Store.
func (s *Store) DeleteTheme(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.themes[id]; !ok {
		return maptheme.ErrThemeNotFound
	}
	if id == s.defaultID {
		return maptheme.ErrDefaultTheme
	}
	delete(s.themes, id)
	return nil
}

// LockContributor implements maptheme.Store. A Member SeedContributor did
// not seed answers not found, which is not a failure to decide.
func (s *Store) LockContributor(_ context.Context, memberID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.contributor != nil {
		return "", s.contributor
	}
	identityID, ok := s.contributors[memberID]
	if !ok {
		return "", domainerr.ResourceNotFound("member", memberID)
	}
	return identityID, nil
}
