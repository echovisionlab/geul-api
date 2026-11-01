package member_test

import (
	"errors"
	"testing"
	"time"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/member/membertest"
	"github.com/echovisionlab/geul-api/internal/structured"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestAdminMemberReadsRecordPersonalDataAccessScopes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	targetID, targetIdentityID := f.seedMember("target")
	subscribedAt := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	f.store.SeedNewsletter(targetIdentityID, subscribedAt)
	tagID := uuid.NewString()
	f.store.SeedTag(member.Tag{ID: tagID, Name: "patrons"})
	f.store.SeedMemberTags(targetID, tagID)

	view, err := f.service.GetAdminMember(f.ctx(t), member.GetAdminMemberQuery{MemberID: targetID})
	require.NoError(t, err)
	require.Equal(t, targetID, view.Profile.Summary.ID)
	require.Equal(t, []string{tagID}, view.TagIDs)
	require.Equal(t, member.NewsletterState{Subscribed: true, SubscribedAt: &subscribedAt}, view.Newsletter)
	require.NotNil(t, view.AccountDetails)

	page, err := f.service.ListAdminMembers(f.ctx(t), member.ListAdminMembersQuery{Limit: 500})
	require.NoError(t, err)
	require.Len(t, page.Members, 2)
	require.Equal(t, member.Pagination{Total: 2, Limit: 100, Offset: 0}, page.Pagination)

	accesses := f.audit.AccessRecords()
	require.Len(t, accesses, 2)
	require.Equal(t, "member", accesses[0].SubjectType)
	require.Equal(t, targetID, accesses[0].SubjectID)
	require.Equal(t, "192.0.2.77", accesses[0].SourceIP)
	require.Equal(t, f.memberID, accesses[0].MemberID)
	require.Equal(t, "member_collection", accesses[1].SubjectType)
	require.Equal(t, "1", accesses[1].SubjectID)
	for _, access := range accesses {
		require.Equal(t, sharedtelemetry.SecurityPersonalDataAccessed, access.Action)
		require.Equal(t, "member_administration", access.DataCategory)
		require.Equal(t, sharedtelemetry.PersonalDataAccessKind("read"), access.AccessKind)
	}
}

func TestAdminMemberReadsFailClosedWhenTheAccessRecordFails(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	failing := f.newService(t, &audittest.Recorder{Err: errAuditUnavailable})

	_, err := failing.GetAdminMember(f.ctx(t), member.GetAdminMemberQuery{MemberID: f.memberID})
	require.Equal(t, connect.CodeUnavailable, errs.Wrap(err).Code())
	_, err = failing.ListAdminMembers(f.ctx(t), member.ListAdminMembersQuery{})
	require.Equal(t, connect.CodeUnavailable, errs.Wrap(err).Code())
}

func TestAdminMemberReadsKeepATombstoneWithoutAccountOrDetails(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	formerID, _ := f.seedMember("former")
	former, _ := f.store.Member(formerID)
	deletedAt := time.Now().UTC()
	former.DeletedAt, former.AccountIdentityID = &deletedAt, nil
	bio := "private"
	former.Bio = &bio
	f.store.SeedMember(former)
	f.accounts.summaries[formerID] = nil
	f.accounts.err = nil

	view, err := f.service.GetAdminMember(f.ctx(t), member.GetAdminMemberQuery{MemberID: formerID})
	require.NoError(t, err)
	require.True(t, view.Profile.Summary.Deleted)
	require.Nil(t, view.Profile.Bio)
	require.Nil(t, view.AccountDetails)

	_, err = f.service.GetAdminMember(f.ctx(t), member.GetAdminMemberQuery{MemberID: "not-a-uuid"})
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	_, err = f.service.GetAdminMember(f.ctx(t), member.GetAdminMemberQuery{MemberID: uuid.NewString()})
	require.Equal(t, connect.CodeNotFound, errs.Wrap(err).Code())
}

func TestSearchMembersCanRestrictCandidatesToEffectiveAuthors(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	authorID, authorIdentityID := f.seedMember("Author Alpha")
	f.seedMember("Reader Alpha")
	f.spiceDB.Subjects[membertest.Authors()] = []string{authorIdentityID}

	all, err := f.service.SearchMembers(f.ctx(t), member.SearchMembersQuery{Query: " alpha "})
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.Equal(t, "Author Alpha", all[0].Nickname)

	authors, err := f.service.SearchMembers(f.ctx(t), member.SearchMembersQuery{Query: "alpha", EffectiveAuthorsOnly: true})
	require.NoError(t, err)
	require.Equal(t, []member.Summary{{ID: authorID, Nickname: "Author Alpha"}}, authors)

	f.spiceDB.Subjects[membertest.Authors()] = nil
	none, err := f.service.SearchMembers(f.ctx(t), member.SearchMembersQuery{EffectiveAuthorsOnly: true})
	require.NoError(t, err)
	require.Nil(t, none)
}

