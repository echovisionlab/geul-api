package member

import (
	"context"
	"net/http"
	"time"

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

func summaryResponse(summary Summary, err error) (*connect.Response[managev1.MemberSummaryMutationResponse], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.MemberSummaryMutationResponse{Member: SummaryMessage(summary)}), nil
}

// AssetMessage is the API form of a ready asset; nil stays nil.
func AssetMessage(asset *Asset) *commonv1.AssetRef {
	if asset == nil {
		return nil
	}
	return &commonv1.AssetRef{
		AssetId:          asset.ID,
		Url:              asset.URL,
		Extension:        asset.Extension,
		MimeType:         asset.MIMEType,
		FileSize:         asset.FileSize,
		Sha256:           asset.SHA256,
		Disposition:      commonv1.AssetDisposition(asset.Disposition),
		DownloadFilename: asset.DownloadFilename,
	}
}

// AssetFromMessage is the domain form of an API asset; nil stays nil.
func AssetFromMessage(asset *commonv1.AssetRef) *Asset {
	if asset == nil {
		return nil
	}
	return &Asset{
		ID:               asset.AssetId,
		URL:              asset.Url,
		Extension:        asset.Extension,
		MIMEType:         asset.MimeType,
		FileSize:         asset.FileSize,
		SHA256:           asset.Sha256,
		Disposition:      AssetDisposition(asset.Disposition),
		DownloadFilename: asset.DownloadFilename,
	}
}

// SummaryMessage is the API form of a Member summary.
func SummaryMessage(summary Summary) *commonv1.MemberSummary {
	return &commonv1.MemberSummary{Id: summary.ID, Nickname: summary.Nickname, AvatarAsset: AssetMessage(summary.Avatar), Deleted: summary.Deleted}
}

func summaryMessages(summaries []Summary) []*commonv1.MemberSummary {
	if summaries == nil {
		return nil
	}
	messages := make([]*commonv1.MemberSummary, len(summaries))
	for i, summary := range summaries {
		messages[i] = SummaryMessage(summary)
	}
	return messages
}

func timestampMessage(value *time.Time) *timestamppb.Timestamp {
	if value == nil {
		return nil
	}
	return timestamppb.New(*value)
}

func profileMessage(profile Profile) *managev1.MemberProfile {
	return &managev1.MemberProfile{
		Summary: SummaryMessage(profile.Summary), Bio: profile.Bio, Website: profile.Website,
		SocialLinks: profile.SocialLinks, PreferredLocale: profile.PreferredLocale,
		CreatedAt: timestamppb.New(profile.CreatedAt), UpdatedAt: timestampMessage(profile.UpdatedAt),
	}
}

func newsletterMessage(state NewsletterState) *managev1.NewsletterSubscriptionState {
	return &managev1.NewsletterSubscriptionState{Subscribed: state.Subscribed, SubscribedAt: timestampMessage(state.SubscribedAt)}
}

func canonicalEmailMessage(email *CanonicalEmail) *managev1.CanonicalEmailSummary {
	if email == nil {
		return nil
	}
	return &managev1.CanonicalEmailSummary{Email: email.Email, Verified: email.Verified}
}

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

// AdminStatusMessage is the API form of an AdminStatus.
func AdminStatusMessage(status AdminStatus) managev1.AccountStatus {
	switch status {
	case AdminStatusActive:
		return managev1.AccountStatus_ACCOUNT_STATUS_ACTIVE
	case AdminStatusBanned:
		return managev1.AccountStatus_ACCOUNT_STATUS_BANNED
	case AdminStatusPendingDeletion:
		return managev1.AccountStatus_ACCOUNT_STATUS_PENDING_DELETION
	case AdminStatusDeleted:
		return managev1.AccountStatus_ACCOUNT_STATUS_DELETED
	default:
		return managev1.AccountStatus_ACCOUNT_STATUS_UNSPECIFIED
	}
}

func accountSummaryMessage(account *AccountSummary) *managev1.AccountSummary {
	if account == nil {
		return nil
	}
	message := &managev1.AccountSummary{
		MemberId: account.MemberID, CanonicalEmail: canonicalEmailMessage(account.CanonicalEmail),
		Role: account.Role, Status: AdminStatusMessage(account.Status), Banned: account.Banned,
	}
	if ban := account.BanDetails; ban != nil {
		message.BanDetails = &managev1.AccountBanDetails{
			MetadataBanned: ban.MetadataBanned, IdentityState: ban.IdentityState, InactiveState: ban.InactiveState,
			Reason: ban.Reason, ExpiresAt: timestampMessage(ban.ExpiresAt),
		}
	}
	return message
}

