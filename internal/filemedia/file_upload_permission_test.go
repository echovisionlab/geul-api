//go:build integration

package filemedia

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
)

func TestCheckEntityPermissionAllowsAdminProgramEventPosterForSeriesID(t *testing.T) {
	t.Parallel()

	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	svc := &Service{spiceDB: stack.SpiceDBClient}
	ctx := auth.WithUser(context.Background(), admin.AuthUserInfo())
	target, err := resolveUploadPermissionTarget(managev1.UploadType_UPLOAD_TYPE_PROGRAM_EVENT_POSTER, "")
	require.NoError(t, err)

	require.NoError(t, svc.checkRoleAndSpiceDBUploadPermission(ctx, target, "program-event-series-id", admin.MemberID))
}

func TestCheckEntityPermissionRejectsNonAdminProgramEventPoster(t *testing.T) {
	t.Parallel()

	stack := testutil.SetupOryStack(t)
	user := stack.CreateUser(t, policyv1.Role.User().ID())
	svc := &Service{spiceDB: stack.SpiceDBClient}
	ctx := auth.WithUser(context.Background(), user.AuthUserInfo())
	target, err := resolveUploadPermissionTarget(managev1.UploadType_UPLOAD_TYPE_PROGRAM_EVENT_POSTER, "")
	require.NoError(t, err)

	err = svc.checkRoleAndSpiceDBUploadPermission(ctx, target, "program-event-series-id", user.MemberID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "admin")
}

func TestCheckPartUploadPermissionAllowsAdminProgramEventPosterForSeriesID(t *testing.T) {
	t.Parallel()

	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	svc := &Service{spiceDB: stack.SpiceDBClient}
	target, err := resolveUploadPermissionTarget(managev1.UploadType_UPLOAD_TYPE_PROGRAM_EVENT_POSTER, "")
	require.NoError(t, err)
	require.NoError(t, svc.checkRoleAndSpiceDBUploadPermission(auth.WithUser(context.Background(), admin.AuthUserInfo()), target, "program-event-series-id", admin.MemberID))
}
