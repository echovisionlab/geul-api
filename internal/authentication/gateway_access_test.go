package authentication_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	accountadapter "github.com/echovisionlab/geul-api/internal/adapters/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/authentication/authenticationtest"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// gateway is the gateway authorization of one test on fakes, with one live
// session of an onboarded Member.
type gateway struct {
	service    *authentication.Service
	checker    *authztest.Checker
	sessions   *authenticationtest.Sessions
	identityID string
	sessionID  string
}

func newGateway(t *testing.T) gateway {
	t.Helper()
	_, units := authenticationtest.New()
	g := gateway{
		checker: &authztest.Checker{}, sessions: authenticationtest.NewSessions(),
		identityID: uuid.NewString(), sessionID: uuid.NewString(),
	}
	g.sessions.Store(g.sessionID, &auth.UserInfo{
		IdentityID: auth.IdentityID(g.identityID), MemberID: auth.MemberID(uuid.NewString()),
		SessionID: auth.SessionID(g.sessionID), Authenticated: true, Onboarded: true,
	}, false)
	var err error
	g.service, err = authentication.NewService(authentication.Deps{
		Units: units, SpiceDB: authenticationtest.NewAuthorizer(g.checker), Fence: &identitystatetest.Fence{},
		Audit: &audittest.Recorder{}, Roles: accountadapter.AccountDirectRoleTransition{}, Sessions: g.sessions,
	})
	require.NoError(t, err)
	return g
}

func (g gateway) query(role policyv1.AuthorizationRole) authentication.GatewayAccessQuery {
	return authentication.GatewayAccessQuery{AccountIdentityID: g.identityID, SessionID: g.sessionID, Role: role}
}

func requireGatewayAnswer(t *testing.T, err error, code connect.Code, message string) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, code, errs.Wrap(err).Code())
	require.Equal(t, message, errs.Wrap(err).Message())
}

func TestGatewayAccessAsksSpiceDBTheRoleOfTheSessionsIdentity(t *testing.T) {
	g := newGateway(t)
	g.checker.For(g.identityID).Allow(policyv1.Platform.IsAuthor())

	require.NoError(t, g.service.AuthorizeGatewayAccess(t.Context(), g.query(policyv1.AuthorizationRole_AUTHOR)))
	requireGatewayAnswer(t, g.service.AuthorizeGatewayAccess(t.Context(), g.query(policyv1.AuthorizationRole_ADMIN)),
		connect.CodePermissionDenied, "ADMIN access required")
	isAuthor, err := policyv1.Platform.IsAuthor()
	require.NoError(t, err)
	isAdmin, err := policyv1.Platform.IsAdmin()
	require.NoError(t, err)
	require.Equal(t, []authztest.Check{
		authztest.Of(g.identityID, isAuthor, nil), authztest.Of(g.identityID, isAdmin, nil),
	}, g.checker.Checks())
}

func TestGatewayAccessRejectsASessionThatCannotActAsAnInvalidSession(t *testing.T) {
	for name, principal := range map[string]func(g gateway) *auth.UserInfo{
		"another Identity's session": func(gateway) *auth.UserInfo {
			return &auth.UserInfo{IdentityID: auth.IdentityID(uuid.NewString()), Authenticated: true, Onboarded: true}
		},
		"a banned Identity": func(g gateway) *auth.UserInfo {
			return &auth.UserInfo{IdentityID: auth.IdentityID(g.identityID), Authenticated: true, Onboarded: true, Banned: true}
		},
		"a Member not onboarded": func(g gateway) *auth.UserInfo {
			return &auth.UserInfo{IdentityID: auth.IdentityID(g.identityID), Authenticated: true}
		},
	} {
		t.Run(name, func(t *testing.T) {
			g := newGateway(t)
			g.sessions.Store(g.sessionID, principal(g), false)

			requireGatewayAnswer(t, g.service.AuthorizeGatewayAccess(t.Context(), g.query(policyv1.AuthorizationRole_AUTHOR)),
				connect.CodeUnauthenticated, "invalid or expired session")
			require.Empty(t, g.checker.Checks())
		})
	}
	t.Run("an unknown session", func(t *testing.T) {
		g := newGateway(t)
		query := g.query(policyv1.AuthorizationRole_AUTHOR)
		query.SessionID = uuid.NewString()

		requireGatewayAnswer(t, g.service.AuthorizeGatewayAccess(t.Context(), query),
			connect.CodeUnauthenticated, "invalid or expired session")
		require.Empty(t, g.checker.Checks())
	})
}

func TestGatewayAccessAnswersAnUnsupportedRoleAfterTheSession(t *testing.T) {
	for _, role := range []policyv1.AuthorizationRole{policyv1.AuthorizationRole_USER, policyv1.AuthorizationRole_ANON, policyv1.AuthorizationRole_UNSPECIFIED} {
		t.Run(role.String(), func(t *testing.T) {
			g := newGateway(t)

			requireGatewayAnswer(t, g.service.AuthorizeGatewayAccess(t.Context(), g.query(role)),
				connect.CodeInvalidArgument, "role: unsupported gateway authorization role")
			require.Empty(t, g.checker.Checks())
		})
	}
}

func TestGatewayAccessAnswersAnUnreachableDependencyAsUnavailable(t *testing.T) {
	down := errors.New("connection refused")
	t.Run("principal database", func(t *testing.T) {
		g := newGateway(t)
		g.sessions.Fail(down)

		err := g.service.AuthorizeGatewayAccess(t.Context(), g.query(policyv1.AuthorizationRole_AUTHOR))

		requireGatewayAnswer(t, err, connect.CodeUnavailable, "principal database unavailable")
		require.ErrorIs(t, err, down)
	})
	t.Run("SpiceDB", func(t *testing.T) {
		g := newGateway(t)
		g.checker.Fail(down)

		err := g.service.AuthorizeGatewayAccess(t.Context(), g.query(policyv1.AuthorizationRole_ADMIN))

		requireGatewayAnswer(t, err, connect.CodeUnavailable, "SpiceDB unavailable")
		require.ErrorIs(t, err, down)
	})
}