// EmailSourceTypeMessage is the API form of an email source type; an
// unknown type is the Identity's current address.
func EmailSourceTypeMessage(sourceType EmailSourceType) managev1.AccountEmailSourceType {
	switch sourceType {
	case EmailSourceEmailCode:
		return managev1.AccountEmailSourceType_ACCOUNT_EMAIL_SOURCE_TYPE_EMAIL_CODE
	case EmailSourceOIDCProvider:
		return managev1.AccountEmailSourceType_ACCOUNT_EMAIL_SOURCE_TYPE_OIDC_PROVIDER
	default:
		return managev1.AccountEmailSourceType_ACCOUNT_EMAIL_SOURCE_TYPE_IDENTITY_CURRENT
	}
}

func accountDetailsMessage(details *AccountDetails) *managev1.AccountAdminDetails {
	if details == nil {
		return nil
	}
	message := &managev1.AccountAdminDetails{Providers: []*managev1.AccountProvider{}}
	for _, provider := range details.Providers {
		message.Providers = append(message.Providers, &managev1.AccountProvider{Provider: provider.Provider, Identifier: provider.Identifier})
	}
	for _, candidate := range details.EmailCandidates {
		candidateMessage := &managev1.AccountEmailCandidate{
			Email: candidate.Email, NormalizedEmail: candidate.NormalizedEmail, Current: candidate.Current,
			IdentityVerified: candidate.IdentityVerified, EffectiveTrusted: candidate.EffectiveTrusted,
			UsableForDelivery: candidate.UsableForDelivery,
		}
		for _, source := range candidate.Sources {
			candidateMessage.Sources = append(candidateMessage.Sources, &managev1.AccountEmailSource{
				SourceType: EmailSourceTypeMessage(source.Type), Provider: source.Provider, ProviderSubject: source.ProviderSubject,
			})
		}
		message.EmailCandidates = append(message.EmailCandidates, candidateMessage)
	}
	return message
}

// AdminMemberMessage is the API form of the administrator view of a Member.
func AdminMemberMessage(member AdminMember) *managev1.AdminMember {
	return &managev1.AdminMember{
		Member: profileMessage(member.Profile), Account: accountSummaryMessage(member.Account), TagIds: member.TagIDs,
		AccountDetails: accountDetailsMessage(member.AccountDetails), NewsletterSubscription: newsletterMessage(member.Newsletter),
		Onboarded: member.Onboarded,
	}
}

func adminMemberResponse(member AdminMember, err error) (*connect.Response[managev1.AdminMember], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(AdminMemberMessage(member)), nil
}

// PaginationMessage is the API form of a page.
func PaginationMessage(page Pagination) *commonv1.PaginationResponse {
	return &commonv1.PaginationResponse{Total: page.Total, Limit: page.Limit, Offset: page.Offset, HasMore: page.HasMore}
}

func settingsMessage(settings Settings) *managev1.GetMySettingsResponse {
	message := &managev1.GetMySettingsResponse{
		PreferredLocale: settings.PreferredLocale, NewsletterSubscription: newsletterMessage(settings.Newsletter),
		CanonicalEmail: canonicalEmailMessage(settings.CanonicalEmail),
	}
	if consent := settings.CookieConsent; consent != nil {
		message.CookieConsent = &managev1.CookieConsent{
			Essential: consent.Essential, Analytics: consent.Analytics, Version: consent.Version,
			UpdatedAt: timestamppb.New(consent.RecordedAt), Source: consent.Source,
		}
	}
	return message
}

func sessionMessage(session Session) *managev1.GetCurrentSessionResponse {
	metadata := &managev1.RequestMetadata{IpAddress: session.Metadata.IPAddress}
	if geo := session.Metadata.Geo; geo != nil {
		metadata.Geo = &managev1.GeoIPInfo{
			CountryCode: geo.CountryCode,
			CountryName: geo.CountryName,
			Latitude:    geo.Latitude,
			Longitude:   geo.Longitude,
			IsProxy:     geo.IsProxy,
			IsSatellite: geo.IsSatellite,
		}
		if geo.City != "" {
			metadata.Geo.City = &geo.City
		}
		if geo.TimeZone != "" {
			metadata.Geo.TimeZone = &geo.TimeZone
		}
	}
	return &managev1.GetCurrentSessionResponse{
		Member: &managev1.CurrentSessionMember{
			Summary: SummaryMessage(session.Member), PreferredLocale: session.PreferredLocale,
			Role: session.Role, Status: AdminStatusMessage(session.Status), Email: session.Email,
		},
		Metadata:           metadata,
		Onboarded:          session.Onboarded,
		AccountIdentityId:  session.AccountIdentityID,
		NicknameSuggestion: session.NicknameSuggestion,
	}
}

var sectionMessages = map[Section]managev1.MySection{
	SectionProfile:  managev1.MySection_MY_SECTION_PROFILE,
	SectionSecurity: managev1.MySection_MY_SECTION_SECURITY,
	SectionSettings: managev1.MySection_MY_SECTION_SETTINGS,
	SectionPosts:    managev1.MySection_MY_SECTION_POSTS,
	SectionSeries:   managev1.MySection_MY_SECTION_SERIES,
	SectionWorks:    managev1.MySection_MY_SECTION_WORKS,
	SectionArtists:  managev1.MySection_MY_SECTION_ARTISTS,
	SectionForms:    managev1.MySection_MY_SECTION_FORMS,
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
	return respond(&managev1.SearchMembersResponse{Members: summaryMessages(members)}, err)
}

