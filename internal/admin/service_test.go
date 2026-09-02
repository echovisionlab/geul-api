package admin

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestAdminDashboardRequiresUsableAdminUnit(t *testing.T) {
	svc := &Service{}

	_, err := svc.GetDashboardStats(context.Background(), connect.NewRequest(&managev1.GetDashboardStatsRequest{}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	userCtx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(uuid.NewString()),
		MemberID:      auth.MemberID(uuid.NewString()),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true,
	})
	_, err = svc.GetDashboardStats(userCtx, connect.NewRequest(&managev1.GetDashboardStatsRequest{}))
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))

	bannedAdminCtx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(uuid.NewString()),
		MemberID:      auth.MemberID(uuid.NewString()),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true,
		Banned:        true,
	})
	_, err = svc.GetDashboardStats(bannedAdminCtx, connect.NewRequest(&managev1.GetDashboardStatsRequest{}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}
