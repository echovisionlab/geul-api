package sitesettings_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	"github.com/echovisionlab/geul-api/internal/sitesettings/sitesettingstest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type fixture struct {
	store    *sitesettingstest.Store
	units    *mutationtest.UnitOfWork[sitesettings.Store]
	checker  *authztest.Checker
	fence    *identitystatetest.Fence
	audit    *audittest.Recorder
	settings *sitesettings.ConnectHandler
	admin    principal
}

// newFixture builds the use cases with checker answering SpiceDB and audit
// recording; nil takes a checker that allows everything and a recorder.
func newFixture(t *testing.T, checker *authztest.Checker, audit *audittest.Recorder) *fixture {
	t.Helper()
	if checker == nil {
		checker = authztest.AllowAll()
	}
	if audit == nil {
		audit = &audittest.Recorder{}
	}
	store, units := sitesettingstest.New()
	fence := &identitystatetest.Fence{}
	service, err := sitesettings.NewService(sitesettings.Deps{
		Units: units, SpiceDB: checker, Fence: fence, Audit: audit, SiteOrigin: " https://www.example.test/ ",
	})
	require.NoError(t, err)
	return &fixture{
		store: store, units: units, checker: checker, fence: fence, audit: audit,
		settings: sitesettings.NewConnectHandler(service), admin: newPrincipal(t),
	}
}

// principal is an authenticated, onboarded Member with a request context,
// as the Connect and MCP gateways hand it to a use case.
type principal struct {
	identityID string
	memberID   string
	ctx        context.Context
}

func newPrincipal(t *testing.T) principal {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.77")
	require.NoError(t, err)
	identityID, memberID := uuid.NewString(), uuid.NewString()
	ctx := auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), requestContext), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
	return principal{identityID: identityID, memberID: memberID, ctx: ctx}
}

// changedFields returns the changed fields of each Site Settings record
// appended so far, asserting each is attributed to the administrator's
// request.
func (f *fixture) changedFields(t *testing.T) [][]string {
	t.Helper()
	var fields [][]string
	for _, record := range f.audit.Records() {
		require.Equal(t, sharedtelemetry.AuditSiteSettingsUpdated, record.Action)
		require.Equal(t, "site_settings", record.TargetType)
		require.Equal(t, "1", record.TargetID)
		require.Equal(t, f.admin.memberID, record.MemberID)
		require.Equal(t, sharedtelemetry.RequestIDFromContext(f.admin.ctx), record.RequestID)
		fields = append(fields, record.ChangedFields)
	}
	return fields
}

func (f *fixture) set(key string, value *structpb.Value) (*connect.Response[managev1.SetSettingResponse], error) {
	return f.settings.SetSetting(f.admin.ctx, connect.NewRequest(&managev1.SetSettingRequest{Key: key, Value: value}))
}

func (f *fixture) setMany(settings ...*managev1.SiteSetting) (*connect.Response[managev1.SetManySettingsResponse], error) {
	return f.settings.SetManySettings(f.admin.ctx, connect.NewRequest(&managev1.SetManySettingsRequest{Settings: settings}))
}

func (f *fixture) get(t *testing.T, key string) *structpb.Value {
	t.Helper()
	response, err := f.settings.GetSetting(f.admin.ctx, connect.NewRequest(&managev1.GetSettingRequest{Key: key}))
	require.NoError(t, err)
	require.Equal(t, key, response.Msg.GetSetting().GetKey())
	return response.Msg.GetSetting().GetValue()
}

func (f *fixture) all(t *testing.T) *managev1.AllSettings {
	t.Helper()
	response, err := f.settings.GetSettings(f.admin.ctx, connect.NewRequest(&managev1.GetSettingsRequest{}))
	require.NoError(t, err)
	return response.Msg.GetSettings()
}

func setting(key string, value *structpb.Value) *managev1.SiteSetting {
	return &managev1.SiteSetting{Key: key, Value: value}
}

func checkOf(identityID string) func(policyv1.Can, error) authztest.Check {
	return func(can policyv1.Can, err error) authztest.Check { return authztest.Of(identityID, can, err) }
}

func lastStep(f *fixture) mutation.Step {
	steps := f.units.Steps()
	return steps[len(steps)-1]
}