func (h *ConnectHandler) GetMember(ctx context.Context, req *connect.Request[managev1.GetMemberRequest]) (*connect.Response[managev1.AdminMember], error) {
	return adminMemberResponse(h.members.GetAdminMember(ctx, GetAdminMemberQuery{MemberID: req.Msg.MemberId}))
}

func (h *ConnectHandler) ListMembersAdmin(ctx context.Context, req *connect.Request[managev1.ListMembersAdminRequest]) (*connect.Response[managev1.ListMembersAdminResponse], error) {
	page, err := h.members.ListAdminMembers(ctx, ListAdminMembersQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(), Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	members := make([]*managev1.AdminMember, len(page.Members))
	for i, member := range page.Members {
		members[i] = AdminMemberMessage(member)
	}
	return connect.NewResponse(&managev1.ListMembersAdminResponse{Members: members, Pagination: PaginationMessage(page.Pagination)}), nil
}

func (h *ConnectHandler) UpdateMemberProfile(ctx context.Context, req *connect.Request[managev1.UpdateMemberProfileRequest]) (*connect.Response[managev1.AdminMember], error) {
	return adminMemberResponse(h.members.UpdateMemberProfile(ctx, UpdateMemberProfileCommand{
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
	if err != nil {
		return nil, errs.Wrap(err)
	}
	tags := make([]*managev1.MemberTag, len(page.Tags))
	for i, tag := range page.Tags {
		tags[i] = memberTagMessage(tag)
	}
	return connect.NewResponse(&managev1.ListMemberTagsAdminResponse{Tags: tags, Pagination: PaginationMessage(page.Pagination)}), nil
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
	return adminMemberResponse(h.members.SetMemberTags(ctx, SetMemberTagsCommand{MemberID: req.Msg.MemberId, TagIDs: req.Msg.TagIds}))
}

func (h *ConnectHandler) CompleteMyOnboarding(ctx context.Context, req *connect.Request[managev1.CompleteMyOnboardingRequest]) (*connect.Response[managev1.CompleteMyOnboardingResponse], error) {
	summary, err := h.members.CompleteOnboarding(ctx, CompleteOnboardingCommand{Nickname: req.Msg.Nickname})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.CompleteMyOnboardingResponse{Member: SummaryMessage(summary), Onboarded: true}), nil
}

func (h *ConnectHandler) UpdateMyPreferences(ctx context.Context, req *connect.Request[managev1.UpdateMyPreferencesRequest]) (*connect.Response[managev1.UpdateMyPreferencesResponse], error) {
	command := UpdatePreferencesCommand{PreferredLocale: req.Msg.PreferredLocale}
	if consent := req.Msg.CookieConsent; consent != nil {
		command.CookieConsent = &CookieConsentInput{Analytics: consent.Analytics, Version: consent.Version, Source: consent.Source}
	}
	settings, err := h.members.UpdatePreferences(ctx, command)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.UpdateMyPreferencesResponse{Settings: settingsMessage(settings)}), nil
}

func (h *ConnectHandler) GetMySettings(ctx context.Context, _ *connect.Request[managev1.GetMySettingsRequest]) (*connect.Response[managev1.GetMySettingsResponse], error) {
	settings, err := h.members.GetSettings(ctx)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(settingsMessage(settings)), nil
}

func (h *ConnectHandler) UpdateMyProfile(ctx context.Context, req *connect.Request[managev1.UpdateMyProfileRequest]) (*connect.Response[managev1.UpdateMyProfileResponse], error) {
	summary, err := h.members.UpdateOwnProfile(ctx, UpdateOwnProfileCommand{
		Nickname: req.Msg.Nickname, Bio: req.Msg.Bio, Website: req.Msg.Website, SocialLinks: req.Msg.SocialLinks,
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.UpdateMyProfileResponse{Member: SummaryMessage(summary)}), nil
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
	session, err := h.members.CurrentSession(ctx, CurrentSessionQuery{ClientAddress: clientAddress(req.Header())})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(sessionMessage(session)), nil
}

func (h *ConnectHandler) GetMyProfile(ctx context.Context, _ *connect.Request[managev1.GetMyProfileRequest]) (*connect.Response[managev1.GetMyProfileResponse], error) {
	profile, err := h.members.OwnProfile(ctx)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.GetMyProfileResponse{Member: profileMessage(profile)}), nil
}

func (h *ConnectHandler) GetMySections(ctx context.Context, _ *connect.Request[managev1.GetMySectionsRequest]) (*connect.Response[managev1.GetMySectionsResponse], error) {
	sections, err := h.members.OwnSections(ctx)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	var messages []managev1.MySection
	for _, section := range sections {
		messages = append(messages, sectionMessages[section])
	}
	return connect.NewResponse(&managev1.GetMySectionsResponse{Sections: messages}), nil
}
