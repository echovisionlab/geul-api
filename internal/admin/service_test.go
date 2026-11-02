package admin_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/admin"
	"github.com/echovisionlab/geul-api/internal/admin/admintest"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// newHandler serves the dashboard over a Store with totals that fails every
// count with failure, checker answering SpiceDB.
func newHandler(t *testing.T, totals admin.DashboardStats, failure error, checker *authztest.Checker) *admin.ConnectHandler {
	t.Helper()
	store, units := admintest.New(totals)
	store.Err = failure
	service, err := admin.NewService(admin.Deps{Units: units, SpiceDB: checker})
	require.NoError(t, err)
	return admin.NewConnectHandler(service, nil)
}

func userContext(banned bool) (context.Context, string) {
	identityID := uuid.NewString()
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(uuid.NewString()),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Banned: banned,
	}), identityID
}

func stats(handler *admin.ConnectHandler, ctx context.Context) (*managev1.DashboardStats, error) {
	response, err := handler.GetDashboardStats(ctx, connect.NewRequest(&managev1.GetDashboardStatsRequest{}))
	if err != nil {
		return nil, err
	}
	return response.Msg.GetStats(), nil
}

func TestDashboardStatsReportsEveryTotalCappedToTheAPIRange(t *testing.T) {
	checker := authztest.AllowAll()
	handler := newHandler(t, admin.DashboardStats{Members: 2, Posts: 3, Pages: 1, Comments: math.MaxInt32 + 1, Campaigns: 4}, nil, checker)
	ctx, identityID := userContext(false)

	got, err := stats(handler, ctx)

	require.NoError(t, err)
	require.Equal(t, int32(2), got.GetTotalMembers())
	require.Equal(t, int32(3), got.GetTotalPosts())
	require.Equal(t, int32(1), got.GetTotalPages())
	require.Equal(t, int32(math.MaxInt32), got.GetTotalComments())
	require.Equal(t, int32(4), got.GetTotalCampaigns())
	can, canErr := policyv1.Platform.IsAdmin()
	require.Equal(t, []authztest.Check{authztest.Of(identityID, can, canErr)}, checker.Checks())
}

func TestDashboardStatsRequiresAUsableAdministrator(t *testing.T) {
	unavailable := errors.New("SpiceDB unavailable")
	member, _ := userContext(false)
	banned, _ := userContext(true)
	for name, test := range map[string]struct {
		ctx     context.Context
		checker *authztest.Checker
		code    connect.Code
	}{
		"an anonymous caller":      {ctx: context.Background(), checker: authztest.AllowAll(), code: connect.CodePermissionDenied},
		"a banned administrator":   {ctx: banned, checker: authztest.AllowAll(), code: connect.CodePermissionDenied},
		"a member who is no admin": {ctx: member, checker: &authztest.Checker{}, code: connect.CodePermissionDenied},
		"an unavailable SpiceDB":   {ctx: member, checker: authztest.AllowAll().Fail(unavailable), code: connect.CodeUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := stats(newHandler(t, admin.DashboardStats{}, nil, test.checker), test.ctx)

			require.Equal(t, test.code, errs.Wrap(err).Code())
		})
	}
}

func TestDashboardStatsFailsWhenACountFails(t *testing.T) {
	countDown := errors.New("count failed")
	ctx, _ := userContext(false)

	_, err := stats(newHandler(t, admin.DashboardStats{}, countDown, authztest.AllowAll()), ctx)

	require.ErrorIs(t, err, countDown)
}
