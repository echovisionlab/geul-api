package account_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/account/accounttest"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Security mutations need a recent authentication of the current session;
// the session's age comes from the principal, before any read.
func TestAccountSecurityMutationsRejectStaleCurrentSession(t *testing.T) {
	t.Parallel()
	f := newFixture(t, authztest.AllowAll())
	memberID, identityID := f.seedPair("")
	email := identityID + "@example.test"
	handler := account.NewConnectHandler(f.service)
	ctx := sessionContext(t, memberID, identityID, uuid.NewString(), time.Now().UTC().Add(-3*time.Hour-time.Second))

	for name, call := range map[string]func() error{
		"set canonical email": func() error {
			_, err := handler.SetMyCanonicalEmail(ctx, connect.NewRequest(&managev1.SetMyCanonicalEmailRequest{Email: email}))
			return err
		},
		"request email change": func() error {
			_, err := handler.RequestEmailChange(ctx, connect.NewRequest(&managev1.RequestEmailChangeRequest{NewEmail: "new-" + email}))
			return err
		},
		"revoke session": func() error {
			_, err := handler.RevokeMySession(ctx, connect.NewRequest(&managev1.RevokeMySessionRequest{SessionId: uuid.NewString()}))
			return err
		},
		"revoke other sessions": func() error {
			_, err := handler.RevokeMyOtherSessions(ctx, connect.NewRequest(&managev1.RevokeMyOtherSessionsRequest{}))
			return err
		},
		"request account deletion": func() error {
			_, err := handler.RequestAccountDeletion(ctx, connect.NewRequest(&managev1.RequestAccountDeletionRequest{}))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := call()
			require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			require.ErrorContains(t, err, "reauthenticate")
		})
	}
	require.Empty(t, f.kratos.Calls())
	require.Empty(t, f.units.Steps())
}

func TestRevokeOtherSessionsPreservesCurrentSession(t *testing.T) {
	t.Parallel()
	f := newFixture(t, authztest.AllowAll())
	memberID, identityID := f.seedPair("")
	now := time.Now().UTC()
	current, other := uuid.NewString(), uuid.NewString()
	f.store.UpdateIdentity(identityID, func(identity *accounttest.Identity) {
		for _, sessionID := range []string{current, other} {
			identity.LiveSessions = append(identity.LiveSessions, account.Session{ID: sessionID, Active: true, AuthenticatedAt: now, ExpiresAt: now.Add(time.Hour)})
		}
	})

	security, err := f.service.RevokeMyOtherSessions(sessionContext(t, memberID, identityID, current, now))

	require.NoError(t, err)
	require.Equal(t, []string{"DeleteSession:" + other}, f.kratos.Writes())
	require.Len(t, security.Sessions, 1)
	require.Equal(t, current, security.Sessions[0].ID)
	records := f.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, []string{other}, records[0].SessionIDs)
}

func TestAccountNewsletterSubscriptionAuditRecordsOnlyCommittedTransitions(t *testing.T) {
	t.Parallel()
	checker := &authztest.Checker{}
	f := newFixture(t, checker)
	adminID, adminIdentityID := f.seedPair("")
	memberID, identityID := f.seedPair("")
	grantRole(f, checker, adminIdentityID, policyv1.Role.Admin())
	grantRole(f, checker, identityID, policyv1.Role.User())
	handler := account.NewConnectHandler(f.service)
	memberCtx := principalContext(t, memberID, identityID)
	adminCtx := principalContext(t, adminID, adminIdentityID)

	request := connect.NewRequest(&managev1.SetMyNewsletterSubscriptionRequest{Subscribed: true})
	for range 2 {
		_, err := handler.SetMyNewsletterSubscription(memberCtx, request)
		require.NoError(t, err)
	}
	require.True(t, f.store.Newsletter(identityID))
	for range 2 {
		_, err := handler.UnsubscribeAccountFromNewsletter(adminCtx, connect.NewRequest(&managev1.UnsubscribeAccountFromNewsletterRequest{MemberId: memberID}))
		require.NoError(t, err)
	}
	require.False(t, f.store.Newsletter(identityID))

	records := f.audit.Records()
	require.Len(t, records, 2)
	require.Equal(t, memberID, records[0].MemberID)
	require.Equal(t, adminID, records[1].MemberID)
	for index, transition := range [][2]sharedtelemetry.AuditState{{"unsubscribed", "subscribed"}, {"subscribed", "unsubscribed"}} {
		require.Equal(t, sharedtelemetry.AuditAccountUpdated, records[index].Action)
		require.Equal(t, "account", records[index].TargetType)
		require.Equal(t, memberID, records[index].TargetID)
		require.Equal(t, []string{"newsletter_subscription"}, records[index].ChangedFields)
		require.Equal(t, transition[0], records[index].PreviousState)
		require.Equal(t, transition[1], records[index].NewState)
	}
}

