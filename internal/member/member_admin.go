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
	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/echovisionlab/geul-api/internal/securityaccess"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
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

// AdminMember is the administrator view of one Member.
type AdminMember struct {
	Profile        Profile
	Account        *AccountSummary
	TagIDs         []string
	AccountDetails *AccountDetails
	Newsletter     NewsletterState
	Onboarded      bool
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
func (s *Service) SearchMembers(ctx context.Context, query SearchMembersQuery) ([]memberref.Summary, error) {
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
	search := MemberSearch{Limit: limit, Query: strings.TrimSpace(query.Query), ExcludeMemberIDs: query.ExcludeMemberIDs}
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
		search.IdentityIDs = identityIDs
	}
	var result []memberref.Summary
	err := s.read(ctx, func(store Store) error {
		members, err := store.SearchMembers(ctx, search)
		if err != nil {
			return err
		}
		ids := make([]string, len(members))
		for i := range members {
			ids[i] = members[i].ID
		}
		assets, err := loadAvatars(ctx, store, ids)
		if err != nil {
			return err
		}
		result = make([]memberref.Summary, len(members))
		for i := range members {
			result[i] = memberSummary(members[i], assets[members[i].ID])
		}
		return nil
	})
	return result, err
}

// GetAdminMemberQuery reads one Member as an administrator.
type GetAdminMemberQuery struct {
	MemberID string
}

// GetAdminMember returns the administrator view of one Member and records
// the personal-data access.
func (s *Service) GetAdminMember(ctx context.Context, query GetAdminMemberQuery) (AdminMember, error) {
	if _, err := s.requireAdminMember(ctx); err != nil {
		return AdminMember{}, err
	}
	return s.adminMember(ctx, query.MemberID)
}

// adminMember assembles the administrator view after the caller's
// administrator check, and records the personal-data access.
func (s *Service) adminMember(ctx context.Context, memberID string) (AdminMember, error) {
	member, profile, err := s.memberProfileByID(ctx, memberID)
	if err != nil {
		return AdminMember{}, err
	}
	account, err := s.accounts.Summary(ctx, member.ID)
	if err != nil && !member.tombstone() {
		return AdminMember{}, err
	}
	var tags []string
	if err := s.read(ctx, func(store Store) error {
		byMember, err := store.TagIDs(ctx, []string{member.ID})
		tags = byMember[member.ID]
		return err
	}); err != nil {
		return AdminMember{}, err
	}
	if tags == nil {
		tags = []string{}
	}
	details, err := s.accountAdminDetails(ctx, member)
	if err != nil {
		return AdminMember{}, err
	}
	var subscriptions map[string]time.Time
	if err := s.read(ctx, func(store Store) error {
		var err error
		subscriptions, err = store.NewsletterSubscriptions(ctx, []string{member.ID})
		return err
	}); err != nil {
		return AdminMember{}, err
	}
	result := AdminMember{Profile: profile, Account: account, TagIDs: tags, AccountDetails: details, Newsletter: subscribedState(subscriptions, member.ID), Onboarded: member.Onboarded}
	if err := s.personalDataAccess.AppendMember(ctx, member.ID); err != nil {
		return AdminMember{}, securityaccess.Unavailable().WithCause(err)
	}
	return result, nil
}

func subscribedState(subscriptions map[string]time.Time, memberID string) NewsletterState {
	if subscribedAt, ok := subscriptions[memberID]; ok {
		return NewsletterState{Subscribed: true, SubscribedAt: &subscribedAt}
	}
	return NewsletterState{}
}

func (s *Service) accountAdminDetails(ctx context.Context, member Member) (*AccountDetails, error) {
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
	return s.accounts.AdminDetails(ctx, identity)
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
	Members    []AdminMember
	Pagination Pagination
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
	var (
		members []Member
		total   int64
		assets  map[string]*memberref.Asset
		ids     []string
	)
	if err := s.read(ctx, func(store Store) error {
		var err error
		if members, total, err = store.AdminMemberPage(ctx, query.Filters, query.Sorts, limit, offset); err != nil {
			return err
		}
		ids = make([]string, len(members))
		for i := range members {
			ids[i] = members[i].ID
		}
		assets, err = loadAvatars(ctx, store, ids)
		return err
	}); err != nil {
		return AdminMemberPage{}, err
	}
	accounts, err := s.accounts.Summaries(ctx, ids)
	if err != nil {
		return AdminMemberPage{}, err
	}
	var (
		tags          map[string][]string
		subscriptions map[string]time.Time
	)
	if err := s.read(ctx, func(store Store) error {
		var err error
		if len(ids) != 0 {
			if tags, err = store.TagIDs(ctx, ids); err != nil {
				return err
			}
		}
		subscriptions, err = loadNewsletterSubscriptions(ctx, store, ids)
		return err
	}); err != nil {
		return AdminMemberPage{}, err
	}
	result := make([]AdminMember, len(members))
	for i, member := range members {
		result[i] = AdminMember{Profile: memberProfile(member, assets[member.ID]), Account: accounts[member.ID], TagIDs: tags[member.ID], Newsletter: subscribedState(subscriptions, member.ID), Onboarded: member.Onboarded}
	}
	if err := s.personalDataAccess.AppendMemberCollection(ctx); err != nil {
		return AdminMemberPage{}, securityaccess.Unavailable().WithCause(err)
	}
	return AdminMemberPage{Members: result, Pagination: Pagination{Total: int32(min(total, math.MaxInt32)), Limit: int32(limit), Offset: int32(offset), HasMore: int64(offset+len(members)) < total}}, nil
}

// loadNewsletterSubscriptions reads no row for no Members.
func loadNewsletterSubscriptions(ctx context.Context, store Store, memberIDs []string) (map[string]time.Time, error) {
	if len(memberIDs) == 0 {
		return map[string]time.Time{}, nil
	}
	return store.NewsletterSubscriptions(ctx, memberIDs)
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
func (s *Service) UpdateMemberProfile(ctx context.Context, command UpdateMemberProfileCommand) (AdminMember, error) {
	if _, err := s.requireAdminMember(ctx); err != nil {
		return AdminMember{}, err
	}
	if err := s.updateMemberProfileAsAdmin(ctx, command); err != nil {
		return AdminMember{}, err
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
func (s *Service) SetMemberAvatar(ctx context.Context, command SetMemberAvatarCommand) (memberref.Summary, error) {
	if _, err := s.requireAdminMember(ctx); err != nil {
		return memberref.Summary{}, err
	}
	return s.setAvatar(ctx, command.MemberID, command.FileID, freshAdmin(), true)
}

// DeleteMemberAvatarCommand removes another Member's avatar as an
// administrator.
type DeleteMemberAvatarCommand struct {
	MemberID string
}

// DeleteMemberAvatar applies command and returns the Member summary.
func (s *Service) DeleteMemberAvatar(ctx context.Context, command DeleteMemberAvatarCommand) (memberref.Summary, error) {
	if _, err := s.requireAdminMember(ctx); err != nil {
		return memberref.Summary{}, err
	}
	return s.deleteAvatar(ctx, command.MemberID, freshAdmin(), true)
}
