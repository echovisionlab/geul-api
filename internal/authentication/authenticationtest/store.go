// Package authenticationtest keeps the authentication Store in memory, with
// the SpiceDB and session doubles the use-case tests need and the Store
// contract both implementations keep.
package authenticationtest

import (
	"context"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

// Member is a Member row as the first-admin claim reads it, with the state
// of the Identity linked to it.
type Member struct {
	IdentityID string
	Onboarded  bool
	Deleted    bool
	// Banned is the administrator-facing ban state of the linked Identity:
	// banned in admin metadata, or deactivated.
	Banned bool
}

type firstAdmin struct {
	identityID string
	memberID   string
}

// Store is the in-memory authentication Store. It knows no schema and takes
// no lock; mutationtest serializes its transactions like one connection.
type Store struct {
	mu         sync.Mutex
	members    map[string]Member
	identities map[string]bool
	firstAdmin *firstAdmin
	issuances  map[string]authentication.Issuance
}

// NewStore returns an empty Store.
func NewStore() *Store {
	return &Store{members: map[string]Member{}, identities: map[string]bool{}, issuances: map[string]authentication.Issuance{}}
}

// New returns an empty Store and its unit of work.
func New() (*Store, *mutationtest.UnitOfWork[authentication.Store]) {
	store := NewStore()
	return store, mutationtest.New[authentication.Store](store)
}

// SeedMember stores the Member memberID linked to member.IdentityID, and
// that account Identity.
func (s *Store) SeedMember(memberID string, member Member) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.members[memberID] = member
	s.identities[member.IdentityID] = true
}

// SeedIdentity stores an account Identity without a Member.
func (s *Store) SeedIdentity(identityID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.identities[identityID] = true
}

// FirstAdmin returns the Identity and Member holding the first-admin claim,
// and whether there is a claim.
func (s *Store) FirstAdmin() (identityID, memberID string, claimed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.firstAdmin == nil {
		return "", "", false
	}
	return s.firstAdmin.identityID, s.firstAdmin.memberID, true
}

// Issuances returns every recorded issuance, oldest first.
func (s *Store) Issuances() []authentication.Issuance {
	s.mu.Lock()
	defer s.mu.Unlock()
	issuances := slices.Collect(maps.Values(s.issuances))
	slices.SortFunc(issuances, func(a, b authentication.Issuance) int { return a.IssuedAt.Compare(b.IssuedAt) })
	return issuances
}

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() mutationtest.Checkpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	members := mutationtest.ClonePersisted(s.members)
	identities := mutationtest.ClonePersisted(s.identities)
	firstAdmin := mutationtest.ClonePersisted(s.firstAdmin)
	issuances := mutationtest.ClonePersisted(s.issuances)
	return mutationtest.Checkpoint{
		State: map[string]any{
			"members":    members,
			"identities": identities,
			"firstAdmin": firstAdmin,
			"issuances":  issuances,
		},
		Restore: func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.members = members
			s.identities = identities
			s.firstAdmin = firstAdmin
			s.issuances = issuances
		},
	}
}

// ClaimFirstAdmin implements authentication.Store.
func (s *Store) ClaimFirstAdmin(_ context.Context, identityID, memberID string) (authentication.FirstAdminClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	member, exists := s.members[memberID]
	eligible := exists && member.IdentityID == identityID && member.Onboarded && !member.Deleted && !member.Banned
	greenfield := true
	for id := range s.identities {
		if id != identityID {
			greenfield = false
		}
	}
	claimedNow := false
	if eligible && greenfield && s.firstAdmin == nil {
		s.firstAdmin = &firstAdmin{identityID: identityID, memberID: memberID}
		claimedNow = true
	}
	owner := s.firstAdmin != nil && s.firstAdmin.identityID == identityID
	return authentication.FirstAdminClaim{Owner: owner, ClaimedNow: claimedNow}, nil
}

// LockIssuances implements authentication.Store; the fake's transactions
// are already serial.
func (s *Store) LockIssuances(context.Context) error {
	return nil
}

// IssuanceUsage implements authentication.Store.
func (s *Store) IssuanceUsage(_ context.Context, query authentication.IssuanceUsageQuery) (authentication.IssuanceUsage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var usage authentication.IssuanceUsage
	for _, issuance := range s.issuances {
		if !issuance.IssuedAt.After(query.After) {
			continue
		}
		switch query.Scope {
		case authentication.ClientIPIssuances:
			if issuance.ClientIPDigest != query.Digest {
				continue
			}
		case authentication.SubjectIssuances:
			if issuance.SubjectDigest != query.Digest {
				continue
			}
		}
		usage.Count++
		if usage.First.IsZero() || issuance.IssuedAt.Before(usage.First) {
			usage.First = issuance.IssuedAt
		}
	}
	return usage, nil
}

// InsertIssuance implements authentication.Store.
func (s *Store) InsertIssuance(_ context.Context, issuance authentication.Issuance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.issuances[issuance.Token]; exists {
		return errDuplicateIssuance
	}
	issuance.IssuedAt, issuance.ExpiresAt = issuance.IssuedAt.UTC(), issuance.ExpiresAt.UTC()
	s.issuances[issuance.Token] = issuance
	return nil
}

// DeleteIssuance implements authentication.Store.
func (s *Store) DeleteIssuance(_ context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.issuances, token)
	return nil
}

// LatestIssuance implements authentication.Store.
func (s *Store) LatestIssuance(_ context.Context, recipientDigest string, now time.Time) (authentication.IssuedCode, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest authentication.Issuance
	found := false
	for _, issuance := range s.issuances {
		if issuance.RecipientDigest != recipientDigest || !issuance.ExpiresAt.After(now) {
			continue
		}
		if !found || issuance.IssuedAt.After(latest.IssuedAt) {
			latest, found = issuance, true
		}
	}
	if !found {
		return authentication.IssuedCode{}, false, nil
	}
	return authentication.IssuedCode{
		Token: latest.Token, Purpose: latest.Purpose, RecipientDigest: latest.RecipientDigest, IssuedAt: latest.IssuedAt,
	}, true, nil
}

var _ authentication.Store = (*Store)(nil)
