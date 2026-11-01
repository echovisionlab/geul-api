package accounttest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/account"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
)

// The Member steps of an account deletion keep member.DeletionLifecycle's
// answers over the fake's Member rows.

func validatePair(memberID, identityID string) error {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return err
	}
	if _, err := uuidutil.ParseCanonical(identityID, "identity_id"); err != nil {
		return err
	}
	if memberID == identityID {
		return errors.New("member_id and identity_id must be distinct")
	}
	return nil
}

func sameEmail(left, right string) bool {
	return emailutil.NormalizeAddressForDelivery(left) == emailutil.NormalizeAddressForDelivery(right)
}

// witness reports whether the pair links both ways and whether the Identity
// exists.
func (st state) witness(memberID, identityID string) (bool, bool) {
	identity, exists := st.identities[identityID]
	member, found := st.members[memberID]
	return found && exists && member.IdentityID == identityID && identity.ExternalID == memberID, exists
}

func (s *Store) PrepareMemberTombstone(_ context.Context, memberID, identityID, primaryEmail string) (account.MemberDeletionSnapshot, error) {
	result := account.MemberDeletionSnapshot{MemberID: memberID, IdentityID: identityID, PrimaryEmailSnapshot: emailutil.NormalizeAddressForDelivery(primaryEmail)}
	if err := validatePair(memberID, identityID); err != nil {
		return result, err
	}
	if result.PrimaryEmailSnapshot == "" {
		return result, errors.New("user deletion identity command requires Member primary email snapshot")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	member, found := s.state.members[memberID]
	if !found {
		return result, fmt.Errorf("load Member email projection before identity deletion: %w", account.ErrMemberLinkNotFound)
	}
	result.AlreadyTombstoned, result.IdentityLinked = member.Deleted, member.IdentityID != ""
	switch {
	case result.AlreadyTombstoned && result.IdentityLinked:
		return result, errors.New("deleted Member retains an identity reverse pointer")
	case result.AlreadyTombstoned:
		return result, nil
	case result.IdentityLinked && member.IdentityID != identityID:
		return result, errors.New("member reverse pointer does not match deletion identity")
	}
	if result.IdentityLinked {
		if linked, exists := s.state.witness(memberID, identityID); exists && !linked {
			return result, errors.New("member and identity deletion link is not bilateral")
		}
	}
	if !sameEmail(member.PrimaryEmail, result.PrimaryEmailSnapshot) || !slices.Contains(member.AvailableEmails, result.PrimaryEmailSnapshot) {
		return result, errors.New("deletion email snapshot does not match the Member email projection")
	}
	return result, nil
}

func (s *Store) FinalizeMemberTombstone(ctx context.Context, snapshot account.MemberDeletionSnapshot, _ time.Time, audit account.MemberDeletionAudit) error {
	if err := s.enter("FinalizeMemberTombstone"); err != nil {
		return err
	}
	s.mu.Lock()
	if _, exists := s.state.identities[snapshot.IdentityID]; exists {
		s.mu.Unlock()
		return errors.New("exact Kratos identity still exists after deletion")
	}
	member, found := s.state.members[snapshot.MemberID]
	if !found {
		s.mu.Unlock()
		return account.ErrMemberLinkNotFound
	}
	if member.IdentityID != "" {
		s.mu.Unlock()
		return errors.New("member reverse pointer was not cleared by exact identity deletion")
	}
	if !member.Deleted {
		member.Deleted, member.Locale = true, nil
		s.state.members[snapshot.MemberID] = member
		s.state.finalized = append(s.state.finalized, snapshot.MemberID)
	}
	s.mu.Unlock()
	if !snapshot.AlreadyTombstoned && (!sameEmail(member.PrimaryEmail, snapshot.PrimaryEmailSnapshot) || !slices.Contains(member.AvailableEmails, snapshot.PrimaryEmailSnapshot)) {
		return errors.New("member email projection changed during identity deletion")
	}
	if !snapshot.AlreadyTombstoned && audit != nil {
		return audit(ctx, snapshot.MemberID)
	}
	return nil
}

func (s *Store) PrepareUnonboardedHardDelete(_ context.Context, memberID, identityID string) (account.MemberUnonboardedDeletionTarget, error) {
	target := account.MemberUnonboardedDeletionTarget{MemberID: memberID, IdentityID: identityID}
	if err := validatePair(memberID, identityID); err != nil {
		return target, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	member, found := s.state.members[memberID]
	if !found || member.Onboarded || member.Deleted {
		return account.MemberUnonboardedDeletionTarget{}, nil
	}
	target.IdentityLinked = member.IdentityID != ""
	if target.IdentityLinked && member.IdentityID != identityID {
		return target, errors.New("unonboarded hard deletion identity does not match Member link")
	}
	if target.IdentityLinked {
		if linked, exists := s.state.witness(memberID, identityID); exists && !linked {
			return target, errors.New("unonboarded Member and identity link is not bilateral")
		}
	}
	return target, nil
}

func (s *Store) FinalizeUnonboardedHardDelete(ctx context.Context, target account.MemberUnonboardedDeletionTarget, audit account.MemberDeletionAudit) error {
	if target.MemberID == "" {
		return nil
	}
	if err := s.enter("FinalizeUnonboardedHardDelete"); err != nil {
		return err
	}
	s.mu.Lock()
	member, found := s.state.members[target.MemberID]
	if !found || member.Onboarded || member.Deleted {
		s.mu.Unlock()
		return nil
	}
	if member.IdentityID != "" {
		s.mu.Unlock()
		if member.IdentityID != target.IdentityID {
			return errors.New("unonboarded hard deletion identity changed during cleanup")
		}
		return errors.New("unonboarded Member identity anchor still exists after cleanup")
	}
	delete(s.state.members, target.MemberID)
	s.state.finalized = append(s.state.finalized, target.MemberID)
	s.mu.Unlock()
	if audit == nil {
		return nil
	}
	return audit(ctx, target.MemberID)
}

func (s *Store) MemberDeletionNotification(_ context.Context, memberID, identityID string) (account.MemberDeletionNotification, error) {
	if err := validatePair(memberID, identityID); err != nil {
		return account.MemberDeletionNotification{}, err
	}
	if err := s.enter("MemberDeletionNotification"); err != nil {
		return account.MemberDeletionNotification{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	member, found := s.state.members[memberID]
	if !found || member.Deleted || member.IdentityID != identityID {
		return account.MemberDeletionNotification{}, account.ErrMemberLinkNotFound
	}
	result := account.MemberDeletionNotification{Nickname: strings.TrimSpace(member.Nickname), PrimaryEmail: emailutil.NormalizeAddressForDelivery(member.PrimaryEmail)}
	if member.Locale != nil {
		if locale := strings.TrimSpace(*member.Locale); locale != "" {
			result.Locale = &locale
		}
	}
	return result, nil
}

func (s *Store) MemberCompletionEligible(_ context.Context, memberID string) (bool, error) {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	member, found := s.state.members[memberID]
	return found && member.Deleted && member.IdentityID == "" && member.Onboarded, nil
}

func (s *Store) MemberAvatarAssetID(_ context.Context, memberID string) (string, error) {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.members[memberID].AvatarAssetID, nil
}

func (st state) unonboardedLinked(memberID string, member Member) bool {
	if member.Onboarded || member.Deleted || member.IdentityID == "" {
		return false
	}
	linked, _ := st.witness(memberID, member.IdentityID)
	return linked
}

func (s *Store) ExpiredUnonboardedMembers(_ context.Context, cutoff time.Time, limit int, after *account.MemberUnonboardedCursor) ([]account.MemberUnonboardedCandidate, *account.MemberUnonboardedCursor, error) {
	if limit <= 0 {
		return []account.MemberUnonboardedCandidate{}, nil, nil
	}
	if err := s.enter("ExpiredUnonboardedMembers"); err != nil {
		return nil, nil, fmt.Errorf("list expired unonboarded Members: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []account.MemberUnonboardedCandidate{}
	for memberID, member := range s.state.members {
		if !s.state.unonboardedLinked(memberID, member) || member.CreatedAt.After(cutoff) {
			continue
		}
		if after != nil && (member.CreatedAt.Before(after.CreatedAt) || (member.CreatedAt.Equal(after.CreatedAt) && memberID <= after.MemberID)) {
			continue
		}
		result = append(result, account.MemberUnonboardedCandidate{MemberID: memberID, IdentityID: member.IdentityID, CreatedAt: member.CreatedAt})
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
	if len(result) == 0 {
		return result, nil, nil
	}
	last := result[len(result)-1]
	return result, &account.MemberUnonboardedCursor{CreatedAt: last.CreatedAt, MemberID: last.MemberID}, nil
}

func (s *Store) RecheckUnonboardedMember(_ context.Context, memberID, identityID string, cutoff *time.Time) (bool, error) {
	if err := validatePair(memberID, identityID); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	member, found := s.state.members[memberID]
	if !found || member.IdentityID != identityID || !s.state.unonboardedLinked(memberID, member) {
		return false, nil
	}
	return cutoff == nil || !member.CreatedAt.After(*cutoff), nil
}

func (s *Store) UnonboardedMemberIdentity(_ context.Context, memberID string) (string, bool, error) {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return "", false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	member, found := s.state.members[memberID]
	if !found || !s.state.unonboardedLinked(memberID, member) {
		return "", false, nil
	}
	return member.IdentityID, true, nil
}