func TestAccountNewsletterSubscriptionAuditFailureRollsBack(t *testing.T) {
	t.Parallel()
	f := newFixture(t, authztest.AllowAll())
	memberID, identityID := f.seedPair("")
	f.audit.Err = errInjected

	_, err := account.NewConnectHandler(f.service).SetMyNewsletterSubscription(
		principalContext(t, memberID, identityID),
		connect.NewRequest(&managev1.SetMyNewsletterSubscriptionRequest{Subscribed: true}),
	)

	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	require.False(t, f.store.Newsletter(identityID))
}

func TestRevokeMySessionEndsOnlyAnOwnedOtherSession(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	memberID, identityID := f.seedPair("")
	now := time.Now().UTC()
	current, other := uuid.NewString(), uuid.NewString()
	f.store.UpdateIdentity(identityID, func(identity *accounttest.Identity) {
		for _, sessionID := range []string{current, other} {
			identity.LiveSessions = append(identity.LiveSessions, account.Session{ID: sessionID, Active: true, AuthenticatedAt: now, ExpiresAt: now.Add(time.Hour)})
		}
	})
	ctx := sessionContext(t, memberID, identityID, current, now)

	_, err := f.service.RevokeMySession(ctx, account.RevokeMySessionCommand{SessionID: current})
	require.Equal(t, connect.CodeFailedPrecondition, errsCode(err))
	_, err = f.service.RevokeMySession(ctx, account.RevokeMySessionCommand{SessionID: uuid.NewString()})
	require.Equal(t, connect.CodeNotFound, errsCode(err))
	require.Empty(t, f.kratos.Writes())

	security, err := f.service.RevokeMySession(ctx, account.RevokeMySessionCommand{SessionID: other})
	require.NoError(t, err)
	require.Equal(t, []string{"DeleteSession:" + other}, f.kratos.Writes())
	require.Len(t, security.Sessions, 1)
	records := f.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AccountSessionScopeOne, records[0].SessionScope)
	require.Equal(t, []string{other}, records[0].SessionIDs)
}

// An address a trusted provider proves becomes the canonical email in
// Kratos and the Member projection, and the change is recorded once.
func TestSetMyCanonicalEmailMovesToAProvenAddress(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	memberID, identityID := f.seedPair("")
	previous, next := identityID+"@example.test", "next-"+identityID+"@example.test"
	f.kratos.Update(identityID, func(identity *auth.Identity) {
		identity.Credentials["oidc"] = googleCredential(next)
	})
	sessionID := uuid.NewString()
	now := time.Now().UTC()
	f.store.UpdateIdentity(identityID, func(identity *accounttest.Identity) {
		identity.LiveSessions = []account.Session{{ID: sessionID, Active: true, AuthenticatedAt: now, ExpiresAt: now.Add(time.Hour)}}
	})

	security, err := f.service.SetMyCanonicalEmail(sessionContext(t, memberID, identityID, sessionID, now), account.SetMyCanonicalEmailCommand{Email: next})

	require.NoError(t, err)
	require.Equal(t, next, security.CanonicalEmail)
	require.Equal(t, next, f.kratos.Identity(identityID).CurrentEmail())
	member, _ := f.store.Member(memberID)
	require.Equal(t, next, member.PrimaryEmail)
	require.Empty(t, f.store.EmailChanges())
	records := f.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, previous, records[0].PreviousEmail)
	require.Equal(t, next, records[0].NewEmail)
}

func errsCode(err error) connect.Code { return errs.Wrap(err).Code() }
