package member

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identityread"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/securityaccess"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

// AdminStatus is the administrator-facing lifecycle state of a Member,
// derived from the Member row, its linked Identity and a pending deletion.
// The values are what administrators filter by and what MCP reports.
type AdminStatus string

const (
	AdminStatusActive          AdminStatus = "active"
	AdminStatusBanned          AdminStatus = "banned"
	AdminStatusPendingDeletion AdminStatus = "pending_deletion"
	AdminStatusDeleted         AdminStatus = "deleted"
)

// AdminStatuses lists every AdminStatus.
var AdminStatuses = []AdminStatus{AdminStatusActive, AdminStatusBanned, AdminStatusPendingDeletion, AdminStatusDeleted}

// AdminStatusFromProto maps the Account summary status. It reports false for
// a status that names no AdminStatus.
func AdminStatusFromProto(status managev1.AccountStatus) (AdminStatus, bool) {
	switch status {
	case managev1.AccountStatus_ACCOUNT_STATUS_ACTIVE:
		return AdminStatusActive, true
	case managev1.AccountStatus_ACCOUNT_STATUS_BANNED:
		return AdminStatusBanned, true
	case managev1.AccountStatus_ACCOUNT_STATUS_PENDING_DELETION:
		return AdminStatusPendingDeletion, true
	case managev1.AccountStatus_ACCOUNT_STATUS_DELETED:
		return AdminStatusDeleted, true
	default:
		return "", false
	}
}

const memberAdminPendingDeletionSQL = `EXISTS (
	SELECT 1
	FROM user_deletion_request AS deletion
	WHERE deletion.member_id = m.id
	  AND deletion.lifecycle_state IN ('scheduled', 'recovery_confirmation_pending')
	  AND deletion.scheduled_at IS NOT NULL
)`

var memberAdminBannedSQL = identityread.BannedSQL("i")

var memberAdminStatusSQL = `(CASE
	WHEN m.deleted_at IS NOT NULL OR i.id IS NULL THEN '` + string(AdminStatusDeleted) + `'
	WHEN ` + memberAdminPendingDeletionSQL + ` THEN '` + string(AdminStatusPendingDeletion) + `'
	WHEN ` + memberAdminBannedSQL + ` THEN '` + string(AdminStatusBanned) + `'
	ELSE '` + string(AdminStatusActive) + `'
END)`

var memberAdminFilterConfig = queryutil.FilterConfig{Fields: map[string]queryutil.FieldDef{
	"search": {
		Type:          queryutil.TypeText,
		AllowedOps:    queryutil.SearchOps,
		SearchColumns: []string{"m.nickname", "COALESCE(m.primary_email, '')"},
	},
	"id": {Column: "m.id", Type: queryutil.TypeID, AllowedOps: queryutil.IDOps},
	"nickname": {
		Column: "m.nickname", Type: queryutil.TypeText, AllowedOps: queryutil.TextOps,
	},
	"email": {
		Column: "m.primary_email", Type: queryutil.TypeText, AllowedOps: queryutil.TextOps,
	},
	"banned": {
		Column: memberAdminBannedSQL, Type: queryutil.TypeBool, AllowedOps: queryutil.BoolOps,
	},
	"status": {
		Column:     memberAdminStatusSQL,
		Type:       queryutil.TypeEnum,
		AllowedOps: queryutil.EnumOps,
		EnumValues: adminStatusValues(),
	},
	"newsletter_subscribed": {
		Column: "(ns.identity_id IS NOT NULL)", Type: queryutil.TypeBool, AllowedOps: queryutil.BoolOps,
	},
	"created_at": {
		Column: "m.created_at", Type: queryutil.TypeDate, AllowedOps: queryutil.DateOps,
	},
}}

func adminStatusValues() []string {
	values := make([]string, len(AdminStatuses))
	for i, status := range AdminStatuses {
		values[i] = string(status)
	}
	return values
}

var memberAdminSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"nickname":              "m.nickname",
		"email":                 "m.primary_email",
		"newsletter_subscribed": "(ns.identity_id IS NOT NULL)",
		"created_at":            "m.created_at",
	},
	DefaultSort: "m.created_at DESC, m.id DESC",
}

// SearchMembersQuery searches onboarded Members by nickname for pickers.
type SearchMembersQuery struct {
	Query string
	// Limit defaults to 20 and is capped at 100.
	Limit                int32
	ExcludeMemberIDs     []string
	EffectiveAuthorsOnly bool
}

