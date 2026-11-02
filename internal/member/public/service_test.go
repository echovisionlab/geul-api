package public_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/member/membertest"
	"github.com/echovisionlab/geul-api/internal/member/public"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const tokenSecret = "public-newsletter-token-test-secret-at-least-32-bytes"

type fixture struct {
	store   *fakeStore
	units   *mutationtest.UnitOfWork[public.Store]
	checker *authztest.Checker
	spiceDB *membertest.Authorizer
	audit   *audittest.Recorder
	service *public.Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	store, units := newFakeStore()
	f := &fixture{store: store, units: units, checker: authztest.AllowAll(), audit: &audittest.Recorder{}}
	f.spiceDB = membertest.NewAuthorizer(f.checker)
	f.service = f.newService(t, f.audit)
	return f
}

func (f *fixture) newService(t *testing.T, audit *audittest.Recorder) *public.Service {
	t.Helper()
	service, err := public.NewService(public.Deps{Units: f.units, SpiceDB: f.spiceDB, Fence: &identitystatetest.Fence{}, Audit: audit, TokenSecret: tokenSecret})
	require.NoError(t, err)
	return service
}

func (f *fixture) seed(nickname string) (string, string) {
	memberID, identityID := uuid.NewString(), uuid.NewString()
	f.store.members[memberID] = member.Member{ID: memberID, AccountIdentityID: &identityID, Nickname: nickname, Onboarded: true, SocialLinks: map[string]string{}}
	return memberID, identityID
}

func unsubscribeToken(identityID string) string {
	return crypto.GenerateSignedToken(member.NewsletterUnsubscribeTokenID(identityID), crypto.PurposeUnsubscribe, time.Time{}, tokenSecret)
}

func TestNewServiceRequiresEveryCollaborator(t *testing.T) {
	_, err := public.NewService(public.Deps{})
	require.EqualError(t, err, "public member dependencies are required: Audit, Fence, SpiceDB, TokenSecret, Units")
}

// The token is the only authority: an invalid one answers before any
// read.
func TestNewsletterUnsubscribeRejectsAnInvalidTokenBeforeTheStore(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	err := f.service.UnsubscribeNewsletter(t.Context(), public.UnsubscribeNewsletterCommand{Token: "invalid"})
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	require.ErrorContains(t, err, "token: must be a valid unsubscribe token")
	require.Empty(t, f.units.Steps())
}

func TestNewsletterTokenUnsubscribeAuditsOnlyTheCommittedTransition(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	memberID, identityID := f.seed("subscriber")
	f.store.newsletter[identityID] = time.Now()
	command := public.UnsubscribeNewsletterCommand{Token: unsubscribeToken(identityID)}

	require.NoError(t, f.service.UnsubscribeNewsletter(t.Context(), command))
	require.NoError(t, f.service.UnsubscribeNewsletter(t.Context(), command))

	records := f.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditAccountUpdated, records[0].Action)
	require.Equal(t, sharedtelemetry.ActorKindMember, records[0].Kind)
	require.Equal(t, memberID, records[0].MemberID)
	require.Equal(t, memberID, records[0].TargetID)
	require.Equal(t, []string{"newsletter_subscription"}, records[0].ChangedFields)
	require.Equal(t, sharedtelemetry.AuditStateSubscribed, records[0].PreviousState)
	require.Equal(t, sharedtelemetry.AuditStateUnsubscribed, records[0].NewState)
	require.NotContains(t, f.store.newsletter, identityID)
}

func TestNewsletterTokenUnsubscribeAuditFailureRollsBack(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, identityID := f.seed("subscriber")
	f.store.newsletter[identityID] = time.Now()

	err := f.newService(t, &audittest.Recorder{Err: errAuditDown}).UnsubscribeNewsletter(t.Context(), public.UnsubscribeNewsletterCommand{Token: unsubscribeToken(identityID)})

	require.Equal(t, connect.CodeInternal, errs.Wrap(err).Code())
	require.Contains(t, f.store.newsletter, identityID)
	require.Equal(t, mutation.StepRollback, f.units.Steps()[len(f.units.Steps())-1])
}

func TestNewsletterTokenUnsubscribeOfAnIdentityWithoutMemberChangesNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	identityID := uuid.NewString()
	f.store.newsletter[identityID] = time.Now()

	err := f.service.UnsubscribeNewsletter(t.Context(), public.UnsubscribeNewsletterCommand{Token: unsubscribeToken(identityID)})

	require.Equal(t, connect.CodeInternal, errs.Wrap(err).Code())
	require.Contains(t, f.store.newsletter, identityID)
	require.Empty(t, f.audit.Records())
}

