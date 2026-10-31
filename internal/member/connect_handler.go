package member

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ConnectHandler serves the Member Connect RPCs. Each method translates the
// request into a use-case command or query, and the result into the
// response; errs.Wrap maps use-case errors to Connect codes.
type ConnectHandler struct {
	managev1connect.UnimplementedMemberServiceHandler
	members *Service
}

// NewConnectHandler returns the Connect adapter of members.
func NewConnectHandler(members *Service) *ConnectHandler {
	return &ConnectHandler{members: members}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func memberTagMessage(tag Tag) *managev1.MemberTag {
	return &managev1.MemberTag{Id: tag.ID, Name: tag.Name, CreatedAt: timestamppb.New(tag.CreatedAt), MemberCount: tag.MemberCount}
}

func summaryResponse(summary *commonv1.MemberSummary, err error) (*connect.Response[managev1.MemberSummaryMutationResponse], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.MemberSummaryMutationResponse{Member: summary}), nil
}

// clientAddress is the client address the edge proxy forwarded.
func clientAddress(header http.Header) string {
	if value := header.Get("X-Forwarded-For"); value != "" {
		return value
	}
	return header.Get("X-Real-IP")
}

func (h *ConnectHandler) SearchMembers(ctx context.Context, req *connect.Request[managev1.SearchMembersRequest]) (*connect.Response[managev1.SearchMembersResponse], error) {
	members, err := h.members.SearchMembers(ctx, SearchMembersQuery{
		Query: req.Msg.Query, Limit: req.Msg.Limit, ExcludeMemberIDs: req.Msg.ExcludeMemberIds, EffectiveAuthorsOnly: req.Msg.EffectiveAuthorsOnly,
	})
	return respond(&managev1.SearchMembersResponse{Members: members}, err)
}

func (h *ConnectHandler) GetMember(ctx context.Context, req *connect.Request[managev1.GetMemberRequest]) (*connect.Response[managev1.AdminMember], error) {
	return respond(h.members.GetAdminMember(ctx, GetAdminMemberQuery{MemberID: req.Msg.MemberId}))
}

