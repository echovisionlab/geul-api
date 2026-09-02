package member

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/securityaccess"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

func (s *MemberService) SearchMembers(
	ctx context.Context,
	req *connect.Request[managev1.SearchMembersRequest],
) (*connect.Response[managev1.SearchMembersResponse], error) {
	if _, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	limit := int(req.Msg.Limit)
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	query := s.db.WithContext(ctx).Where("deleted_at IS NULL AND account_identity_id IS NOT NULL AND onboarded = TRUE")
	if req.Msg.EffectiveAuthorsOnly {
		subjects, err := s.spicedb.LookupGlobalSubjects(ctx, policyv1.Platform.LookupAuthorSubjects())
		if err != nil {
			return nil, errs.Internal(fmt.Errorf("lookup author candidates: %w", err))
		}
		identityIDs := make([]string, 0, len(subjects))
		for _, subject := range subjects {
			identityIDs = append(identityIDs, subject.ID.String())
		}
		if len(identityIDs) == 0 {
			return connect.NewResponse(&managev1.SearchMembersResponse{}), nil
		}
		query = query.Where("account_identity_id IN ?", identityIDs)
	}
	if value := strings.TrimSpace(req.Msg.Query); value != "" {
		query = query.Where("nickname ILIKE ?", "%"+value+"%")
	}
	if len(req.Msg.ExcludeMemberIds) != 0 {
		query = query.Where("id NOT IN ?", req.Msg.ExcludeMemberIds)
	}
	var members []model.Member
	if err := query.Order("nickname ASC, id ASC").Limit(limit).Find(&members).Error; err != nil {
		return nil, errs.Internal(err)
	}
	ids := make([]string, len(members))
	for i := range members {
		ids[i] = members[i].ID
	}
	assets, err := s.loadAvatarAssets(ctx, ids)
	if err != nil {
		return nil, errs.Internal(err)
	}
	result := make([]*commonv1.MemberSummary, len(members))
	for i := range members {
		result[i] = memberSummary(members[i], assets[members[i].ID])
	}
	return connect.NewResponse(&managev1.SearchMembersResponse{Members: result}), nil
}

func (s *MemberService) GetMember(
	ctx context.Context,
	req *connect.Request[managev1.GetMemberRequest],
) (*connect.Response[managev1.AdminMember], error) {
	if _, err := s.requireAdminMember(ctx); err != nil {
		return nil, err
	}
	member, profile, err := s.memberProfileByID(ctx, req.Msg.MemberId)
	if err != nil {
		return nil, err
	}
	account, err := s.accountSummary(ctx, member.ID)
	if err != nil && !memberIsTombstone(*member) {
		return nil, err
	}
	tags, err := s.getMemberTagIDs(ctx, member.ID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	details, err := s.accountAdminDetails(ctx, *member)
	if err != nil {
		return nil, err
	}
	subscriptions, err := s.newsletterSubscriptions(ctx, []string{member.ID})
	if err != nil {
		return nil, errs.Internal(err)
	}
	result := &managev1.AdminMember{Member: profile, Account: account, TagIds: tags, AccountDetails: details, NewsletterSubscription: &managev1.NewsletterSubscriptionState{}, Onboarded: member.Onboarded}
	if subscribedAt, ok := subscriptions[member.ID]; ok {
		result.NewsletterSubscription = &managev1.NewsletterSubscriptionState{Subscribed: true, SubscribedAt: timestamppb.New(subscribedAt)}
	}
	if s.personalDataAccess != nil {
		if err := s.personalDataAccess.AppendMember(ctx, member.ID); err != nil {
			return nil, securityaccess.Unavailable()
		}
	}
	return connect.NewResponse(result), nil
}

func (s *MemberService) accountAdminDetails(ctx context.Context, member model.Member) (*managev1.AccountAdminDetails, error) {
	if member.DeletedAt != nil || member.AccountIdentityID == nil {
		return nil, nil
	}
	identity, err := s.kratosIdentityWithOIDCCredentials(ctx, *member.AccountIdentityID)
	if err != nil {
		return nil, err
	}
	if identity.ID != *member.AccountIdentityID || identity.ExternalID != member.ID {
		return nil, errs.InvalidSession()
	}
	return s.accountAdminProjection(ctx, identity)
}

func (s *MemberService) kratosIdentityWithOIDCCredentials(ctx context.Context, identityID string) (*auth.Identity, error) {
	if s.identity == nil {
		return nil, errs.InternalMsg("member service identity manager is required")
	}
	identity, err := loadMemberIdentityWithEmailCredentials(ctx, s.identity, identityID)
	if err != nil || identity == nil {
		return nil, errs.Internal(fmt.Errorf("read member account details: %w", err))
	}
	return identity, nil
}

func (s *MemberService) newsletterSubscriptions(ctx context.Context, memberIDs []string) (map[string]time.Time, error) {
	result := make(map[string]time.Time, len(memberIDs))
	if len(memberIDs) == 0 {
		return result, nil
	}
	type subscriptionRow struct {
		MemberID     string    `gorm:"column:member_id"`
		SubscribedAt time.Time `gorm:"column:subscribed_at"`
	}
	var rows []subscriptionRow
	if err := s.db.WithContext(ctx).Raw(`
		SELECT m.id::text AS member_id, ns.subscribed_at
		FROM member AS m
		JOIN kratos.identities AS i
		  ON i.id = m.account_identity_id
		 AND i.external_id = m.id::text
		JOIN newsletter_subscription AS ns
		  ON ns.identity_id = i.id
		WHERE m.id IN ?
	`, memberIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.MemberID] = row.SubscribedAt
	}
	return result, nil
}

