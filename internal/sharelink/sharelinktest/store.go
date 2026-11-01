// Package sharelinktest provides the in-memory Share Link Store, its unit of
// work and the Store contract both Store implementations keep.
package sharelinktest

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/sharelink"
)

// Store is the in-memory sharelink.Store. It knows no schema and takes no
// lock: its unit of work serializes transactions instead.
type Store struct {
	mu      sync.Mutex
	links   map[string]sharelink.Link
	targets map[targetKey]sharelink.Target
}

type targetKey struct {
	entityType sharelink.EntityType
	entityID   string
}

// keyOf is the stored row of a target: a Form's submission and dashboard
// links name the same Form.
func keyOf(entityType sharelink.EntityType, entityID string) targetKey {
	if entityType == sharelink.EntityFormDashboard {
		entityType = sharelink.EntityForm
	}
	return targetKey{entityType, entityID}
}

var _ sharelink.Store = (*Store)(nil)

// New returns an empty Store and the unit of work over it.
func New() (*Store, *mutationtest.UnitOfWork[sharelink.Store]) {
	store := NewStore()
	return store, mutationtest.New[sharelink.Store](store)
}

// NewStore returns an empty Store, for a test that wraps it before building
// the unit of work.
func NewStore() *Store {
	return &Store{links: map[string]sharelink.Link{}, targets: map[targetKey]sharelink.Target{}}
}

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	links, targets := maps.Clone(s.links), maps.Clone(s.targets)
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.links, s.targets = links, targets
	}
}

// SeedTarget stores a link target that another domain owns.
func (s *Store) SeedTarget(entityType sharelink.EntityType, entityID string, target sharelink.Target) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targets[keyOf(entityType, entityID)] = target
}

// SeedLink stores link as it is, with an ID when it has none, and returns it.
func (s *Store) SeedLink(link sharelink.Link) sharelink.Link {
	s.mu.Lock()
	defer s.mu.Unlock()
	if link.ID == "" {
		link.ID = uuid.NewString()
	}
	s.links[link.ID] = link
	return link
}

// Links returns every stored link, oldest first.
func (s *Store) Links() []sharelink.Link {
	s.mu.Lock()
	defer s.mu.Unlock()
	links := slices.Collect(maps.Values(s.links))
	slices.SortStableFunc(links, func(a, b sharelink.Link) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return links
}

func (s *Store) ListLinks(_ context.Context, entityType sharelink.EntityType, entityID string) ([]sharelink.Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	links := []sharelink.Link{}
	for _, link := range s.links {
		if link.EntityType == entityType && link.EntityID == entityID {
			links = append(links, link)
		}
	}
	slices.SortStableFunc(links, func(a, b sharelink.Link) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return links, nil
}

func (s *Store) Link(_ context.Context, id string) (sharelink.Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	link, ok := s.links[id]
	if !ok {
		return sharelink.Link{}, sharelink.ErrLinkNotFound
	}
	return link, nil
}

func (s *Store) LinkByToken(_ context.Context, token string) (sharelink.Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if link, ok := s.byToken(token); ok {
		return link, nil
	}
	return sharelink.Link{}, sharelink.ErrLinkNotFound
}

func (s *Store) LiveLink(_ context.Context, token string, entityType sharelink.EntityType, entityID string, now time.Time) (sharelink.Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	link, ok := s.byToken(token)
	if !ok || link.EntityType != entityType || link.EntityID != entityID || !link.ExpiresAt.After(now) {
		return sharelink.Link{}, sharelink.ErrLinkNotFound
	}
	return link, nil
}

func (s *Store) byToken(token string) (sharelink.Link, bool) {
	for _, link := range s.links {
		if link.Token == token {
			return link, true
		}
	}
	return sharelink.Link{}, false
}

func (s *Store) InsertLink(_ context.Context, link sharelink.Link) (sharelink.Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byToken(link.Token); ok {
		return sharelink.Link{}, errors.New("share link token already exists")
	}
	link.ID = uuid.NewString()
	s.links[link.ID] = link
	return link, nil
}

func (s *Store) InsertLinkUnlessTokenExists(_ context.Context, link sharelink.Link) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byToken(link.Token); ok {
		return nil
	}
	link.ID = uuid.NewString()
	s.links[link.ID] = link
	return nil
}

func (s *Store) DeleteLink(_ context.Context, link sharelink.Link) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.links[link.ID]
	if !ok || stored.EntityType != link.EntityType || stored.EntityID != link.EntityID {
		return sharelink.ErrLinkNotFound
	}
	delete(s.links, link.ID)
	return nil
}

func (s *Store) DeleteTargetLinks(_ context.Context, entityID string, entityTypes ...sharelink.EntityType) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	maps.DeleteFunc(s.links, func(_ string, link sharelink.Link) bool {
		return link.EntityID == entityID && slices.Contains(entityTypes, link.EntityType)
	})
	return nil
}

func (s *Store) DeletePasswordFreeLink(_ context.Context, token string, entityType sharelink.EntityType, entityID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	maps.DeleteFunc(s.links, func(_ string, link sharelink.Link) bool {
		return link.Token == token && link.EntityType == entityType && link.EntityID == entityID && link.PasswordHash == nil
	})
	return nil
}

func (s *Store) DeleteExpiredLinks(_ context.Context, now time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var deleted int64
	maps.DeleteFunc(s.links, func(_ string, link sharelink.Link) bool {
		expired := link.ExpiresAt.Before(now)
		if expired {
			deleted++
		}
		return expired
	})
	return deleted, nil
}

func (s *Store) Target(_ context.Context, entityType sharelink.EntityType, entityID string) (sharelink.Target, error) {
	return s.target(entityType, entityID)
}

func (s *Store) LockTarget(_ context.Context, entityType sharelink.EntityType, entityID string) (sharelink.Target, error) {
	return s.target(entityType, entityID)
}

func (s *Store) target(entityType sharelink.EntityType, entityID string) (sharelink.Target, error) {
	if !entityType.Known() {
		return sharelink.Target{}, fmt.Errorf("share link target type %q has no table", entityType)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	target, ok := s.targets[keyOf(entityType, entityID)]
	if !ok {
		return sharelink.Target{}, sharelink.ErrTargetNotFound
	}
	return target, nil
}
