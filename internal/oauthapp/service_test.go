package oauthapp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/oauthapp"
	"github.com/echovisionlab/geul-api/internal/oauthapp/oauthapptest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const mcpResource = "https://www.example.test/mcp"

type fixture struct {
	store       *oauthapptest.Store
	hydra       *oauthapptest.Hydra
	eligibility *oauthapptest.Eligibility
	checker     *authztest.Checker
	audit       *audittest.Recorder
	service     *oauthapp.Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	store, units := oauthapptest.New()
	f := &fixture{
		store: store, hydra: oauthapptest.NewHydra(), checker: authztest.AllowAll(), audit: &audittest.Recorder{},
		eligibility: &oauthapptest.Eligibility{Criteria: map[string]oauthapp.CriteriaSummary{}, Inactive: map[string]bool{}, Matches: map[string][]string{}},
	}
	service, err := oauthapp.NewService(oauthapp.Deps{
		Units: units, SpiceDB: f.checker, Fence: &identitystatetest.Fence{}, Audit: f.audit,
		Clients: f.hydra, Eligibility: f.eligibility, MCPResourceURL: mcpResource,
	})
	require.NoError(t, err)
	f.service = service
	return f
}

type member struct {
	identityID string
	memberID   string
	ctx        context.Context
}

func newMember(t *testing.T) member {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.77")
	require.NoError(t, err)
	identityID, memberID := uuid.NewString(), uuid.NewString()
	ctx := auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), requestContext), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
	return member{identityID: identityID, memberID: memberID, ctx: ctx}
}

func settings(access oauthapp.Access, method oauthapp.AuthMethod, criteriaIDs ...string) oauthapp.Settings {
	return oauthapp.Settings{
		Name: "Monitor", Access: access, MemberCriteriaIDs: criteriaIDs, RedirectURIs: []string{"https://monitor.example/callback"},
		Scopes: []oauthapp.Scope{oauthapp.ScopeEmail}, AuthMethod: method,
		AccessTokenLifespan: 15 * time.Minute, IDTokenLifespan: 15 * time.Minute,
	}
}

func (f *fixture) create(t *testing.T, admin member, clientID string, settings oauthapp.Settings) oauthapp.SavedApp {
	t.Helper()
	saved, err := f.service.CreateSignInApp(admin.ctx, oauthapp.CreateCommand{ClientID: clientID, Settings: settings})
	require.NoError(t, err)
	return saved
}

func TestCreateSignInAppRegistersAClientWithoutRefreshTokens(t *testing.T) {
	f := newFixture(t)
	admin := newMember(t)

	saved := f.create(t, admin, "monitor", settings(oauthapp.AccessAdmins, oauthapp.AuthMethodClientSecretBasic))

	require.NotNil(t, saved.Secret)
	require.Equal(t, *saved.Secret, f.hydra.Secrets["monitor"])
	client, ok := f.hydra.Client("monitor")
	require.True(t, ok)
	require.Equal(t, []string{"authorization_code"}, client.GrantTypes)
	require.Equal(t, "openid email", client.Scope)
	require.Empty(t, client.Audience)
	require.Equal(t, oauthapp.AccessAdmins, saved.App.Settings.Access)
	require.Equal(t, []sharedtelemetry.AuditAction{sharedtelemetry.AuditOAuthSignInAppCreated}, actions(f.audit))

	public := f.create(t, admin, "", settings(oauthapp.AccessMembers, oauthapp.AuthMethodNone))
	require.Nil(t, public.Secret, "a public app has no secret")
	_, err := uuid.Parse(public.App.ClientID)
	require.NoError(t, err, "an empty client ID is generated")
}

func TestCreateSignInAppRemovesTheClientWhenTheTransactionFails(t *testing.T) {
	f := newFixture(t)
	f.audit.Err = errors.New("audit unavailable")

	_, err := f.service.CreateSignInApp(newMember(t).ctx, oauthapp.CreateCommand{
		ClientID: "monitor", Settings: settings(oauthapp.AccessAdmins, oauthapp.AuthMethodClientSecretBasic),
	})

	require.Error(t, err)
	_, registered := f.hydra.Client("monitor")
	require.False(t, registered, "the Hydra client of an app that was not committed is removed")
	_, stored := f.store.Record("monitor")
	require.False(t, stored)
}

func TestCreateSignInAppRejectsCriteriaThatCannotAdmitMembers(t *testing.T) {
	f := newFixture(t)
	archived := uuid.NewString()
	f.eligibility.Criteria[archived] = oauthapp.CriteriaSummary{ID: archived, Name: "Old", Eligible: false}

	_, err := f.service.CreateSignInApp(newMember(t).ctx, oauthapp.CreateCommand{
		ClientID: "monitor", Settings: settings(oauthapp.AccessMemberCriteria, oauthapp.AuthMethodNone, archived),
	})

	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	_, registered := f.hydra.Client("monitor")
	require.False(t, registered)
}

