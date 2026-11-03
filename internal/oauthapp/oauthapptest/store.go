// Package oauthapptest provides the in-memory OAuth app Store and unit of
// work, an in-memory Hydra, and an Eligibility a test configures.
package oauthapptest

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/oauthapp"
)

// Store implements oauthapp.Store in memory. It takes no lock; its unit of
// work serializes transactions.
type Store struct {
	mu   sync.Mutex
	apps map[string]oauthapp.Record
}

var _ oauthapp.Store = (*Store)(nil)

// New returns an empty Store and the unit of work over it.
func New() (*Store, *mutationtest.UnitOfWork[oauthapp.Store]) {
	store := &Store{apps: map[string]oauthapp.Record{}}
	return store, mutationtest.New[oauthapp.Store](store)
}

// Savepoint captures the apps for the unit of work's rollback.
func (s *Store) Savepoint() mutationtest.Checkpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	saved := maps.Clone(s.apps)
	return mutationtest.Checkpoint{State: saved, Restore: func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.apps = maps.Clone(saved)
	}}
}

// Record returns the stored app, for a test to inspect.
func (s *Store) Record(clientID string) (oauthapp.Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.apps[clientID]
	return record, ok
}

func (s *Store) Apps(context.Context) ([]oauthapp.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := slices.Collect(maps.Values(s.apps))
	slices.SortFunc(records, func(a, b oauthapp.Record) int { return strings.Compare(a.ClientID, b.ClientID) })
	return records, nil
}

func (s *Store) App(_ context.Context, clientID string) (oauthapp.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.apps[clientID]
	if !ok {
		return oauthapp.Record{}, oauthapp.ErrAppNotFound
	}
	return record, nil
}

func (s *Store) LockApp(ctx context.Context, clientID string) (oauthapp.Record, error) {
	return s.App(ctx, clientID)
}

func (s *Store) InsertApp(_ context.Context, record oauthapp.Record) (oauthapp.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.apps[record.ClientID]; exists {
		return oauthapp.Record{}, oauthapp.ErrAppExists
	}
	now := time.Now().UTC()
	record.Revision, record.CreatedAt, record.UpdatedAt = uuid.NewString(), now, now
	record.MemberCriteriaIDs = slices.Clone(record.MemberCriteriaIDs)
	s.apps[record.ClientID] = record
	return record, nil
}

func (s *Store) UpdateApp(_ context.Context, record oauthapp.Record) (oauthapp.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, exists := s.apps[record.ClientID]
	if !exists {
		return oauthapp.Record{}, oauthapp.ErrAppNotFound
	}
	stored.Access, stored.MemberCriteriaIDs = record.Access, slices.Clone(record.MemberCriteriaIDs)
	stored.Revision, stored.UpdatedAt = uuid.NewString(), time.Now().UTC()
	s.apps[record.ClientID] = stored
	return stored, nil
}

func (s *Store) DeleteApp(_ context.Context, clientID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.apps[clientID]; !exists {
		return oauthapp.ErrAppNotFound
	}
	delete(s.apps, clientID)
	return nil
}

// Hydra is an in-memory Hydra Admin API. Secrets holds the last secret
// written to each client, as the token endpoint would accept it.
type Hydra struct {
	mu      sync.Mutex
	clients map[string]oauthapp.Client
	Secrets map[string]string
	// FailWrites fails every write when set.
	FailWrites error
}

var _ oauthapp.Clients = (*Hydra)(nil)

// NewHydra returns an empty Hydra.
func NewHydra() *Hydra {
	return &Hydra{clients: map[string]oauthapp.Client{}, Secrets: map[string]string{}}
}

// Seed stores client as another writer, such as the MCP facade, would.
func (h *Hydra) Seed(client oauthapp.Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[client.ID] = client
}

// Client returns the stored client, for a test to inspect.
func (h *Hydra) Client(clientID string) (oauthapp.Client, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	client, ok := h.clients[clientID]
	return client, ok
}

func (h *Hydra) List(context.Context) ([]oauthapp.Client, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Collect(maps.Values(h.clients)), nil
}

func (h *Hydra) Get(_ context.Context, clientID string) (oauthapp.Client, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	client, ok := h.clients[clientID]
	if !ok {
		return oauthapp.Client{}, oauthapp.ErrClientNotFound
	}
	return client, nil
}

func (h *Hydra) Create(_ context.Context, client oauthapp.Client) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.FailWrites != nil {
		return h.FailWrites
	}
	if _, exists := h.clients[client.ID]; exists {
		return oauthapp.ErrClientExists
	}
	h.store(client)
	return nil
}

func (h *Hydra) Replace(_ context.Context, client oauthapp.Client) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.FailWrites != nil {
		return h.FailWrites
	}
	if _, exists := h.clients[client.ID]; !exists {
		return oauthapp.ErrClientNotFound
	}
	h.store(client)
	return nil
}

func (h *Hydra) Delete(_ context.Context, clientID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.FailWrites != nil {
		return h.FailWrites
	}
	if _, exists := h.clients[clientID]; !exists {
		return oauthapp.ErrClientNotFound
	}
	delete(h.clients, clientID)
	delete(h.Secrets, clientID)
	return nil
}

// store keeps a written secret aside, as Hydra stores only its hash.
func (h *Hydra) store(client oauthapp.Client) {
	if client.Secret != "" {
		h.Secrets[client.ID] = client.Secret
		client.Secret = ""
	}
	h.clients[client.ID] = client
}

// Eligibility answers who a sign-in app admits from what a test set.
type Eligibility struct {
	// Criteria are the Member criteria that exist, by ID.
	Criteria map[string]oauthapp.CriteriaSummary
	// Inactive Members are not live Members.
	Inactive map[string]bool
	// Matches are the criteria each Member matches.
	Matches map[string][]string
}

var _ oauthapp.Eligibility = (*Eligibility)(nil)

func (e *Eligibility) CriteriaSummaries(_ context.Context, ids []string) ([]oauthapp.CriteriaSummary, error) {
	var summaries []oauthapp.CriteriaSummary
	for _, id := range ids {
		if summary, ok := e.Criteria[id]; ok {
			summaries = append(summaries, summary)
		}
	}
	return summaries, nil
}

func (e *Eligibility) ActiveMember(_ context.Context, user *auth.UserInfo) (bool, error) {
	return user != nil && !user.Banned && !e.Inactive[user.MemberID.String()], nil
}

func (e *Eligibility) MatchesAny(ctx context.Context, user *auth.UserInfo, ids []string) (bool, error) {
	if active, _ := e.ActiveMember(ctx, user); !active {
		return false, nil
	}
	for _, id := range ids {
		if e.Criteria[id].Eligible && slices.Contains(e.Matches[user.MemberID.String()], id) {
			return true, nil
		}
	}
	return false, nil
}
