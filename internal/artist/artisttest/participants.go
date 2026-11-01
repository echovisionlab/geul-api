package artisttest

import (
	"context"
	"sort"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
)

// LockEntity implements entityparticipant.Store.
func (s *Store) LockEntity(_ context.Context, entityID string) error {
	if err := s.fail("LockEntity"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.artists[entityID]; !ok {
		return entityparticipant.ErrEntityNotFound
	}
	return nil
}

// ParticipantRole implements entityparticipant.Store.
func (s *Store) ParticipantRole(_ context.Context, entityID, memberID string) (string, error) {
	if err := s.fail("ParticipantRole"); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, role := range []string{entityparticipant.RoleOwner, entityparticipant.RoleManager} {
		if _, ok := s.participants[participantKey{entityID, memberID, role}]; ok {
			return role, nil
		}
	}
	return entityparticipant.RoleNone, nil
}

// CountOwners implements entityparticipant.Store.
func (s *Store) CountOwners(_ context.Context, entityID, memberID string) (int64, error) {
	if err := s.fail("CountOwners"); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for key := range s.participants {
		if key.entityID == entityID && key.role == entityparticipant.RoleOwner && (memberID == "" || key.memberID == memberID) {
			count++
		}
	}
	return count, nil
}

// InsertParticipant implements entityparticipant.Store; an unknown Member
// fails like its foreign key.
func (s *Store) InsertParticipant(_ context.Context, entityID, memberID, role string, createdAt time.Time) error {
	if err := s.fail("InsertParticipant"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.participants[participantKey{entityID, memberID, role}] = createdAt
	return nil
}

// DeleteParticipant implements entityparticipant.Store.
func (s *Store) DeleteParticipant(_ context.Context, entityID, memberID, role string) (bool, error) {
	if err := s.fail("DeleteParticipant"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := participantKey{entityID, memberID, role}
	_, ok := s.participants[key]
	delete(s.participants, key)
	return ok, nil
}

// LockMemberIdentity implements entityparticipant.Store with the answer of
// authorizationtarget.RequireLocked for a Member without a linked Identity.
func (s *Store) LockMemberIdentity(_ context.Context, memberID string) (string, error) {
	if err := s.fail("LockMemberIdentity"); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	identityID, ok := s.identities[memberID]
	if !ok {
		return "", domainerr.ResourceNotFound("member", memberID)
	}
	return identityID, nil
}

// ListParticipants implements entityparticipant.Store.
func (s *Store) ListParticipants(_ context.Context, entityID string) ([]entityparticipant.Participant, error) {
	if err := s.fail("ListParticipants"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var participants []entityparticipant.Participant
	for key, createdAt := range s.participants {
		if key.entityID == entityID {
			participants = append(participants, entityparticipant.Participant{MemberID: key.memberID, Role: key.role, CreatedAt: createdAt})
		}
	}
	sort.Slice(participants, func(i, j int) bool {
		left, right := participants[i], participants[j]
		if left.Role != right.Role {
			return left.Role == entityparticipant.RoleOwner
		}
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.Before(right.CreatedAt)
		}
		return left.MemberID < right.MemberID
	})
	return participants, nil
}

// LockParentGraph implements entityparticipant.Store.
func (s *Store) LockParentGraph(context.Context) error {
	if err := s.fail("LockParentGraph"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.parentLocks++
	return nil
}

// EntityExists implements entityparticipant.Store.
func (s *Store) EntityExists(_ context.Context, entityID string) (bool, error) {
	if err := s.fail("EntityExists"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.artists[entityID]
	return ok, nil
}

// IsDescendant implements entityparticipant.Store.
func (s *Store) IsDescendant(_ context.Context, entityID, candidateID string) (bool, error) {
	if err := s.fail("IsDescendant"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	frontier := []string{entityID}
	for len(frontier) > 0 {
		parent := frontier[0]
		frontier = frontier[1:]
		for id, value := range s.artists {
			if value.ParentArtistID != nil && *value.ParentArtistID == parent && !seen[id] {
				if id == candidateID {
					return true, nil
				}
				seen[id] = true
				frontier = append(frontier, id)
			}
		}
	}
	return false, nil
}

// EntityParent implements entityparticipant.Store.
func (s *Store) EntityParent(_ context.Context, entityID string) (*string, error) {
	if err := s.fail("EntityParent"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.artists[entityID]
	if !ok {
		return nil, entityparticipant.ErrEntityNotFound
	}
	return value.ParentArtistID, nil
}
