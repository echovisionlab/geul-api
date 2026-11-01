package public

import (
	"context"
	"errors"
	"fmt"
	"math"

	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
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
func (s *Service) GetPublicMember(ctx context.Context, query GetPublicMemberQuery) (*openv1.PublicMemberProfile, error) {
	memberID, err := uuidutil.ParseCanonical(query.MemberID, "member_id")
	if err != nil {
		return nil, domainerr.InvalidField("member_id", "must be a canonical UUID")
	}
	var member model.Member
	if err := s.db.WithContext(ctx).
		Table("member AS m").
		Select("m.*").
		Where("m.id = ? AND m.onboarded = TRUE", memberID.String()).
		Take(&member).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("load public member: %w", err)
	}
	canExposeExtendedProfile, err := s.canExposeExtendedProfile(ctx, member)
	if err != nil {
		return nil, err
	}
	avatars, err := loadPublicMemberAvatars(ctx, s.db, s.cdnDomain, []string{member.ID})
	if err != nil {
		return nil, err
	}
	profile := &openv1.PublicMemberProfile{
		Summary:     projectPublicMemberSummary(member, avatars[member.ID]),
		SocialLinks: map[string]string{},
		CreatedAt:   timestamppb.New(member.CreatedAt),
	}
	if canExposeExtendedProfile {
		profile.Bio = member.Bio
		profile.Website = member.Website
		profile.SocialLinks = member.SocialLinks
	}
	return profile, nil
}

// canExposeExtendedProfile reports whether a live Member's Identity is an
// author, read from SpiceDB.
func (s *Service) canExposeExtendedProfile(ctx context.Context, member model.Member) (bool, error) {
	if member.AccountIdentityID == nil || member.DeletedAt != nil {
		return false, nil
	}
	actor, err := policyv1.NewAccountIdentityActor(*member.AccountIdentityID)
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

// authorRow is one Member's published or archived post count.
type authorRow struct {
	MemberID          string  `gorm:"column:member_id"`
	AccountIdentityID *string `gorm:"column:account_identity_id"`
	PostCount         int64   `gorm:"column:post_count"`
	Bio               *string `gorm:"column:bio"`
}

// ListAuthors returns post authors with Member summaries in a fixed
// three-query budget (counts, Member rows, avatar bindings/assets).
func (s *Service) ListAuthors(ctx context.Context, query ListAuthorsQuery) ([]*openv1.PublicAuthorSummary, error) {
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
	var rows []authorRow
	if err := s.db.WithContext(ctx).Raw(`
		SELECT
			pa.member_id,
			COUNT(*) AS post_count,
			m.account_identity_id::text AS account_identity_id,
			m.bio
		FROM post_author AS pa
		JOIN post AS p ON p.id = pa.post_id
		JOIN member AS m ON m.id = pa.member_id
		WHERE p.status IN (?, ?)
		  AND m.onboarded = TRUE
		GROUP BY pa.member_id, m.account_identity_id, m.bio
		ORDER BY post_count DESC, pa.member_id ASC
		LIMIT ?
	`, "POST_STATUS_PUBLISHED", "POST_STATUS_ARCHIVED", limit).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list top post contributors: %w", err)
	}
	memberIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		memberIDs = append(memberIDs, row.MemberID)
	}
	summaries, err := LoadPublicMemberSummaries(ctx, s.db, s.cdnDomain, memberIDs)
	if err != nil {
		return nil, err
	}
	authors := make([]*openv1.PublicAuthorSummary, 0, len(rows))
	for _, row := range rows {
		summary := summaries[row.MemberID]
		if summary == nil {
			continue
		}
		author := &openv1.PublicAuthorSummary{Member: summary, PostCount: safeInt32(row.PostCount)}
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
func (s *Service) listSelectedAuthors(ctx context.Context, requested []string) ([]*openv1.PublicAuthorSummary, error) {
	memberIDs, err := selectedAuthorIDs(requested)
	if err != nil {
		return nil, err
	}
	var rows []authorRow
	if err := s.db.WithContext(ctx).Raw(`
		SELECT
			m.id::text AS member_id,
			m.account_identity_id::text AS account_identity_id,
			COUNT(pa.post_id) FILTER (WHERE p.status IN (?, ?)) AS post_count,
			m.bio
		FROM member AS m
		LEFT JOIN post_author AS pa ON pa.member_id = m.id
		LEFT JOIN post AS p ON p.id = pa.post_id
		WHERE m.id IN ?
		  AND m.onboarded = TRUE
		  AND m.deleted_at IS NULL
		  AND m.account_identity_id IS NOT NULL
		GROUP BY m.id, m.account_identity_id, m.bio
	`, "POST_STATUS_PUBLISHED", "POST_STATUS_ARCHIVED", memberIDs).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load selected authors: %w", err)
	}
	authorIdentityIDs, err := s.authorIdentityIDs(ctx, "selected")
	if err != nil {
		return nil, err
	}
	rowsByMemberID := make(map[string]authorRow, len(rows))
	eligibleMemberIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if _, eligible := authorIdentityIDs[optionalStringValue(row.AccountIdentityID)]; eligible {
			rowsByMemberID[row.MemberID] = row
			eligibleMemberIDs = append(eligibleMemberIDs, row.MemberID)
		}
	}
	summaries, err := LoadPublicMemberSummaries(ctx, s.db, s.cdnDomain, eligibleMemberIDs)
	if err != nil {
		return nil, err
	}
	authors := make([]*openv1.PublicAuthorSummary, 0, len(eligibleMemberIDs))
	for _, memberID := range memberIDs {
		row, eligible := rowsByMemberID[memberID]
		summary := summaries[memberID]
		if !eligible || summary == nil || summary.Deleted {
			continue
		}
		authors = append(authors, &openv1.PublicAuthorSummary{Member: summary, PostCount: safeInt32(row.PostCount), Bio: row.Bio})
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
