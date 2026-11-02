package public

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/echovisionlab/geul-api/internal/uuidutil"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func safeInt32(n int64) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	if n < math.MinInt32 {
		return math.MinInt32
	}
	return int32(n)
}

// Profile is the public profile of an onboarded Member.
type Profile struct {
	Summary     memberref.Summary
	Bio         *string
	Website     *string
	SocialLinks map[string]string
	CreatedAt   time.Time
}

// AuthorSummary is one post author and how many posts it published.
type AuthorSummary struct {
	Member    memberref.Summary
	PostCount int32
	Bio       *string
}

// GetPublicMemberQuery reads one onboarded Member's public profile.
type GetPublicMemberQuery struct{ MemberID string }

// ListAuthorsQuery lists post authors: the top contributors up to Limit, or
// the effective authors among MemberIDs in their order when it is set.
type ListAuthorsQuery struct {
	Limit     int32
	MemberIDs []string
}

// GetPublicMember returns the public profile of an onboarded Member, or nil
// when there is none. Bio, website and social links show only while the
// Member's Identity holds the author role.
func (s *Service) GetPublicMember(ctx context.Context, query GetPublicMemberQuery) (*Profile, error) {
	memberID, err := uuidutil.ParseCanonical(query.MemberID, "member_id")
	if err != nil {
		return nil, domainerr.InvalidField("member_id", "must be a canonical UUID")
	}
	var found member.Member
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		found, err = store.PublicMember(ctx, memberID.String())
		return err
	}); err != nil {
		if errors.Is(err, member.ErrMemberNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("load public member: %w", err)
	}
	canExposeExtendedProfile, err := s.canExposeExtendedProfile(ctx, found)
	if err != nil {
		return nil, err
	}
	var avatars map[string]*memberref.Asset
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		avatars, err = store.PublicAvatars(ctx, []string{found.ID})
		return err
	}); err != nil {
		return nil, err
	}
	profile := &Profile{
		Summary:     projectPublicMemberSummary(found, avatars[found.ID]),
		SocialLinks: map[string]string{},
		CreatedAt:   found.CreatedAt,
	}
	if canExposeExtendedProfile {
		profile.Bio = found.Bio
		profile.Website = found.Website
		profile.SocialLinks = found.SocialLinks
	}
	return profile, nil
}

// canExposeExtendedProfile reports whether a live Member's Identity is an
// author, read from SpiceDB.
func (s *Service) canExposeExtendedProfile(ctx context.Context, value member.Member) (bool, error) {
	if value.AccountIdentityID == nil || value.DeletedAt != nil {
		return false, nil
	}
	actor, err := policyv1.NewAccountIdentityActor(*value.AccountIdentityID)
	if err != nil {
		return false, fmt.Errorf("parse public member account identity: %w", err)
	}
	can, err := policyv1.Platform.IsAuthor()
	if err != nil {
		return false, fmt.Errorf("build public member visibility permission: %w", err)
	}
	canExpose, err := s.spiceDB.CheckActorCan(ctx, actor, can)
	if err != nil {
		return false, fmt.Errorf("check public member visibility permission: %w", err)
	}
	return canExpose, nil
}

