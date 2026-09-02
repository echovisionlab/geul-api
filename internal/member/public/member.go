package public

import (
	"context"
	"fmt"
	"math"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
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

// MemberService owns public Member profile reads. Member stores profile data;
// SpiceDB is the authority for the extended-profile visibility gate.
type MemberService struct {
	openv1connect.UnimplementedMemberServiceHandler
	db        *gorm.DB
	cdnDomain string
	spiceDB   *auth.SpiceDBClient
}

func NewMemberService(db *gorm.DB, cdnDomain string, spiceDB *auth.SpiceDBClient) *MemberService {
	if db == nil || spiceDB == nil {
		panic("public member service dependencies are required")
	}
	return &MemberService{db: db, cdnDomain: cdnDomain, spiceDB: spiceDB}
}

func (s *MemberService) GetPublicMember(
	ctx context.Context,
	req *connect.Request[openv1.GetPublicMemberRequest],
) (*connect.Response[openv1.GetPublicMemberResponse], error) {
	memberID, err := uuidutil.ParseCanonical(req.Msg.GetMemberId(), "member_id")
	if err != nil {
		return nil, errs.InvalidArgument("member_id", "must be a canonical UUID")
	}

	type publicMemberRow struct {
		model.Member
		CanExposeExtendedProfile bool `gorm:"column:can_expose_extended_profile"`
	}
	var row publicMemberRow
	if err := s.db.WithContext(ctx).
		Table("member AS m").
		Select("m.*, FALSE AS can_expose_extended_profile").
		Where("m.id = ? AND m.onboarded = TRUE", memberID.String()).
		Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return connect.NewResponse(&openv1.GetPublicMemberResponse{}), nil
		}
		return nil, errs.Internal(fmt.Errorf("load public member: %w", err))
	}
	member := row.Member
	if member.AccountIdentityID != nil && member.DeletedAt == nil {
		actor, err := policyv1.NewAccountIdentityActor(*member.AccountIdentityID)
		if err != nil {
			return nil, errs.Internal(fmt.Errorf("parse public member account identity: %w", err))
		}
		can, err := policyv1.Platform.IsAuthor()
		if err != nil {
			return nil, errs.Internal(fmt.Errorf("build public member visibility permission: %w", err))
		}
		canExpose, err := s.spiceDB.CheckActorCan(ctx, actor, can)
		if err != nil {
			return nil, errs.Internal(fmt.Errorf("check public member visibility permission: %w", err))
		}
		row.CanExposeExtendedProfile = canExpose
	}
	avatars, err := loadPublicMemberAvatars(ctx, s.db, s.cdnDomain, []string{member.ID})
	if err != nil {
		return nil, errs.Internal(err)
	}
	summary, err := projectPublicMemberSummary(member, avatars[member.ID])
	if err != nil {
		return nil, errs.Internal(err)
	}

	profile := &openv1.PublicMemberProfile{
		Summary:     summary,
		SocialLinks: map[string]string{},
		CreatedAt:   timestamppb.New(member.CreatedAt),
	}
	if row.CanExposeExtendedProfile {
		profile.Bio = member.Bio
		profile.Website = member.Website
		profile.SocialLinks = member.SocialLinks
	}
	return connect.NewResponse(&openv1.GetPublicMemberResponse{Member: profile}), nil
}

