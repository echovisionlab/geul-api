//go:build integration

package maptheme

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
)

func mapThemeMemberContext(user *testutil.OryUser) context.Context {
	return auth.WithUser(context.Background(), user.AuthUserInfo())
}

func TestManageMapThemeRPCsRequireAdmin(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	member := stack.CreateUser(t, policyv1.Role.User().ID())
	service := mapThemeServiceForTest(t, stack.DB, stack.SpiceDBClient)
	adminCtx := mapThemeMemberContext(admin)
	memberCtx := mapThemeMemberContext(member)
	target, err := service.CreateMapTheme(
		adminCtx,
		connect.NewRequest(validCreateMapThemeRequest("Authorization target "+integrationTestUUID())),
	)
	require.NoError(t, err)
	id := target.Msg.Id
	t.Cleanup(func() {
		_, _ = service.DeleteMapTheme(
			adminCtx,
			connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: id}),
		)
	})
	tests := []struct {
		name string
		call func(context.Context) error
	}{
		{name: "get", call: func(ctx context.Context) error {
			_, err := service.GetMapTheme(ctx, connect.NewRequest(&managev1.GetMapThemeRequest{Id: id}))
			return err
		}},
		{name: "create", call: func(ctx context.Context) error {
			_, err := service.CreateMapTheme(
				ctx,
				connect.NewRequest(validCreateMapThemeRequest("Unauthorized create "+integrationTestUUID())),
			)
			return err
		}},
		{name: "copy", call: func(ctx context.Context) error {
			_, err := service.CopyMapTheme(ctx, connect.NewRequest(&managev1.CopyMapThemeRequest{Id: id, Name: "copy"}))
			return err
		}},
		{name: "set_default", call: func(ctx context.Context) error {
			_, err := service.SetDefaultMapTheme(ctx, connect.NewRequest(&managev1.SetDefaultMapThemeRequest{ThemeId: id}))
			return err
		}},
		{name: "delete", call: func(ctx context.Context) error {
			_, err := service.DeleteMapTheme(ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: id}))
			return err
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(test.call(memberCtx)))
		})
	}
}
