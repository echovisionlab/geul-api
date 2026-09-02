//go:build integration

package public

import (
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestEvaluateFormAccessPolicyMatrix(t *testing.T) {
	stack, err := testutil.StartBackendIntegrationStack(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stack.Close()) })
	spiceDB := stack.SpiceDBClient
	now := time.Now()
	requireAuth := true
	allowDuplicates := true
	passwordHash := "$2a$04$WLWqfbCrZp4bLVMi.3wxKODz/gfg0vR7.KYku23QsAFjH85NcIF0i"
	futureOpen := now.Add(time.Hour)
	pastClose := now.Add(-time.Hour)
	currentOpen := now.Add(-time.Hour)
	currentClose := now.Add(time.Hour)

	baseForm := func() *model.Form {
		return &model.Form{
			ID:                       "form-policy",
			Status:                   model.FormStatus(managev1.FormStatus_FORM_STATUS_PUBLISHED.String()),
			IsPublic:                 true,
			AllowDuplicateSubmission: &allowDuplicates,
			CreatedAt:                now,
		}
	}

	adminCtx := publicFormPolicyUserContext(t, spiceDB, policyv1.Role.Admin())
	authorCtx := publicFormPolicyUserContext(t, spiceDB, policyv1.Role.Author())
	malformedRoleCtx := publicFormPolicyUserContextWithoutRole(t)

	cases := []struct {
		name string
		ctx  context.Context
		form func() *model.Form
		opts formAccessOptions
		want openv1.FormAccessReason
	}{
		{
			name: "draft without preview token",
			ctx:  context.Background(),
			form: func() *model.Form {
				form := baseForm()
				form.Status = model.FormStatus(managev1.FormStatus_FORM_STATUS_DRAFT.String())
				return form
			},
			want: openv1.FormAccessReason_FORM_ACCESS_REASON_FORM_NOT_PUBLISHED,
		},
		{
			name: "draft with preview token bypasses publish and public gates",
			ctx:  context.Background(),
			form: func() *model.Form {
				form := baseForm()
				form.Status = model.FormStatus(managev1.FormStatus_FORM_STATUS_DRAFT.String())
				form.IsPublic = false
				return form
			},
			opts: formAccessOptions{
				context:              openv1.FormAccessContext_FORM_ACCESS_CONTEXT_URL,
				hasValidPreviewToken: true,
			},
			want: openv1.FormAccessReason_FORM_ACCESS_REASON_ALLOWED,
		},
		{
			name: "private URL without preview token",
			ctx:  context.Background(),
			form: func() *model.Form {
				form := baseForm()
				form.IsPublic = false
				return form
			},
			opts: formAccessOptions{context: openv1.FormAccessContext_FORM_ACCESS_CONTEXT_URL},
			want: openv1.FormAccessReason_FORM_ACCESS_REASON_NOT_PUBLIC,
		},
		{
			name: "required auth blocks anonymous",
			ctx:  context.Background(),
			form: func() *model.Form {
				form := baseForm()
				form.RequireAuth = &requireAuth
				return form
			},
			want: openv1.FormAccessReason_FORM_ACCESS_REASON_AUTH_REQUIRED,
		},
		{
			name: "allowed roles block anonymous even without require auth",
			ctx:  context.Background(),
			form: func() *model.Form {
				form := baseForm()
				form.AllowedRoles = []string{policyv1.Role.Admin().ID()}
				return form
			},
			want: openv1.FormAccessReason_FORM_ACCESS_REASON_AUTH_REQUIRED,
		},
		{
			name: "allowed roles reject different role",
			ctx:  authorCtx,
			form: func() *model.Form {
				form := baseForm()
				form.RequireAuth = &requireAuth
				form.AllowedRoles = []string{policyv1.Role.Admin().ID()}
				return form
			},
			want: openv1.FormAccessReason_FORM_ACCESS_REASON_ROLE_NOT_ALLOWED,
		},
		{
			name: "allowed roles reject malformed role",
			ctx:  malformedRoleCtx,
			form: func() *model.Form {
				form := baseForm()
				form.AllowedRoles = []string{policyv1.Role.Admin().ID()}
				return form
			},
			want: openv1.FormAccessReason_FORM_ACCESS_REASON_ROLE_NOT_ALLOWED,
		},
		{
			name: "allowed roles permit admin",
			ctx:  adminCtx,
			form: func() *model.Form {
				form := baseForm()
				form.RequireAuth = &requireAuth
				form.AllowedRoles = []string{policyv1.Role.Admin().ID()}
				return form
			},
			want: openv1.FormAccessReason_FORM_ACCESS_REASON_ALLOWED,
		},
		{
			name: "future open window",
			ctx:  context.Background(),
			form: func() *model.Form {
				form := baseForm()
				form.OpensAt = &futureOpen
				return form
			},
			want: openv1.FormAccessReason_FORM_ACCESS_REASON_NOT_YET_OPEN,
		},
		{
			name: "past close window",
			ctx:  context.Background(),
			form: func() *model.Form {
				form := baseForm()
				form.ClosesAt = &pastClose
				return form
			},
			want: openv1.FormAccessReason_FORM_ACCESS_REASON_CLOSED,
		},
		{
			name: "current window",
			ctx:  context.Background(),
			form: func() *model.Form {
				form := baseForm()
				form.OpensAt = &currentOpen
				form.ClosesAt = &currentClose
				return form
			},
			want: openv1.FormAccessReason_FORM_ACCESS_REASON_ALLOWED,
		},
		{
			name: "password required",
			ctx:  context.Background(),
			form: func() *model.Form {
				form := baseForm()
				form.AccessPassword = &passwordHash
				return form
			},
			opts: formAccessOptions{enforcePassword: true},
			want: openv1.FormAccessReason_FORM_ACCESS_REASON_PASSWORD_REQUIRED,
		},
	}

	service := &FormService{spiceDB: spiceDB}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, err := service.evaluateFormAccess(tc.ctx, tc.form(), tc.opts)
			require.NoError(t, err)
			require.Equal(t, tc.want, reason)
		})
	}
}

func publicFormPolicyUserContext(t *testing.T, spiceDB *auth.SpiceDBClient, role policyv1.RoleID) context.Context {
	t.Helper()
	identityID := uuid.NewString()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = spiceDB.SyncAccountIdentityGlobalRole(context.Background(), subject, role)
	require.NoError(t, err)
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(identityID),
		MemberID:      auth.MemberID(uuid.NewString()),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true,
		Onboarded:     true,
	})
}

func publicFormPolicyUserContextWithoutRole(t *testing.T) context.Context {
	t.Helper()
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(uuid.NewString()),
		MemberID:      auth.MemberID(uuid.NewString()),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true,
		Onboarded:     true,
	})
}