// ListAuthors returns top post contributors with Member summaries in a fixed
// three-query budget (counts, Member rows, avatar bindings/assets).
func (s *MemberService) ListAuthors(
	ctx context.Context,
	req *connect.Request[openv1.ListAuthorsRequest],
) (*connect.Response[openv1.ListAuthorsResponse], error) {
	if len(req.Msg.MemberIds) != 0 {
		return s.listSelectedAuthors(ctx, req.Msg.MemberIds)
	}

	limit := req.Msg.GetLimit()
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}

	type authorCount struct {
		MemberID          string  `gorm:"column:member_id"`
		AccountIdentityID *string `gorm:"column:account_identity_id"`
		PostCount         int64   `gorm:"column:post_count"`
		Bio               *string `gorm:"column:bio"`
	}
	authorSubjects, err := s.spiceDB.LookupGlobalSubjects(ctx, policyv1.Platform.LookupAuthorSubjects())
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("lookup public author identities: %w", err))
	}
	authorIdentityIDs := make(map[string]struct{}, len(authorSubjects))
	for _, subject := range authorSubjects {
		authorIdentityIDs[subject.ID.String()] = struct{}{}
	}
	var rows []authorCount
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
		return nil, errs.Internal(fmt.Errorf("list top post contributors: %w", err))
	}

	memberIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		memberIDs = append(memberIDs, row.MemberID)
	}
	summaries, err := LoadPublicMemberSummaries(ctx, s.db, s.cdnDomain, memberIDs)
	if err != nil {
		return nil, errs.Internal(err)
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
	return connect.NewResponse(&openv1.ListAuthorsResponse{Authors: authors}), nil
}

func (s *MemberService) listSelectedAuthors(
	ctx context.Context,
	requestedMemberIDs []string,
) (*connect.Response[openv1.ListAuthorsResponse], error) {
	if len(requestedMemberIDs) > 24 {
		return nil, errs.InvalidArgument("member_ids", "must contain at most 24 unique Member IDs")
	}

	memberIDs := make([]string, 0, len(requestedMemberIDs))
	seen := make(map[string]struct{}, len(requestedMemberIDs))
	for _, value := range requestedMemberIDs {
		memberID, err := uuidutil.ParseCanonical(value, "member_ids")
		if err != nil {
			return nil, errs.InvalidArgument("member_ids", "must contain canonical UUIDs")
		}
		canonical := memberID.String()
		if _, duplicate := seen[canonical]; duplicate {
			return nil, errs.InvalidArgument("member_ids", "must contain unique Member IDs")
		}
		seen[canonical] = struct{}{}
		memberIDs = append(memberIDs, canonical)
	}

	type selectedAuthorRow struct {
		MemberID          string  `gorm:"column:member_id"`
		AccountIdentityID *string `gorm:"column:account_identity_id"`
		PostCount         int64   `gorm:"column:post_count"`
		Bio               *string `gorm:"column:bio"`
	}
	var rows []selectedAuthorRow
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
		return nil, errs.Internal(fmt.Errorf("load selected authors: %w", err))
	}

	authorSubjects, err := s.spiceDB.LookupGlobalSubjects(ctx, policyv1.Platform.LookupAuthorSubjects())
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("lookup selected author identities: %w", err))
	}
	authorIdentityIDs := make(map[string]struct{}, len(authorSubjects))
	for _, subject := range authorSubjects {
		authorIdentityIDs[subject.ID.String()] = struct{}{}
	}
	rowsByMemberID := make(map[string]selectedAuthorRow, len(rows))
	eligibleMemberIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if _, eligible := authorIdentityIDs[optionalStringValue(row.AccountIdentityID)]; !eligible {
			continue
		}
		rowsByMemberID[row.MemberID] = row
		eligibleMemberIDs = append(eligibleMemberIDs, row.MemberID)
	}

	summaries, err := LoadPublicMemberSummaries(ctx, s.db, s.cdnDomain, eligibleMemberIDs)
	if err != nil {
		return nil, errs.Internal(err)
	}
	authors := make([]*openv1.PublicAuthorSummary, 0, len(eligibleMemberIDs))
	for _, memberID := range memberIDs {
		row, eligible := rowsByMemberID[memberID]
		if !eligible {
			continue
		}
		summary := summaries[memberID]
		if summary == nil || summary.Deleted {
			continue
		}
		authors = append(authors, &openv1.PublicAuthorSummary{
			Member:    summary,
			PostCount: safeInt32(row.PostCount),
			Bio:       row.Bio,
		})
	}
	return connect.NewResponse(&openv1.ListAuthorsResponse{Authors: authors}), nil
}