// SearchMembers returns the summaries matching query in nickname order.
func (s *Service) SearchMembers(ctx context.Context, query SearchMembersQuery) ([]*commonv1.MemberSummary, error) {
	if _, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	limit := int(query.Limit)
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	search := s.db.WithContext(ctx).Where("deleted_at IS NULL AND account_identity_id IS NOT NULL AND onboarded = TRUE")
	if query.EffectiveAuthorsOnly {
		subjects, err := s.spicedb.LookupGlobalSubjects(ctx, policyv1.Platform.LookupAuthorSubjects())
		if err != nil {
			return nil, fmt.Errorf("lookup author candidates: %w", err)
		}
		identityIDs := make([]string, 0, len(subjects))
		for _, subject := range subjects {
			identityIDs = append(identityIDs, subject.ID.String())
		}
		if len(identityIDs) == 0 {
			return nil, nil
		}
		search = search.Where("account_identity_id IN ?", identityIDs)
	}
	if value := strings.TrimSpace(query.Query); value != "" {
		search = search.Where("nickname ILIKE ?", "%"+value+"%")
	}
	if len(query.ExcludeMemberIDs) != 0 {
		search = search.Where("id NOT IN ?", query.ExcludeMemberIDs)
	}
	var members []model.Member
	if err := search.Order("nickname ASC, id ASC").Limit(limit).Find(&members).Error; err != nil {
		return nil, err
	}
	ids := make([]string, len(members))
	for i := range members {
		ids[i] = members[i].ID
	}
	assets, err := s.loadAvatarAssets(ctx, ids)
	if err != nil {
		return nil, err
	}
	result := make([]*commonv1.MemberSummary, len(members))
	for i := range members {
		result[i] = memberSummary(members[i], assets[members[i].ID])
	}
	return result, nil
}

// GetAdminMemberQuery reads one Member as an administrator.
type GetAdminMemberQuery struct {
	MemberID string
}

// GetAdminMember returns the administrator view of one Member and records
// the personal-data access.
func (s *Service) GetAdminMember(ctx context.Context, query GetAdminMemberQuery) (*managev1.AdminMember, error) {
	if _, err := s.requireAdminMember(ctx); err != nil {
		return nil, err
	}
	return s.adminMember(ctx, query.MemberID)
}

// adminMember assembles the administrator view after the caller's
// administrator check, and records the personal-data access.
func (s *Service) adminMember(ctx context.Context, memberID string) (*managev1.AdminMember, error) {
	member, profile, err := s.memberProfileByID(ctx, memberID)
	if err != nil {
		return nil, err
	}
	account, err := s.accounts.SummaryForMember(ctx, s.db, s.spicedb, member.ID)
	if err != nil && !memberIsTombstone(*member) {
		return nil, err
	}
	tags, err := s.memberTagIDs(ctx, member.ID)
	if err != nil {
		return nil, err
	}
	details, err := s.accountAdminDetails(ctx, *member)
	if err != nil {
		return nil, err
	}
	subscriptions, err := s.newsletterSubscriptions(ctx, []string{member.ID})
	if err != nil {
		return nil, err
	}
	result := &managev1.AdminMember{Member: profile, Account: account, TagIds: tags, AccountDetails: details, NewsletterSubscription: newsletterState(subscriptions, member.ID), Onboarded: member.Onboarded}
	if err := s.personalDataAccess.AppendMember(ctx, member.ID); err != nil {
		return nil, securityaccess.Unavailable().WithCause(err)
	}
	return result, nil
}

func newsletterState(subscriptions map[string]time.Time, memberID string) *managev1.NewsletterSubscriptionState {
	if subscribedAt, ok := subscriptions[memberID]; ok {
		return &managev1.NewsletterSubscriptionState{Subscribed: true, SubscribedAt: timestamppb.New(subscribedAt)}
	}
	return &managev1.NewsletterSubscriptionState{}
}

func (s *Service) accountAdminDetails(ctx context.Context, member model.Member) (*managev1.AccountAdminDetails, error) {
	if member.DeletedAt != nil || member.AccountIdentityID == nil {
		return nil, nil
	}
	identity, err := s.kratosIdentityWithOIDCCredentials(ctx, *member.AccountIdentityID)
	if err != nil {
		return nil, err
	}
	if identity.ID != *member.AccountIdentityID || identity.ExternalID != member.ID {
		return nil, domainerr.InvalidSession()
	}
	return s.accountEmails.AdminDetails(ctx, identity)
}

func (s *Service) kratosIdentityWithOIDCCredentials(ctx context.Context, identityID string) (*auth.Identity, error) {
	identity, err := loadMemberIdentityWithEmailCredentials(ctx, s.identity, identityID)
	if err != nil || identity == nil {
		if err == nil {
			err = errors.New("Kratos identity response is empty")
		}
		return nil, fmt.Errorf("read member account details: %w", err)
	}
	return identity, nil
}