func (s *MemberService) ListMembersAdmin(ctx context.Context, req *connect.Request[managev1.ListMembersAdminRequest]) (*connect.Response[managev1.ListMembersAdminResponse], error) {
	if _, err := s.requireAdminMember(ctx); err != nil {
		return nil, err
	}
	limit, offset := 50, 0
	if p := req.Msg.Pagination; p != nil {
		if p.Limit > 0 {
			limit = int(p.Limit)
		}
		offset = int(p.Offset)
	}
	if limit > 100 {
		limit = 100
	}
	query := s.db.WithContext(ctx).
		Table("member AS m").
		Joins(`LEFT JOIN kratos.identities AS i
			ON i.id = m.account_identity_id
		   AND i.external_id = m.id::text`).
		Joins("LEFT JOIN newsletter_subscription AS ns ON ns.identity_id = i.id")
	var err error
	query, err = memberAdminFilterConfig.ApplyFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}
	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}
	query, err = memberAdminSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}
	if len(req.Msg.Sorts) != 0 {
		query = query.Order("m.id DESC")
	}
	var members []model.Member
	if err := query.Select("m.*").Limit(limit).Offset(offset).Scan(&members).Error; err != nil {
		return nil, errs.Internal(err)
	}
	ids := make([]string, len(members))
	for i := range members {
		ids[i] = members[i].ID
	}
	assets, err := s.loadAvatarAssets(ctx, ids)
	if err != nil {
		return nil, errs.Internal(err)
	}
	accounts, err := s.accountSummaries(ctx, ids)
	if err != nil {
		return nil, err
	}
	tags, err := s.getMemberTagIDsBatch(ctx, ids)
	if err != nil {
		return nil, errs.Internal(err)
	}
	subscriptions, err := s.newsletterSubscriptions(ctx, ids)
	if err != nil {
		return nil, errs.Internal(err)
	}
	result := make([]*managev1.AdminMember, len(members))
	for i, member := range members {
		result[i] = &managev1.AdminMember{Member: memberProfile(member, assets[member.ID]), Account: accounts[member.ID], TagIds: tags[member.ID], NewsletterSubscription: &managev1.NewsletterSubscriptionState{}, Onboarded: member.Onboarded}
		if subscribedAt, ok := subscriptions[member.ID]; ok {
			result[i].NewsletterSubscription = &managev1.NewsletterSubscriptionState{Subscribed: true, SubscribedAt: timestamppb.New(subscribedAt)}
		}
	}
	if s.personalDataAccess != nil {
		if err := s.personalDataAccess.AppendMemberCollection(ctx); err != nil {
			return nil, securityaccess.Unavailable()
		}
	}
	return connect.NewResponse(&managev1.ListMembersAdminResponse{Members: result, Pagination: &commonv1.PaginationResponse{Total: int32(min(total, math.MaxInt32)), Limit: int32(limit), Offset: int32(offset), HasMore: int64(offset+len(members)) < total}}), nil
}

func (s *MemberService) UpdateMemberProfile(ctx context.Context, req *connect.Request[managev1.UpdateMemberProfileRequest]) (*connect.Response[managev1.AdminMember], error) {
	if _, err := s.requireAdminMember(ctx); err != nil {
		return nil, err
	}
	if _, err := s.updateMemberProfileAsAdmin(ctx, req.Msg.MemberId, req.Msg.Nickname, req.Msg.Bio, req.Msg.Website, req.Msg.SocialLinks); err != nil {
		return nil, err
	}
	return s.GetMember(ctx, connect.NewRequest(&managev1.GetMemberRequest{MemberId: req.Msg.MemberId}))
}

func (s *MemberService) SetMemberAvatar(ctx context.Context, req *connect.Request[managev1.SetMemberAvatarRequest]) (*connect.Response[managev1.MemberSummaryMutationResponse], error) {
	if _, err := s.requireAdminMember(ctx); err != nil {
		return nil, err
	}
	summary, err := s.setAvatar(ctx, req.Msg.MemberId, req.Msg.FileId, true)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.MemberSummaryMutationResponse{Member: summary}), nil
}

func (s *MemberService) DeleteMemberAvatar(ctx context.Context, req *connect.Request[managev1.DeleteMemberAvatarRequest]) (*connect.Response[managev1.MemberSummaryMutationResponse], error) {
	if _, err := s.requireAdminMember(ctx); err != nil {
		return nil, err
	}
	summary, err := s.deleteAvatar(ctx, req.Msg.MemberId, true)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.MemberSummaryMutationResponse{Member: summary}), nil
}
