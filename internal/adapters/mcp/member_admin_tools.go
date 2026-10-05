package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	ToolMemberAdminList = "member_admin_list"
	ToolMemberAdminGet  = "member_admin_get"
)

var memberAdminTools = []mcpserver.Tool{
	relatedTool(ToolMemberAdminList, "List members as administrator", "Search members by nickname or email, including unonboarded, banned, pending-deletion, and deleted members. Requires current administrator permission and records personal-data access through the owning Member service.", memberAdminListInputJSONSchema, memberAdminListOutputJSONSchema, true, false),
	relatedTool(ToolMemberAdminGet, "Get member as administrator", "Read a member profile, account status, global role, email verification, and administrative state. Requires current administrator permission and records personal-data access. Authentication credentials and provider identifiers are excluded.", memberAdminGetInputJSONSchema, memberAdminGetOutputJSONSchema, true, false),
}

// MemberAdminReader retains the owning service's authorization and access audit.
type MemberAdminReader interface {
	ListMembersAdmin(context.Context, *connect.Request[managev1.ListMembersAdminRequest]) (*connect.Response[managev1.ListMembersAdminResponse], error)
	GetMember(context.Context, *connect.Request[managev1.GetMemberRequest]) (*connect.Response[managev1.AdminMember], error)
}

type MemberAdminTools struct{ reader MemberAdminReader }

func NewMemberAdminTools(reader MemberAdminReader) (*MemberAdminTools, error) {
	if interfaceValueIsNil(reader) {
		return nil, errors.New("MCP administrator Member reader is required")
	}
	return &MemberAdminTools{reader: reader}, nil
}

func (*MemberAdminTools) ToolNames() []string { return toolDefinitionNames(memberAdminTools) }

func (*MemberAdminTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return cloneToolDefinitions(memberAdminTools), nil
}

func (tools *MemberAdminTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	switch name {
	case ToolMemberAdminList:
		if err := rejectNullArguments(arguments, "query", "status", "limit", "offset"); err != nil {
			return executionError(err)
		}
		var input struct {
			Query  string `json:"query"`
			Status string `json:"status"`
			Limit  int    `json:"limit"`
			Offset int    `json:"offset"`
		}
		if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		if _, supplied := arguments["limit"]; !supplied {
			input.Limit = 20
		}
		if input.Limit < 1 || input.Limit > 100 {
			return executionError(errors.New("limit must be between 1 and 100"))
		}
		if input.Offset < 0 || input.Offset > 1<<31-1 {
			return executionError(errors.New("offset must be between 0 and 2147483647"))
		}
		filters := discoverySearchFilters(input.Query)
		if _, supplied := arguments["status"]; supplied {
			switch input.Status {
			case "active", "banned", "pending_deletion", "deleted":
				filters = append(filters, &commonv1.FilterSpec{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: input.Status})
			default:
				return executionError(errors.New("status must be active, banned, pending_deletion, or deleted"))
			}
		}
		response, err := tools.reader.ListMembersAdmin(ctx, connect.NewRequest(&managev1.ListMembersAdminRequest{
			Pagination: &commonv1.PaginationRequest{Limit: int32(input.Limit), Offset: int32(input.Offset)}, Filters: filters,
		}))
		if err != nil {
			return expectedToolError(err)
		}
		members := make([]memberAdminOutput, 0, len(response.Msg.Members))
		for _, member := range response.Msg.Members {
			members = append(members, projectMemberAdmin(member))
		}
		return memberAdminResult(struct {
			Members []memberAdminOutput `json:"members"`
			Total   int32               `json:"total"`
			Limit   int32               `json:"limit"`
			Offset  int32               `json:"offset"`
			HasMore bool                `json:"has_more"`
		}{members, response.Msg.Pagination.GetTotal(), response.Msg.Pagination.GetLimit(), response.Msg.Pagination.GetOffset(), response.Msg.Pagination.GetHasMore()})
	case ToolMemberAdminGet:
		var input struct {
			MemberID string `json:"member_id"`
		}
		if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		if err := validateUUID("member_id", input.MemberID); err != nil {
			return executionError(err)
		}
		response, err := tools.reader.GetMember(ctx, connect.NewRequest(&managev1.GetMemberRequest{MemberId: input.MemberID}))
		if err != nil {
			return expectedToolError(err)
		}
		return memberAdminResult(struct {
			Member memberAdminOutput `json:"member"`
		}{projectMemberAdmin(response.Msg)})
	default:
		return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
	}
}

