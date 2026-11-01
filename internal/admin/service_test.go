package admin

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
)

func TestAdminDashboardRequiresUsableAdminUnit(t *testing.T) {
	svc := &Service{}

	_, err := svc.DashboardStats(context.Background())
	require.Equal(t, connect.CodePermissionDenied, errs.Wrap(err).Code())

	userCtx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(uuid.NewString()),
		MemberID:      auth.MemberID(uuid.NewString()),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true,
	})
	_, err = svc.DashboardStats(userCtx)
	require.Equal(t, connect.CodeUnavailable, errs.Wrap(err).Code())

	bannedAdminCtx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(uuid.NewString()),
		MemberID:      auth.MemberID(uuid.NewString()),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true,
		Banned:        true,
	})
	_, err = svc.DashboardStats(bannedAdminCtx)
	require.Equal(t, connect.CodePermissionDenied, errs.Wrap(err).Code())
}
