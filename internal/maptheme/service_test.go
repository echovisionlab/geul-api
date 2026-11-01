package maptheme_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/maptheme"
	"github.com/echovisionlab/geul-api/internal/maptheme/mapthemetest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// fixture is the Map Theme use cases over the in-memory store.
type fixture struct {
	store    *mapthemetest.Store
	units    *mutationtest.UnitOfWork[maptheme.Store]
	checker  *authztest.Checker
	audit    *audittest.Recorder
	service  *maptheme.Service
	themes   *maptheme.ConnectHandler
	internal *maptheme.InternalConnectHandler
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
	store, units := mapthemetest.New()
	service, err := maptheme.NewService(maptheme.Deps{Units: units, SpiceDB: checker, Fence: &identitystatetest.Fence{}, Audit: audit})
	require.NoError(t, err)
	return &fixture{
		store: store, units: units, checker: checker, audit: audit, service: service,
		themes: maptheme.NewConnectHandler(service), internal: maptheme.NewInternalConnectHandler(service),
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
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.98")
	require.NoError(t, err)
	identityID, memberID := uuid.NewString(), uuid.NewString()
	ctx := auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), requestContext), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
	return principal{identityID: identityID, memberID: memberID, ctx: ctx}
}

func (f *fixture) seed(name string) maptheme.Theme {
	return f.store.Seed(mapthemetest.Snapshot(name))
}

// stepsSince returns the steps recorded after the first from.
func (f *fixture) stepsSince(from int) []mutation.Step {
	return f.units.Steps()[from:]
}

// checkOf compares a generated descriptor's check by identityID with
// authztest.Checker.Checks: checkOf(id)(policyv1.MapTheme.Edit(themeID)).
func checkOf(identityID string) func(policyv1.Can, error) authztest.Check {
	return func(can policyv1.Can, err error) authztest.Check { return authztest.Of(identityID, can, err) }
}

func codeOf(err error) connect.Code {
	return errs.Wrap(err).Code()
}

func variantInput(v maptheme.Variant) *managev1.MapThemeVariantInput {
	return &managev1.MapThemeVariantInput{
		BackgroundColor: v.BackgroundColor, WaterColor: v.WaterColor, LandColor: v.LandColor,
		RoadColor: v.RoadColor, BuildingFillColor: v.BuildingFillColor,
		BuildingStrokeEnabled: v.BuildingStrokeEnabled, BuildingStrokeColor: v.BuildingStrokeColor,
		CalloutLineColor: v.CalloutLineColor, CalloutTextColor: v.CalloutTextColor,
		CalloutBackgroundColor: v.CalloutBackgroundColor, CalloutDescriptionColor: v.CalloutDescriptionColor,
		AttributionColor: v.AttributionColor, LabelTextColor: v.LabelTextColor,
		ClusterColor: v.ClusterColor, ClusterHoverColor: v.ClusterHoverColor,
		ClusterTextColor: v.ClusterTextColor, ClusterTextHoverColor: v.ClusterTextHoverColor,
		CalloutHoverLineColor: v.CalloutHoverLineColor, CalloutHoverTextColor: v.CalloutHoverTextColor,
		CalloutHoverDescriptionColor: v.CalloutHoverDescriptionColor,
		CalloutHoverBackgroundColor:  v.CalloutHoverBackgroundColor,
	}
}

// createRequest is the administrator create request of a valid snapshot.
func createRequest(name string) *managev1.CreateMapThemeRequest {
	snapshot := mapthemetest.Snapshot(name)
	settings := snapshot.Settings
	return &managev1.CreateMapThemeRequest{
		Name: name,
		Settings: &managev1.MapThemeSettings{
			CalloutScale: settings.CalloutScale, CalloutOffsetX: settings.CalloutOffsetX, CalloutOffsetY: settings.CalloutOffsetY,
			CalloutFields: settings.CalloutFields, AttributionFontSize: settings.AttributionFontSize,
			ShowAreaLabels: settings.ShowAreaLabels, ShowPoiLabels: settings.ShowPoiLabels,
		},
		LightVariant: variantInput(snapshot.LightVariant),
		DarkVariant:  variantInput(snapshot.DarkVariant),
	}
}

// snapshotInput is a valid snapshot as the MCP tool submits it.
func snapshotInput(name string) *maptheme.SnapshotInput {
	snapshot := mapthemetest.Snapshot(name)
	return &maptheme.SnapshotInput{
		Name: snapshot.Name, Settings: &snapshot.Settings, LightVariant: &snapshot.LightVariant, DarkVariant: &snapshot.DarkVariant,
	}
}

func TestNewServiceNamesEveryMissingCollaborator(t *testing.T) {
	_, err := maptheme.NewService(maptheme.Deps{})
	require.EqualError(t, err, "map theme dependencies are required: Audit, Fence, SpiceDB, Units")
}

type failingReads struct {
	mutation.UnitOfWork[maptheme.Store]
	err error
}

func (f failingReads) Read(context.Context, func(maptheme.Store) error) error { return f.err }

// The handler does not log: the cause stays in the returned error for the one
// request.completed record.
func TestListMapThemesKeepsCauseForTheRequestRecord(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(sharedtelemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	_, units := mapthemetest.New()
	cause := errors.New("transaction backend unavailable")
	service, err := maptheme.NewService(maptheme.Deps{
		Units: failingReads{UnitOfWork: units, err: cause}, SpiceDB: authztest.AllowAll(), Fence: &identitystatetest.Fence{}, Audit: &audittest.Recorder{},
	})
	require.NoError(t, err)

	response, err := maptheme.NewConnectHandler(service).ListMapThemes(t.Context(), connect.NewRequest(&managev1.ListMapThemesRequest{}))

	require.Nil(t, response)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	require.ErrorIs(t, err, cause)
	require.Empty(t, output.String())
}