type memberAdminOutput struct {
	Profile                memberAdminProfileOutput      `json:"profile"`
	Account                *memberAdminAccountOutput     `json:"account"`
	TagIDs                 []string                      `json:"tag_ids"`
	Onboarded              bool                          `json:"onboarded"`
	NewsletterSubscription memberAdminSubscriptionOutput `json:"newsletter_subscription"`
}

type memberAdminProfileOutput struct {
	ID              string            `json:"id"`
	Nickname        string            `json:"nickname"`
	Deleted         bool              `json:"deleted"`
	AvatarAssetID   string            `json:"avatar_asset_id,omitempty"`
	Bio             *string           `json:"bio,omitempty"`
	Website         *string           `json:"website,omitempty"`
	SocialLinks     map[string]string `json:"social_links,omitempty"`
	PreferredLocale *string           `json:"preferred_locale,omitempty"`
	CreatedAt       string            `json:"created_at,omitempty"`
	UpdatedAt       string            `json:"updated_at,omitempty"`
}

type memberAdminAccountOutput struct {
	CanonicalEmail *memberAdminEmailOutput `json:"canonical_email,omitempty"`
	Role           string                  `json:"role"`
	Status         string                  `json:"status"`
	Banned         bool                    `json:"banned"`
	BanDetails     *memberAdminBanOutput   `json:"ban_details,omitempty"`
}

type memberAdminEmailOutput struct {
	Email    string `json:"email"`
	Verified bool   `json:"verified"`
}

type memberAdminBanOutput struct {
	MetadataBanned bool    `json:"metadata_banned"`
	IdentityState  string  `json:"identity_state"`
	InactiveState  bool    `json:"inactive_state"`
	Reason         *string `json:"reason,omitempty"`
	ExpiresAt      string  `json:"expires_at,omitempty"`
}

type memberAdminSubscriptionOutput struct {
	Subscribed   bool   `json:"subscribed"`
	SubscribedAt string `json:"subscribed_at,omitempty"`
}

func projectMemberAdmin(member *managev1.AdminMember) memberAdminOutput {
	profile, account, subscription := member.GetMember(), member.GetAccount(), member.GetNewsletterSubscription()
	result := memberAdminOutput{
		Profile: memberAdminProfileOutput{ID: profile.GetSummary().GetId(), Nickname: profile.GetSummary().GetNickname(), Deleted: profile.GetSummary().GetDeleted(),
			AvatarAssetID: profile.GetSummary().GetAvatarAsset().GetAssetId(),
			Bio:           profile.Bio, Website: profile.Website, SocialLinks: profile.GetSocialLinks(), PreferredLocale: profile.PreferredLocale,
			CreatedAt: timestampString(profile.GetCreatedAt()), UpdatedAt: timestampString(profile.GetUpdatedAt())},
		TagIDs: append([]string{}, member.GetTagIds()...), Onboarded: member.GetOnboarded(),
		NewsletterSubscription: memberAdminSubscriptionOutput{Subscribed: subscription.GetSubscribed(), SubscribedAt: timestampString(subscription.GetSubscribedAt())},
	}
	if account != nil {
		result.Account = &memberAdminAccountOutput{Role: strings.ToLower(account.GetRole().String()), Status: strings.ToLower(strings.TrimPrefix(account.GetStatus().String(), "ACCOUNT_STATUS_")), Banned: account.GetBanned()}
		if email := account.GetCanonicalEmail(); email != nil {
			result.Account.CanonicalEmail = &memberAdminEmailOutput{Email: email.GetEmail(), Verified: email.GetVerified()}
		}
		if ban := account.GetBanDetails(); ban != nil {
			result.Account.BanDetails = &memberAdminBanOutput{MetadataBanned: ban.GetMetadataBanned(), IdentityState: ban.GetIdentityState(), InactiveState: ban.GetInactiveState(), Reason: ban.Reason, ExpiresAt: timestampString(ban.GetExpiresAt())}
		}
	}
	return result
}

func memberAdminResult(value any) (mcpserver.ToolResult, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return mcpserver.ToolResult{}, fmt.Errorf("encode MCP administrator Member result: %w", err)
	}
	return structuredResult(encoded, false)
}