func (h *ConnectHandler) ListMembersAdmin(ctx context.Context, req *connect.Request[managev1.ListMembersAdminRequest]) (*connect.Response[managev1.ListMembersAdminResponse], error) {
	page, err := h.members.ListAdminMembers(ctx, ListAdminMembersQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(), Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	return respond(&managev1.ListMembersAdminResponse{Members: page.Members, Pagination: page.Pagination}, err)
}

func (h *ConnectHandler) UpdateMemberProfile(ctx context.Context, req *connect.Request[managev1.UpdateMemberProfileRequest]) (*connect.Response[managev1.AdminMember], error) {
	return respond(h.members.UpdateMemberProfile(ctx, UpdateMemberProfileCommand{
		MemberID: req.Msg.MemberId, Nickname: req.Msg.Nickname, Bio: req.Msg.Bio, Website: req.Msg.Website, SocialLinks: req.Msg.SocialLinks,
	}))
}

func (h *ConnectHandler) SetMemberAvatar(ctx context.Context, req *connect.Request[managev1.SetMemberAvatarRequest]) (*connect.Response[managev1.MemberSummaryMutationResponse], error) {
	return summaryResponse(h.members.SetMemberAvatar(ctx, SetMemberAvatarCommand{MemberID: req.Msg.MemberId, FileID: req.Msg.FileId}))
}

func (h *ConnectHandler) DeleteMemberAvatar(ctx context.Context, req *connect.Request[managev1.DeleteMemberAvatarRequest]) (*connect.Response[managev1.MemberSummaryMutationResponse], error) {
	return summaryResponse(h.members.DeleteMemberAvatar(ctx, DeleteMemberAvatarCommand{MemberID: req.Msg.MemberId}))
}

func (h *ConnectHandler) ListMemberTagsAdmin(ctx context.Context, req *connect.Request[managev1.ListMemberTagsAdminRequest]) (*connect.Response[managev1.ListMemberTagsAdminResponse], error) {
	page, err := h.members.ListMemberTags(ctx, ListMemberTagsQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(), Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	tags := make([]*managev1.MemberTag, len(page.Tags))
	for i, tag := range page.Tags {
		tags[i] = memberTagMessage(tag)
	}
	return respond(&managev1.ListMemberTagsAdminResponse{Tags: tags, Pagination: page.Pagination}, err)
}

func (h *ConnectHandler) CreateMemberTag(ctx context.Context, req *connect.Request[managev1.CreateMemberTagRequest]) (*connect.Response[managev1.MemberTag], error) {
	tag, err := h.members.CreateMemberTag(ctx, CreateMemberTagCommand{Name: req.Msg.Name})
	return respond(memberTagMessage(tag), err)
}

func (h *ConnectHandler) DeleteMemberTag(ctx context.Context, req *connect.Request[managev1.DeleteMemberTagRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	err := h.members.DeleteMemberTag(ctx, DeleteMemberTagCommand{ID: req.Msg.Id})
	return respond(&managev1.DeleteResponse{Success: true}, err)
}

func (h *ConnectHandler) SetMemberTags(ctx context.Context, req *connect.Request[managev1.SetMemberTagsRequest]) (*connect.Response[managev1.AdminMember], error) {
	return respond(h.members.SetMemberTags(ctx, SetMemberTagsCommand{MemberID: req.Msg.MemberId, TagIDs: req.Msg.TagIds}))
}

func (h *ConnectHandler) CompleteMyOnboarding(ctx context.Context, req *connect.Request[managev1.CompleteMyOnboardingRequest]) (*connect.Response[managev1.CompleteMyOnboardingResponse], error) {
	summary, err := h.members.CompleteOnboarding(ctx, CompleteOnboardingCommand{Nickname: req.Msg.Nickname})
	return respond(&managev1.CompleteMyOnboardingResponse{Member: summary, Onboarded: true}, err)
}

func (h *ConnectHandler) UpdateMyPreferences(ctx context.Context, req *connect.Request[managev1.UpdateMyPreferencesRequest]) (*connect.Response[managev1.UpdateMyPreferencesResponse], error) {
	command := UpdatePreferencesCommand{PreferredLocale: req.Msg.PreferredLocale}
	if consent := req.Msg.CookieConsent; consent != nil {
		command.CookieConsent = &CookieConsentInput{Analytics: consent.Analytics, Version: consent.Version, Source: consent.Source}
	}
	settings, err := h.members.UpdatePreferences(ctx, command)
	return respond(&managev1.UpdateMyPreferencesResponse{Settings: settings}, err)
}

func (h *ConnectHandler) GetMySettings(ctx context.Context, _ *connect.Request[managev1.GetMySettingsRequest]) (*connect.Response[managev1.GetMySettingsResponse], error) {
	return respond(h.members.GetSettings(ctx))
}

func (h *ConnectHandler) UpdateMyProfile(ctx context.Context, req *connect.Request[managev1.UpdateMyProfileRequest]) (*connect.Response[managev1.UpdateMyProfileResponse], error) {
	summary, err := h.members.UpdateOwnProfile(ctx, UpdateOwnProfileCommand{
		Nickname: req.Msg.Nickname, Bio: req.Msg.Bio, Website: req.Msg.Website, SocialLinks: req.Msg.SocialLinks,
	})
	return respond(&managev1.UpdateMyProfileResponse{Member: summary}, err)
}

func (h *ConnectHandler) CheckNicknameAvailability(ctx context.Context, req *connect.Request[managev1.CheckNicknameAvailabilityRequest]) (*connect.Response[managev1.CheckNicknameAvailabilityResponse], error) {
	available, err := h.members.CheckNicknameAvailability(ctx, CheckNicknameAvailabilityQuery{Nickname: req.Msg.Nickname})
	return respond(&managev1.CheckNicknameAvailabilityResponse{Available: available}, err)
}

func (h *ConnectHandler) SetMyAvatar(ctx context.Context, req *connect.Request[managev1.SetMyAvatarRequest]) (*connect.Response[managev1.MemberSummaryMutationResponse], error) {
	return summaryResponse(h.members.SetOwnAvatar(ctx, SetOwnAvatarCommand{FileID: req.Msg.FileId}))
}

func (h *ConnectHandler) DeleteMyAvatar(ctx context.Context, _ *connect.Request[managev1.DeleteMyAvatarRequest]) (*connect.Response[managev1.MemberSummaryMutationResponse], error) {
	return summaryResponse(h.members.DeleteOwnAvatar(ctx))
}

func (h *ConnectHandler) GetCurrentSession(ctx context.Context, req *connect.Request[managev1.GetCurrentSessionRequest]) (*connect.Response[managev1.GetCurrentSessionResponse], error) {
	return respond(h.members.CurrentSession(ctx, CurrentSessionQuery{ClientAddress: clientAddress(req.Header())}))
}

func (h *ConnectHandler) GetMyProfile(ctx context.Context, _ *connect.Request[managev1.GetMyProfileRequest]) (*connect.Response[managev1.GetMyProfileResponse], error) {
	profile, err := h.members.OwnProfile(ctx)
	return respond(&managev1.GetMyProfileResponse{Member: profile}, err)
}

func (h *ConnectHandler) GetMySections(ctx context.Context, _ *connect.Request[managev1.GetMySectionsRequest]) (*connect.Response[managev1.GetMySectionsResponse], error) {
	sections, err := h.members.OwnSections(ctx)
	return respond(&managev1.GetMySectionsResponse{Sections: sections}, err)
}