// ListAuthors returns post authors with Member summaries in a fixed
// three-read budget (counts, Member rows, avatar bindings/assets).
func (s *Service) ListAuthors(ctx context.Context, query ListAuthorsQuery) ([]AuthorSummary, error) {
	if len(query.MemberIDs) != 0 {
		return s.listSelectedAuthors(ctx, query.MemberIDs)
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	authorIdentityIDs, err := s.authorIdentityIDs(ctx, "public")
	if err != nil {
		return nil, err
	}
	var (
		rows      []AuthorRow
		summaries map[string]memberref.Summary
	)
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		if rows, err = store.TopAuthors(ctx, limit); err != nil {
			return fmt.Errorf("list top post contributors: %w", err)
		}
		memberIDs := make([]string, 0, len(rows))
		for _, row := range rows {
			memberIDs = append(memberIDs, row.MemberID)
		}
		summaries, err = LoadSummaries(ctx, store, memberIDs)
		return err
	}); err != nil {
		return nil, err
	}
	authors := make([]AuthorSummary, 0, len(rows))
	for _, row := range rows {
		summary, found := summaries[row.MemberID]
		if !found {
			continue
		}
		author := AuthorSummary{Member: summary, PostCount: safeInt32(row.PostCount)}
		_, isAuthor := authorIdentityIDs[optionalStringValue(row.AccountIdentityID)]
		if !summary.Deleted && isAuthor {
			author.Bio = row.Bio
		}
		authors = append(authors, author)
	}
	return authors, nil
}

// listSelectedAuthors returns the effective authors among requested, in
// request order.
func (s *Service) listSelectedAuthors(ctx context.Context, requested []string) ([]AuthorSummary, error) {
	memberIDs, err := selectedAuthorIDs(requested)
	if err != nil {
		return nil, err
	}
	var rows []AuthorRow
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		rows, err = store.SelectedAuthors(ctx, memberIDs)
		return err
	}); err != nil {
		return nil, fmt.Errorf("load selected authors: %w", err)
	}
	authorIdentityIDs, err := s.authorIdentityIDs(ctx, "selected")
	if err != nil {
		return nil, err
	}
	rowsByMemberID := make(map[string]AuthorRow, len(rows))
	eligibleMemberIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if _, eligible := authorIdentityIDs[optionalStringValue(row.AccountIdentityID)]; eligible {
			rowsByMemberID[row.MemberID] = row
			eligibleMemberIDs = append(eligibleMemberIDs, row.MemberID)
		}
	}
	var summaries map[string]memberref.Summary
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		summaries, err = LoadSummaries(ctx, store, eligibleMemberIDs)
		return err
	}); err != nil {
		return nil, err
	}
	authors := make([]AuthorSummary, 0, len(eligibleMemberIDs))
	for _, memberID := range memberIDs {
		row, eligible := rowsByMemberID[memberID]
		summary, found := summaries[memberID]
		if !eligible || !found || summary.Deleted {
			continue
		}
		authors = append(authors, AuthorSummary{Member: summary, PostCount: safeInt32(row.PostCount), Bio: row.Bio})
	}
	return authors, nil
}

// selectedAuthorIDs canonicalizes at most 24 unique requested Member IDs.
func selectedAuthorIDs(requested []string) ([]string, error) {
	if len(requested) > 24 {
		return nil, domainerr.InvalidField("member_ids", "must contain at most 24 unique Member IDs")
	}
	memberIDs := make([]string, 0, len(requested))
	seen := make(map[string]struct{}, len(requested))
	for _, value := range requested {
		memberID, err := uuidutil.ParseCanonical(value, "member_ids")
		if err != nil {
			return nil, domainerr.InvalidField("member_ids", "must contain canonical UUIDs")
		}
		canonical := memberID.String()
		if _, duplicate := seen[canonical]; duplicate {
			return nil, domainerr.InvalidField("member_ids", "must contain unique Member IDs")
		}
		seen[canonical] = struct{}{}
		memberIDs = append(memberIDs, canonical)
	}
	return memberIDs, nil
}

// authorIdentityIDs is the set of Identities that hold the author role;
// purpose names the list in a lookup failure.
func (s *Service) authorIdentityIDs(ctx context.Context, purpose string) (map[string]struct{}, error) {
	subjects, err := s.spiceDB.LookupGlobalSubjects(ctx, policyv1.Platform.LookupAuthorSubjects())
	if err != nil {
		return nil, fmt.Errorf("lookup %s author identities: %w", purpose, err)
	}
	identityIDs := make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		identityIDs[subject.ID.String()] = struct{}{}
	}
	return identityIDs, nil
}