func TestCurrentSessionUsesTheLeanProjectionWithoutSectionAuthorization(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.accounts.summaries[f.memberID] = &member.AccountSummary{
		MemberID: f.memberID, Role: policyv1.AuthorizationRole_AUTHOR, Status: member.AdminStatusActive,
		CanonicalEmail: &member.CanonicalEmail{Email: "member@example.test"},
	}
	asset := &member.Asset{ID: uuid.NewString()}
	f.store.SeedAvatar(f.memberID, asset)

	session, err := f.service.CurrentSession(f.ctx(t), member.CurrentSessionQuery{ClientAddress: "198.51.100.7"})

	require.NoError(t, err)
	require.Equal(t, member.Summary{ID: f.memberID, Nickname: "principal", Avatar: asset}, session.Member)
	require.Equal(t, policyv1.AuthorizationRole_AUTHOR, session.Role)
	require.Equal(t, "member@example.test", *session.Email)
	require.Equal(t, "198.51.100.7", session.Metadata.IPAddress)
	require.True(t, session.Onboarded)
	require.Nil(t, session.NicknameSuggestion)
	require.Empty(t, f.checker.Checks())

	other, _ := f.seedMember("other")
	f.accounts.summaries[f.memberID].MemberID = other
	_, err = f.service.CurrentSession(f.ctx(t), member.CurrentSessionQuery{})
	require.Equal(t, connect.CodeUnauthenticated, errs.Wrap(err).Code())
}

func TestCurrentSessionRejectsAnotherIdentityAndAMissingMember(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.service.CurrentSession(principalContext(t, uuid.NewString(), f.memberID, true), member.CurrentSessionQuery{})
	require.Equal(t, connect.CodeUnauthenticated, errs.Wrap(err).Code())
	_, err = f.service.CurrentSession(principalContext(t, f.identityID, uuid.NewString(), true), member.CurrentSessionQuery{})
	require.Equal(t, connect.CodeNotFound, errs.Wrap(err).Code())
}

func TestOwnSectionsFollowAuthorityAndManagedResources(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.checker.Deny(policyv1.Platform.IsAuthor())
	f.spiceDB.Resources["work"] = []string{uuid.NewString()}

	sections, err := f.service.OwnSections(f.ctx(t))
	require.NoError(t, err)
	require.Equal(t, []member.Section{member.SectionProfile, member.SectionSecurity, member.SectionSettings, member.SectionWorks}, sections)

	none, err := f.service.OwnSections(principalContext(t, f.identityID, f.memberID, false))
	require.NoError(t, err)
	require.Nil(t, none)

	f.spiceDB.LookupErr = errors.New("spicedb down")
	_, err = f.service.OwnSections(f.ctx(t))
	require.Equal(t, connect.CodeUnavailable, errs.Wrap(err).Code())
}

func TestSettingsReadTheLatestConsentAndCanonicalEmail(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.accounts.summaries[f.memberID] = &member.AccountSummary{MemberID: f.memberID, CanonicalEmail: &member.CanonicalEmail{Email: "member@example.test", Verified: true}}

	settings, err := f.service.GetSettings(f.ctx(t))
	require.NoError(t, err)
	require.Nil(t, settings.CookieConsent)
	require.Equal(t, member.NewsletterState{}, settings.Newsletter)
	require.Equal(t, &member.CanonicalEmail{Email: "member@example.test", Verified: true}, settings.CanonicalEmail)
}

func TestNewsletterSubscriptionUsesExactIdentityAndKeepsFirstOptInTime(t *testing.T) {
	t.Parallel()
	store, _ := membertest.New()
	identityID := uuid.NewString()

	changed, err := member.MutateNewsletterSubscription(t.Context(), store, identityID, true)
	require.NoError(t, err)
	require.True(t, changed)
	first, err := member.SubscriptionState(t.Context(), store, identityID)
	require.NoError(t, err)
	require.True(t, first.Subscribed)
	require.NotNil(t, first.SubscribedAt)

	changed, err = member.MutateNewsletterSubscription(t.Context(), store, identityID, true)
	require.NoError(t, err)
	require.False(t, changed)
	second, err := member.SubscriptionState(t.Context(), store, identityID)
	require.NoError(t, err)
	require.Equal(t, *first.SubscribedAt, *second.SubscribedAt)

	for _, want := range []bool{true, false} {
		changed, err = member.MutateNewsletterSubscription(t.Context(), store, identityID, false)
		require.NoError(t, err)
		require.Equal(t, want, changed)
	}
	removed, err := member.SubscriptionState(t.Context(), store, identityID)
	require.NoError(t, err)
	require.Equal(t, member.NewsletterState{}, removed)

	_, err = member.MutateNewsletterSubscription(t.Context(), store, "not-a-uuid", true)
	require.Error(t, err)
}

func TestNicknameSuggestionComesFromTheIdentityClaims(t *testing.T) {
	t.Parallel()
	f := pendingFixture(t)
	f.identity.put(&auth.Identity{ID: f.identityID, ExternalID: f.memberID, State: auth.KratosStateActive, Credentials: map[string]auth.Credential{"oidc": {
		Type: "oidc",
		Config: structured.Fields{"providers": structured.Values{structured.Fields{
			"provider": "github", "subject": "subject", "initial_id_token_claims": structured.Fields{"name": "Claimed Name"},
		}}},
	}}})

	session, err := f.service.CurrentSession(principalContext(t, f.identityID, f.memberID, false), member.CurrentSessionQuery{})
	require.NoError(t, err)
	require.False(t, session.Onboarded)
	if session.NicknameSuggestion != nil {
		require.Equal(t, "Claimed Name", *session.NicknameSuggestion)
	}
}