func (s *Service) newsletterSubscriptions(ctx context.Context, memberIDs []string) (map[string]time.Time, error) {
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
		`+identityread.JoinLinked("m", "i")+`
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

// ListAdminMembersQuery pages through every Member, including unonboarded,
// banned, pending-deletion and deleted ones.
type ListAdminMembersQuery struct {
	// Limit defaults to 50 and is capped at 100.
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// AdminMemberPage is one page of the administrator Member list.
type AdminMemberPage struct {
	Members    []*managev1.AdminMember
	Pagination *commonv1.PaginationResponse
}

// ListAdminMembers returns one page and records the personal-data access.
func (s *Service) ListAdminMembers(ctx context.Context, query ListAdminMembersQuery) (AdminMemberPage, error) {
	if _, err := s.requireAdminMember(ctx); err != nil {
		return AdminMemberPage{}, err
	}
	limit, offset := 50, int(query.Offset)
	if query.Limit > 0 {
		limit = int(query.Limit)
	}
	if limit > 100 {
		limit = 100
	}
	list := s.db.WithContext(ctx).
		Table("member AS m").
		Joins(identityread.LeftJoinLinked("m", "i")).
		Joins("LEFT JOIN newsletter_subscription AS ns ON ns.identity_id = i.id")
	var err error
	list, err = memberAdminFilterConfig.ApplyFilters(list, query.Filters)
	if err != nil {
		return AdminMemberPage{}, err
	}
	var total int64
	if err := list.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return AdminMemberPage{}, err
	}
	list, err = memberAdminSortConfig.ApplySort(list, query.Sorts)
	if err != nil {
		return AdminMemberPage{}, err
	}
	if len(query.Sorts) != 0 {
		list = list.Order("m.id DESC")
	}
	var members []model.Member
	if err := list.Select("m.*").Limit(limit).Offset(offset).Scan(&members).Error; err != nil {
		return AdminMemberPage{}, err
	}
	ids := make([]string, len(members))
	for i := range members {
		ids[i] = members[i].ID
	}
	assets, err := s.loadAvatarAssets(ctx, ids)
	if err != nil {
		return AdminMemberPage{}, err
	}
	accounts, err := s.accounts.SummariesForMembers(ctx, s.db, s.spicedb, ids)
	if err != nil {
		return AdminMemberPage{}, err
	}
	tags, err := s.memberTagIDsBatch(ctx, ids)
	if err != nil {
		return AdminMemberPage{}, err
	}
	subscriptions, err := s.newsletterSubscriptions(ctx, ids)
	if err != nil {
		return AdminMemberPage{}, err
	}
	result := make([]*managev1.AdminMember, len(members))
	for i, member := range members {
		result[i] = &managev1.AdminMember{Member: memberProfile(member, assets[member.ID]), Account: accounts[member.ID], TagIds: tags[member.ID], NewsletterSubscription: newsletterState(subscriptions, member.ID), Onboarded: member.Onboarded}
	}
	if err := s.personalDataAccess.AppendMemberCollection(ctx); err != nil {
		return AdminMemberPage{}, securityaccess.Unavailable().WithCause(err)
	}
	return AdminMemberPage{Members: result, Pagination: &commonv1.PaginationResponse{Total: int32(min(total, math.MaxInt32)), Limit: int32(limit), Offset: int32(offset), HasMore: int64(offset+len(members)) < total}}, nil
}

// UpdateMemberProfileCommand edits another Member's profile as an
// administrator. A nil field stays unchanged.
type UpdateMemberProfileCommand struct {
	MemberID    string
	Nickname    *string
	Bio         *string
	Website     *string
	SocialLinks map[string]string
}

// UpdateMemberProfile applies command and returns the refreshed
// administrator view.
func (s *Service) UpdateMemberProfile(ctx context.Context, command UpdateMemberProfileCommand) (*managev1.AdminMember, error) {
	if _, err := s.requireAdminMember(ctx); err != nil {
		return nil, err
	}
	if err := s.updateMemberProfileAsAdmin(ctx, command); err != nil {
		return nil, err
	}
	return s.adminMember(ctx, command.MemberID)
}

// SetMemberAvatarCommand replaces another Member's avatar as an
// administrator with an uploaded avatar source File.
type SetMemberAvatarCommand struct {
	MemberID string
	FileID   string
}

// SetMemberAvatar applies command and returns the Member summary.
func (s *Service) SetMemberAvatar(ctx context.Context, command SetMemberAvatarCommand) (*commonv1.MemberSummary, error) {
	if _, err := s.requireAdminMember(ctx); err != nil {
		return nil, err
	}
	return s.setAvatar(ctx, command.MemberID, command.FileID, freshAdmin(), true)
}

// DeleteMemberAvatarCommand removes another Member's avatar as an
// administrator.
type DeleteMemberAvatarCommand struct {
	MemberID string
}

// DeleteMemberAvatar applies command and returns the Member summary.
func (s *Service) DeleteMemberAvatar(ctx context.Context, command DeleteMemberAvatarCommand) (*commonv1.MemberSummary, error) {
	if _, err := s.requireAdminMember(ctx); err != nil {
		return nil, err
	}
	return s.deleteAvatar(ctx, command.MemberID, freshAdmin(), true)
}
