package member

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/geoip"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *Service) currentMySections(
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
		return nil, domainerr.InvalidSession().WithCause(err)
	}
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	if err != nil {
		return nil, domainerr.InvalidSession().WithCause(err)
	}
	resources := []struct {
		lookup  policyv1.ResourceLookup
		section managev1.MySection
	}{
		{lookup: policyv1.Work.LookupManage(), section: managev1.MySection_MY_SECTION_WORKS},
		{lookup: policyv1.Artist.LookupManage(), section: managev1.MySection_MY_SECTION_ARTISTS},
		{lookup: policyv1.Form.LookupManage(), section: managev1.MySection_MY_SECTION_FORMS},
	}
	for _, resource := range resources {
		ids, lookupErr := s.spicedb.LookupResources(ctx, resource.lookup, actor)
		if lookupErr != nil {
			return nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(fmt.Errorf("lookup section resources: %w", lookupErr))
		}
		if len(ids) != 0 {
			sections = append(sections, resource.section)
		}
	}
	return sections, nil
}

// requestMetadata describes the request a session read arrived on.
// clientAddress is the address the transport reports for the client.
func requestMetadata(ctx context.Context, clientAddress string) *managev1.RequestMetadata {
	metadata := &managev1.RequestMetadata{IpAddress: clientAddress}
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
	return metadata
}

// CurrentSessionQuery reads the authenticated session. ClientAddress is the
// client address the transport received the request from.
type CurrentSessionQuery struct {
	ClientAddress string
}

// CurrentSession returns the authenticated Member's session view, with a
// nickname suggestion while the Member is not onboarded.
func (s *Service) CurrentSession(ctx context.Context, query CurrentSessionQuery) (*managev1.GetCurrentSessionResponse, error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	member, err := s.loadCurrentSessionMember(ctx, principal.MemberID.String())
	if err != nil {
		return nil, err
	}
	if member.AccountIdentityID == nil || *member.AccountIdentityID != principal.IdentityID.String() {
		return nil, domainerr.InvalidSession()
	}
	assets, err := s.loadAvatarAssets(ctx, []string{member.ID})
	if err != nil {
		return nil, err
	}
	account, err := s.accounts.SessionSummaryForMember(ctx, s.db, s.spicedb, member.ID)
	if err != nil {
		return nil, err
	}
	if account.MemberId != member.ID {
		return nil, domainerr.InvalidSession()
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
	session := &managev1.GetCurrentSessionResponse{
		Member:            sessionMember,
		Metadata:          requestMetadata(ctx, query.ClientAddress),
		Onboarded:         member.Onboarded,
		AccountIdentityId: principal.IdentityID.String(),
	}
	if !member.Onboarded {
		session.NicknameSuggestion = s.nicknameSuggestion(ctx, *member)
	}
	return session, nil
}

// OwnProfile returns the authenticated Member's profile. Bio, website and
// social links are hidden unless the Member holds the Author role.
func (s *Service) OwnProfile(ctx context.Context) (*managev1.MemberProfile, error) {
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
	return profile, nil
}

// OwnSections returns the account sections the authenticated Member may open.
func (s *Service) OwnSections(ctx context.Context) ([]managev1.MySection, error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if !principal.Onboarded {
		return nil, nil
	}
	author, err := s.isGlobalAuthor(ctx)
	if err != nil {
		return nil, err
	}
	return s.currentMySections(ctx, principal.IdentityID, author)
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
