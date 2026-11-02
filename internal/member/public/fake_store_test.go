package public_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/member/public"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

// fakeStore is the in-memory public.Store of the use-case tests; the SQL is
// covered by the PostgreSQL tests of internal/member/public/postgres.
type fakeStore struct {
	mu         sync.Mutex
	members    map[string]member.Member
	avatars    map[string]*memberref.Asset
	authors    []public.AuthorRow
	newsletter map[string]time.Time
}

func newFakeStore() (*fakeStore, *mutationtest.UnitOfWork[public.Store]) {
	store := &fakeStore{members: map[string]member.Member{}, avatars: map[string]*memberref.Asset{}, newsletter: map[string]time.Time{}}
	return store, mutationtest.New[public.Store](store)
}

func (s *fakeStore) Savepoint() func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	members, newsletter := maps.Clone(s.members), maps.Clone(s.newsletter)
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.members, s.newsletter = members, newsletter
	}
}

func (s *fakeStore) PublicMember(_ context.Context, memberID string) (member.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, found := s.members[memberID]
	if !found || !value.Onboarded {
		return member.Member{}, member.ErrMemberNotFound
	}
	return value, nil
}

func (s *fakeStore) PublicMembers(_ context.Context, memberIDs []string) ([]member.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []member.Member
	for _, memberID := range memberIDs {
		if value, found := s.members[memberID]; found && value.Onboarded {
			result = append(result, value)
		}
	}
	return result, nil
}

func (s *fakeStore) PublicAvatars(_ context.Context, memberIDs []string) (map[string]*memberref.Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := map[string]*memberref.Asset{}
	for _, memberID := range memberIDs {
		if avatar, found := s.avatars[memberID]; found {
			result[memberID] = avatar
		}
	}
	return result, nil
}

func (s *fakeStore) TopAuthors(_ context.Context, limit int32) ([]public.AuthorRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := slices.Clone(s.authors)
	if len(rows) > int(limit) {
		rows = rows[:limit]
	}
	return rows, nil
}

func (s *fakeStore) SelectedAuthors(_ context.Context, memberIDs []string) ([]public.AuthorRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []public.AuthorRow
	for _, row := range s.authors {
		value := s.members[row.MemberID]
		if slices.Contains(memberIDs, row.MemberID) && value.Onboarded && value.DeletedAt == nil && value.AccountIdentityID != nil {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (s *fakeStore) NewsletterMemberID(_ context.Context, identityID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, value := range s.members {
		if value.AccountIdentityID != nil && *value.AccountIdentityID == identityID && value.DeletedAt == nil {
			return value.ID, nil
		}
	}
	return "", member.ErrMemberNotFound
}

func (s *fakeStore) SubscribeNewsletter(_ context.Context, identityID string, subscribedAt time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, found := s.newsletter[identityID]; found {
		return false, nil
	}
	s.newsletter[identityID] = subscribedAt
	return true, nil
}

func (s *fakeStore) UnsubscribeNewsletter(_ context.Context, identityID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, found := s.newsletter[identityID]
	delete(s.newsletter, identityID)
	return found, nil
}

func (s *fakeStore) NewsletterSubscription(_ context.Context, identityID string) (*time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, found := s.newsletter[identityID]
	if !found {
		return nil, nil
	}
	return &at, nil
}

var errAuditDown = errors.New("audit unavailable")
