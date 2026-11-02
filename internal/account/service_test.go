package account_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/account/accounttest"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/structured"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// fixture is the Account use cases over fakes.
type fixture struct {
	service    *account.Service
	store      *accounttest.Store
	units      *accounttest.Units
	kratos     *accounttest.Kratos
	authorizer *accounttest.Authorizer
	fence      *identitystatetest.Fence
	emails     *accounttest.Emails
	members    *accounttest.Members
	audit      *audittest.Recorder
}

func newFixture(t *testing.T, checker *authztest.Checker) fixture {
	t.Helper()
	if checker == nil {
		checker = &authztest.Checker{}
	}
	store, units := accounttest.New()
	f := fixture{
		store: store, units: units, kratos: accounttest.NewKratos(store), authorizer: accounttest.NewAuthorizer(checker),
		fence: &identitystatetest.Fence{}, emails: &accounttest.Emails{}, members: &accounttest.Members{}, audit: &audittest.Recorder{},
	}
	service, err := account.NewService(account.Deps{
		Units: units, SpiceDB: f.authorizer, Fence: f.fence, Identity: f.kratos, Emails: f.emails,
		Members: f.members, Audit: f.audit, SiteOrigin: "https://geul.test/",
	})
	require.NoError(t, err)
	f.service = service
	return f
}

// seedPair stores an active, onboarded pair in the store and in Kratos.
func (f fixture) seedPair(email string) (memberID, identityID string) {
	memberID, identityID = uuid.NewString(), uuid.NewString()
	if email == "" {
		email = identityID + "@example.test"
	}
	f.store.SeedPair(memberID, identityID, email)
	f.kratos.Put(&auth.Identity{
		ID: identityID, ExternalID: memberID, State: auth.KratosStateActive,
		Traits:              structured.Fields{"email": email},
		VerifiableAddresses: []auth.VerifiableAddress{{Value: email, Via: "email", Verified: true}},
		Credentials:         map[string]auth.Credential{"code": {Type: "code", Identifiers: []string{email}}},
	})
	return memberID, identityID
}

// principalContext authenticates memberID's onboarded pair on a public
// request, whose source IP personal-data access records need.
func principalContext(t *testing.T, memberID, identityID string) context.Context {
	t.Helper()
	return sessionContext(t, memberID, identityID, uuid.NewString(), time.Now().UTC())
}

// sessionContext authenticates memberID's onboarded pair in sessionID,
// authenticated at authenticatedAt.
func sessionContext(t *testing.T, memberID, identityID, sessionID string, authenticatedAt time.Time) context.Context {
	t.Helper()
	request, err := sharedtelemetry.NewPublicRequestContext("192.0.2.77")
	require.NoError(t, err)
	ctx := sharedtelemetry.WithActor(sharedtelemetry.WithRequestContext(t.Context(), request), sharedtelemetry.MemberActor{MemberID: memberID})
	return auth.WithUser(ctx, &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(sessionID), Authenticated: true, Onboarded: true,
		AuthenticatedAt: authenticatedAt,
	})
}

func TestNewServiceRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	_, err := account.NewService(account.Deps{})
	require.EqualError(t, err, "account dependencies are required: Audit, Emails, Fence, Identity, Members, SpiceDB, Units")

	f := newFixture(t, authztest.AllowAll())
	require.NotNil(t, f.service)
}

var errInjected = errors.New("injected failure")