func TestUpdateSignInAppRequiresTheRevisionItRead(t *testing.T) {
	f := newFixture(t)
	admin := newMember(t)
	saved := f.create(t, admin, "monitor", settings(oauthapp.AccessAdmins, oauthapp.AuthMethodNone))

	_, err := f.service.UpdateSignInApp(admin.ctx, oauthapp.UpdateCommand{
		ClientID: "monitor", ExpectedRevision: uuid.NewString(), Settings: settings(oauthapp.AccessMembers, oauthapp.AuthMethodNone),
	})
	require.Equal(t, connect.CodeAborted, errs.Wrap(err).Code())

	renamed := settings(oauthapp.AccessMembers, oauthapp.AuthMethodClientSecretPost)
	renamed.Name = "Grafana"
	updated, err := f.service.UpdateSignInApp(admin.ctx, oauthapp.UpdateCommand{
		ClientID: "monitor", ExpectedRevision: saved.App.Revision, Settings: renamed,
	})
	require.NoError(t, err)
	require.NotNil(t, updated.Secret, "turning a public app confidential issues a secret")
	require.Equal(t, *updated.Secret, f.hydra.Secrets["monitor"])
	require.Equal(t, "Grafana", updated.App.Settings.Name)
	require.NotEqual(t, saved.App.Revision, updated.App.Revision)
	records := f.audit.Records()
	require.Equal(t, []string{"access", "auth_method", "client_secret", "name"}, records[len(records)-1].ChangedFields)
}

func TestRotateAndDeleteSignInApp(t *testing.T) {
	f := newFixture(t)
	admin := newMember(t)
	saved := f.create(t, admin, "monitor", settings(oauthapp.AccessAdmins, oauthapp.AuthMethodClientSecretBasic))

	rotated, err := f.service.RotateSignInAppSecret(admin.ctx, oauthapp.AppCommand{ClientID: "monitor", ExpectedRevision: saved.App.Revision})
	require.NoError(t, err)
	require.NotEqual(t, *saved.Secret, *rotated.Secret)
	require.Equal(t, *rotated.Secret, f.hydra.Secrets["monitor"])

	require.NoError(t, f.service.DeleteSignInApp(admin.ctx, oauthapp.AppCommand{ClientID: "monitor", ExpectedRevision: rotated.App.Revision}))
	_, registered := f.hydra.Client("monitor")
	require.False(t, registered)
	_, stored := f.store.Record("monitor")
	require.False(t, stored)
}

func TestAuthorizeOAuthSignInFollowsTheAppAccess(t *testing.T) {
	f := newFixture(t)
	admin, someone, tagged, banned := newMember(t), newMember(t), newMember(t), newMember(t)
	criteria := uuid.NewString()
	f.eligibility.Criteria[criteria] = oauthapp.CriteriaSummary{ID: criteria, Name: "Testers", Eligible: true}
	f.eligibility.Matches[tagged.memberID] = []string{criteria}
	auth.GetUser(banned.ctx).Banned = true
	f.checker.Deny(policyv1.Platform.IsAdmin())
	f.checker.For(admin.identityID).Allow(policyv1.Platform.IsAdmin())
	f.create(t, admin, "admins", settings(oauthapp.AccessAdmins, oauthapp.AuthMethodNone))
	f.create(t, admin, "members", settings(oauthapp.AccessMembers, oauthapp.AuthMethodNone))
	f.create(t, admin, "testers", settings(oauthapp.AccessMemberCriteria, oauthapp.AuthMethodNone, criteria))
	f.create(t, admin, "nobody", settings(oauthapp.AccessNone, oauthapp.AuthMethodNone))

	for _, test := range []struct {
		clientID string
		member   member
		want     oauthapp.Decision
	}{
		{"admins", admin, oauthapp.DecisionAllowed},
		{"admins", someone, oauthapp.DecisionDenied},
		{"members", someone, oauthapp.DecisionAllowed},
		{"members", banned, oauthapp.DecisionDenied},
		{"testers", tagged, oauthapp.DecisionAllowed},
		{"testers", someone, oauthapp.DecisionDenied},
		{"nobody", admin, oauthapp.DecisionDenied},
		{"https://tool.example/client.json", someone, oauthapp.DecisionNotSignInApp},
	} {
		decision, err := f.service.AuthorizeOAuthSignIn(test.member.ctx, test.clientID)
		require.NoError(t, err)
		require.Equal(t, test.want, decision, test.clientID)
	}
}

func TestListConnectedToolsLeavesSignInAppsOut(t *testing.T) {
	f := newFixture(t)
	admin := newMember(t)
	f.create(t, admin, "monitor", settings(oauthapp.AccessAdmins, oauthapp.AuthMethodNone))
	f.hydra.Seed(oauthapp.Client{ID: "https://tool.example/client.json", Name: "Tool", Audience: []string{mcpResource}, AuthMethod: "none"})
	f.hydra.Seed(oauthapp.Client{ID: "8d1c", Name: "Old tool", Audience: []string{"https://old.example/mcp"}})

	tools, err := f.service.ListConnectedTools(admin.ctx)
	require.NoError(t, err)

	require.Len(t, tools, 2)
	require.Equal(t, "8d1c", tools[0].ClientID)
	require.True(t, tools[0].Stale)
	require.Nil(t, tools[0].Publisher)
	require.Equal(t, "tool.example", *tools[1].Publisher)
	require.False(t, tools[1].Stale)

	require.Equal(t, connect.CodeNotFound, errs.Wrap(f.service.DisconnectConnectedTool(admin.ctx, "monitor")).Code(),
		"a sign-in app is not disconnected as a tool")
	require.NoError(t, f.service.DisconnectConnectedTool(admin.ctx, "8d1c"))
	_, connected := f.hydra.Client("8d1c")
	require.False(t, connected)
}

func actions(recorder *audittest.Recorder) []sharedtelemetry.AuditAction {
	var result []sharedtelemetry.AuditAction
	for _, record := range recorder.Records() {
		result = append(result, record.Action)
	}
	return result
}
