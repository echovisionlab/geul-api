package member

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/geoip"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *MemberService) currentMySections(
	ctx context.Context,
	identityID auth.IdentityID,
	author bool,
) ([]managev1.MySection, error) {
	sections := []managev1.MySection{managev1.MySection_MY_SECTION_PROFILE, managev1.MySection_MY_SECTION_SECURITY, managev1.MySection_MY_SECTION_SETTINGS}
	if author {
		sections = append(sections, managev1.MySection_MY_SECTION_POSTS, managev1.MySection_MY_SECTION_SERIES)
	}
	subject, err := auth.NewAccountIdentitySubject(identityID)
	if err != nil {
		return nil, errs.InvalidSession()
	}
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	if err != nil {
		return nil, errs.InvalidSession()
	}
	resources := []struct {
		lookup  policyv1.ResourceLookup
		section managev1.MySection
	}{
		{lookup: policyv1.Work.LookupManage(), section: managev1.MySection_MY_SECTION_WORKS},
		{lookup: policyv1.Form.LookupManage(), section: managev1.MySection_MY_SECTION_FORMS},
	}
	for _, resource := range resources {
		ids, lookupErr := s.spicedb.LookupResources(ctx, resource.lookup, actor)
		if lookupErr != nil {
			return nil, errs.DependencyUnavailable("SpiceDB")
		}
		if len(ids) != 0 {
			sections = append(sections, resource.section)
		}
	}
	return sections, nil
}

func buildRequestMetadata(ctx context.Context, header http.Header) *managev1.RequestMetadata {
	metadata := &managev1.RequestMetadata{}
	if geo := geoip.GetInfo(ctx); geo != nil {
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
	if value := header.Get("X-Forwarded-For"); value != "" {
		metadata.IpAddress = value
	} else {
		metadata.IpAddress = header.Get("X-Real-IP")
	}
	return metadata
}

func (s *MemberService) GetCurrentSession(
	ctx context.Context,
	req *connect.Request[managev1.GetCurrentSessionRequest],
) (*connect.Response[managev1.GetCurrentSessionResponse], error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	member, err := s.loadCurrentSessionMember(ctx, principal.MemberID.String())
	if err != nil {
		return nil, err
	}
	if member.AccountIdentityID == nil || *member.AccountIdentityID != principal.IdentityID.String() {
		return nil, errs.InvalidSession()
	}
	assets, err := s.loadAvatarAssets(ctx, []string{member.ID})
	if err != nil {
		return nil, errs.Internal(err)
	}
	account, err := s.sessionAccountSummary(ctx, member.ID)
	if err != nil {
		return nil, err
	}
	if account.MemberId != member.ID {
		return nil, errs.InvalidSession()
	}
	sessionMember := &managev1.CurrentSessionMember{
		Summary:         memberSummary(*member, assets[member.ID]),
		PreferredLocale: member.PreferredLocale,
		Role:            account.Role,
		Status:          account.Status,
	}
	if account.CanonicalEmail != nil {
		sessionMember.Email = &account.CanonicalEmail.Email
	}
	metadata := buildRequestMetadata(ctx, req.Header())
	response := &managev1.GetCurrentSessionResponse{
		Member:            sessionMember,
		Metadata:          metadata,
		Onboarded:         member.Onboarded,
		AccountIdentityId: principal.IdentityID.String(),
	}
	if !member.Onboarded {
		response.NicknameSuggestion = s.nicknameSuggestion(ctx, *member)
	}
	return connect.NewResponse(response), nil
}

func (s *MemberService) GetMyProfile(
	ctx context.Context,
	_ *connect.Request[managev1.GetMyProfileRequest],
) (*connect.Response[managev1.GetMyProfileResponse], error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	_, profile, err := s.memberProfileByID(ctx, principal.MemberID.String())
	if err != nil {
		return nil, err
	}
	author, err := s.isGlobalAuthor(ctx)
	if err != nil {
		return nil, err
	}
	if !author {
		hideExtendedMemberProfile(profile)
	}
	return connect.NewResponse(&managev1.GetMyProfileResponse{Member: profile}), nil
}

func (s *MemberService) GetMySections(
	ctx context.Context,
	_ *connect.Request[managev1.GetMySectionsRequest],
) (*connect.Response[managev1.GetMySectionsResponse], error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if !principal.Onboarded {
		return connect.NewResponse(&managev1.GetMySectionsResponse{}), nil
	}
	author, err := s.isGlobalAuthor(ctx)
	if err != nil {
		return nil, err
	}
	sections, err := s.currentMySections(ctx, principal.IdentityID, author)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.GetMySectionsResponse{Sections: sections}), nil
}

func cookieConsentProto(row *model.UserCookieConsent) *managev1.CookieConsent {
	if row == nil {
		return nil
	}
	return &managev1.CookieConsent{
		Essential: row.Essential,
		Analytics: row.Analytics,
		Version:   row.ConsentVersion,
		UpdatedAt: timestamppb.New(row.RecordedAt),
		Source:    row.Source,
	}
}
