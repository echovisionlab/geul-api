package member

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/geoip"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Section is one account section a Member may open.
type Section string

// The account sections, in the order a Member sees them.
const (
	SectionProfile  Section = "profile"
	SectionSecurity Section = "security"
	SectionSettings Section = "settings"
	SectionPosts    Section = "posts"
	SectionSeries   Section = "series"
	SectionWorks    Section = "works"
	SectionArtists  Section = "artists"
	SectionForms    Section = "forms"
)

func (s *Service) currentMySections(
	ctx context.Context,
	identityID auth.IdentityID,
	author bool,
) ([]Section, error) {
	sections := []Section{SectionProfile, SectionSecurity, SectionSettings}
	if author {
		sections = append(sections, SectionPosts, SectionSeries)
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
		section Section
	}{
		{lookup: policyv1.Work.LookupManage(), section: SectionWorks},
		{lookup: policyv1.Artist.LookupManage(), section: SectionArtists},
		{lookup: policyv1.Form.LookupManage(), section: SectionForms},
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

// RequestMetadata describes the request a session read arrived on.
type RequestMetadata struct {
	IPAddress string
	Geo       *geoip.Info
}

// requestMetadata describes the request a session read arrived on.
// clientAddress is the address the transport reports for the client.
func requestMetadata(ctx context.Context, clientAddress string) RequestMetadata {
	return RequestMetadata{IPAddress: clientAddress, Geo: geoip.GetInfo(ctx)}
}

// Session is the authenticated Member's session view.
type Session struct {
	Member             Summary
	PreferredLocale    *string
	Role               policyv1.AuthorizationRole
	Status             AdminStatus
	Email              *string
	Metadata           RequestMetadata
	Onboarded          bool
	AccountIdentityID  string
	NicknameSuggestion *string
}

// CurrentSessionQuery reads the authenticated session. ClientAddress is the
// client address the transport received the request from.
type CurrentSessionQuery struct {
	ClientAddress string
}

// CurrentSession returns the authenticated Member's session view, with a
// nickname suggestion while the Member is not onboarded.
func (s *Service) CurrentSession(ctx context.Context, query CurrentSessionQuery) (Session, error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return Session{}, err
	}
	var (
		member Member
		assets map[string]*Asset
	)
	if err := s.read(ctx, func(store Store) error {
		var err error
		member, err = store.SessionMember(ctx, principal.MemberID.String())
		if err != nil || member.AccountIdentityID == nil || *member.AccountIdentityID != principal.IdentityID.String() {
			return err
		}
		assets, err = store.Avatars(ctx, []string{member.ID})
		return err
	}); err != nil {
		if errors.Is(err, ErrMemberNotFound) {
			return Session{}, domainerr.ResourceNotFound("member", principal.MemberID.String())
		}
		return Session{}, err
	}
	if member.AccountIdentityID == nil || *member.AccountIdentityID != principal.IdentityID.String() {
		return Session{}, domainerr.InvalidSession()
	}
	account, err := s.accounts.SessionSummary(ctx, member.ID)
	if err != nil {
		return Session{}, err
	}
	if account.MemberID != member.ID {
		return Session{}, domainerr.InvalidSession()
	}
	session := Session{
		Member:            memberSummary(member, assets[member.ID]),
		PreferredLocale:   member.PreferredLocale,
		Role:              account.Role,
		Status:            account.Status,
		Metadata:          requestMetadata(ctx, query.ClientAddress),
		Onboarded:         member.Onboarded,
		AccountIdentityID: principal.IdentityID.String(),
	}
	if account.CanonicalEmail != nil {
		session.Email = &account.CanonicalEmail.Email
	}
	if !member.Onboarded {
		session.NicknameSuggestion = s.nicknameSuggestion(ctx, member)
	}
	return session, nil
}

// OwnProfile returns the authenticated Member's profile. Bio, website and
// social links are hidden unless the Member holds the Author role.
func (s *Service) OwnProfile(ctx context.Context) (Profile, error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return Profile{}, err
	}
	_, profile, err := s.memberProfileByID(ctx, principal.MemberID.String())
	if err != nil {
		return Profile{}, err
	}
	author, err := s.isGlobalAuthor(ctx)
	if err != nil {
		return Profile{}, err
	}
	if !author {
		hideExtendedMemberProfile(&profile)
	}
	return profile, nil
}

// OwnSections returns the account sections the authenticated Member may open.
func (s *Service) OwnSections(ctx context.Context) ([]Section, error) {
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