func TestListAuthorsReturnsOnlySelectedEffectiveAuthorsInRequestOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	authorID, authorIdentity := f.seed("Selected Author")
	adminID, adminIdentity := f.seed("Selected Admin")
	userID, _ := f.seed("Ordinary User")
	bio := "author biography"
	f.store.authors = []public.AuthorRow{
		{MemberID: authorID, AccountIdentityID: &authorIdentity, PostCount: 1, Bio: &bio},
		{MemberID: adminID, AccountIdentityID: &adminIdentity},
		{MemberID: userID},
	}
	f.spiceDB.Subjects[membertest.Authors()] = []string{authorIdentity, adminIdentity}

	authors, err := f.service.ListAuthors(t.Context(), public.ListAuthorsQuery{MemberIDs: []string{adminID, userID, authorID}})
	require.NoError(t, err)
	require.Len(t, authors, 2)
	require.Equal(t, adminID, authors[0].Member.ID)
	require.Zero(t, authors[0].PostCount)
	require.Equal(t, authorID, authors[1].Member.ID)
	require.Equal(t, int32(1), authors[1].PostCount)
	require.Equal(t, &bio, authors[1].Bio)

	_, err = f.service.ListAuthors(t.Context(), public.ListAuthorsQuery{MemberIDs: []string{authorID, authorID}})
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	_, err = f.service.ListAuthors(t.Context(), public.ListAuthorsQuery{MemberIDs: []string{"not-a-uuid"}})
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
}

func TestPublicProfileAndTopAuthorsShowExtendedFieldsOnlyForAuthors(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	memberID, identityID := f.seed("Role Profile")
	value := f.store.members[memberID]
	bio, website := "stored biography", "https://profile.example.test"
	value.Bio, value.Website, value.SocialLinks = &bio, &website, map[string]string{"homepage": website}
	f.store.members[memberID] = value
	f.store.authors = []public.AuthorRow{{MemberID: memberID, AccountIdentityID: &identityID, PostCount: 3, Bio: &bio}}
	f.checker.Deny(policyv1.Platform.IsAuthor())

	profile, err := f.service.GetPublicMember(t.Context(), public.GetPublicMemberQuery{MemberID: memberID})
	require.NoError(t, err)
	require.Nil(t, profile.Bio)
	require.Equal(t, map[string]string{}, profile.SocialLinks)
	authors, err := f.service.ListAuthors(t.Context(), public.ListAuthorsQuery{})
	require.NoError(t, err)
	require.Nil(t, authors[0].Bio)

	f.checker.Allow(policyv1.Platform.IsAuthor())
	f.spiceDB.Subjects[membertest.Authors()] = []string{identityID}
	profile, err = f.service.GetPublicMember(t.Context(), public.GetPublicMemberQuery{MemberID: memberID})
	require.NoError(t, err)
	require.Equal(t, &bio, profile.Bio)
	require.Equal(t, map[string]string{"homepage": website}, profile.SocialLinks)
	authors, err = f.service.ListAuthors(t.Context(), public.ListAuthorsQuery{})
	require.NoError(t, err)
	require.Equal(t, &bio, authors[0].Bio)
	require.Equal(t, int32(3), authors[0].PostCount)
}

func TestPublicProfileOfADeletedOrUnonboardedMember(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	formerID, _ := f.seed("Former")
	former := f.store.members[formerID]
	deletedAt := time.Now()
	bio := "private"
	former.DeletedAt, former.Bio = &deletedAt, &bio
	f.store.members[formerID] = former
	f.store.avatars[formerID] = &memberref.Asset{ID: uuid.NewString()}
	pendingID, _ := f.seed("pending")
	pending := f.store.members[pendingID]
	pending.Onboarded = false
	f.store.members[pendingID] = pending

	profile, err := f.service.GetPublicMember(t.Context(), public.GetPublicMemberQuery{MemberID: formerID})
	require.NoError(t, err)
	require.True(t, profile.Summary.Deleted)
	require.Nil(t, profile.Summary.Avatar)
	require.Nil(t, profile.Bio)
	require.Equal(t, "Former", profile.Summary.Nickname)

	missing, err := f.service.GetPublicMember(t.Context(), public.GetPublicMemberQuery{MemberID: pendingID})
	require.NoError(t, err)
	require.Nil(t, missing)
	_, err = f.service.GetPublicMember(t.Context(), public.GetPublicMemberQuery{MemberID: "nope"})
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	require.Empty(t, f.checker.Checks())
}
