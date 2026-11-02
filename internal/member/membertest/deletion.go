package membertest

import (
	"context"
	"sort"
	"time"

	"github.com/echovisionlab/geul-api/internal/member"
)

func (s *Store) DeletionMember(ctx context.Context, memberID string) (member.Member, error) {
	return s.MemberByID(ctx, memberID)
}

func (s *Store) DeletionPairWitness(_ context.Context, memberID, identityID string) (bool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	identity, exists := s.state.identities[identityID]
	value, found := s.state.members[memberID]
	linked := found && exists && value.AccountIdentityID != nil && *value.AccountIdentityID == identityID && identity.ExternalID == memberID
	return linked, exists, nil
}

func (s *Store) IdentityExists(_ context.Context, identityID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, exists := s.state.identities[identityID]
	return exists, nil
}

// DeleteIdentity removes a Kratos Identity and clears the reverse pointer of
// its Member, as Kratos' identity deletion does through the schema.
func (s *Store) DeleteIdentity(identityID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.state.identities, identityID)
	for id, value := range s.state.members {
		if value.AccountIdentityID != nil && *value.AccountIdentityID == identityID {
			value.AccountIdentityID = nil
			s.state.members[id] = value
		}
	}
}

func (s *Store) LockMember(ctx context.Context, memberID string) (member.Member, error) {
	return s.MemberByID(ctx, memberID)
}

func (s *Store) DeletePersonalAccessTokens(_ context.Context, memberID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.state.tokens, memberID)
	return nil
}

func (s *Store) TombstoneMember(_ context.Context, memberID string, deletedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, found := s.state.members[memberID]
	if !found || value.AccountIdentityID != nil || value.DeletedAt != nil {
		return nil
	}
	value.Bio, value.Website, value.PreferredLocale = nil, nil, nil
	value.SocialLinks = map[string]string{}
	value.DeletedAt, value.UpdatedAt = &deletedAt, deletedAt
	s.state.members[memberID] = value
	return nil
}

func (s *Store) NotificationMember(_ context.Context, memberID, identityID string) (member.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, err := s.find(memberID, func(value member.Member) bool {
		return value.DeletedAt == nil && value.AccountIdentityID != nil && *value.AccountIdentityID == identityID
	})
	if err != nil {
		return member.Member{}, err
	}
	return member.Member{Nickname: value.Nickname, PrimaryEmail: value.PrimaryEmail, PreferredLocale: value.PreferredLocale}, nil
}

func (s *Store) CompletionEligible(_ context.Context, memberID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, found := s.state.members[memberID]
	return found && value.DeletedAt != nil && value.AccountIdentityID == nil && value.Onboarded, nil
}

func (s *Store) AvatarAssetID(_ context.Context, memberID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.bindings[memberID], nil
}

func (s *Store) ReleaseAvatarBindings(_ context.Context, memberID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.state.bindings, memberID)
	return nil
}

func (s *Store) PublicAssetStatus(_ context.Context, assetID string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status, found := s.state.assetStatus[assetID]
	return status, found, nil
}

func (s *Store) RequestPublicAssetDeletion(_ context.Context, assetID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.assetStatus[assetID] = "delete_pending"
	s.state.deletionRequests = append(s.state.deletionRequests, assetID)
	return nil
}

func (s *Store) DeleteUnonboardedReferences(_ context.Context, memberID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.state.bindings, memberID)
	delete(s.state.mappings, memberID)
	for key, owner := range s.state.bootstrap {
		if owner == memberID {
			if key == "first_admin" {
				s.state.bootstrap[key] = ""
			} else {
				delete(s.state.bootstrap, key)
			}
		}
	}
	return nil
}

func (s *Store) DeleteUnonboardedMember(_ context.Context, memberID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, found := s.state.members[memberID]
	if !found || value.Onboarded || value.DeletedAt != nil || value.AccountIdentityID != nil {
		return false, nil
	}
	delete(s.state.members, memberID)
	return true, nil
}

func (s *Store) ExpiredUnonboarded(_ context.Context, cutoff time.Time, limit int, after *member.UnonboardedCursor) ([]member.UnonboardedCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []member.UnonboardedCandidate
	for _, value := range s.state.members {
		if value.Onboarded || value.DeletedAt != nil || value.CreatedAt.After(cutoff) {
			continue
		}
		if _, linked := s.state.linkedIdentity(value); !linked {
			continue
		}
		if after != nil && (value.CreatedAt.Before(after.CreatedAt) || (value.CreatedAt.Equal(after.CreatedAt) && value.ID <= after.MemberID)) {
			continue
		}
		result = append(result, member.UnonboardedCandidate{MemberID: value.ID, IdentityID: *value.AccountIdentityID, CreatedAt: value.CreatedAt})
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].CreatedAt.Before(result[j].CreatedAt)
		}
		return result[i].MemberID < result[j].MemberID
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *Store) UnonboardedLinked(_ context.Context, memberID, identityID string, cutoff *time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, found := s.linkedPair(memberID, identityID)
	if !found || value.Onboarded {
		return false, nil
	}
	return cutoff == nil || !value.CreatedAt.After(*cutoff), nil
}

func (s *Store) UnonboardedIdentity(_ context.Context, memberID string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, found := s.state.members[memberID]
	if !found || value.Onboarded || value.DeletedAt != nil {
		return "", false, nil
	}
	if _, linked := s.state.linkedIdentity(value); !linked {
		return "", false, nil
	}
	return *value.AccountIdentityID, true, nil
}
