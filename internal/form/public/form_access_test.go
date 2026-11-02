package public

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/form"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type formAccessPolicyCase struct {
	name string
	ctx  context.Context
	form func() *form.Form
	opts formAccessOptions
	want AccessReason
}

func TestEvaluateFormAccessPolicyMatrix(t *testing.T) {
	t.Parallel()
	now := time.Now()
	requireAuth := true
	allowDuplicates := true

	baseForm := func() *form.Form {
		return &form.Form{
			ID: "form-policy", Status: form.StatusPublished, IsPublic: true,
			AllowDuplicateSubmission: &allowDuplicates, CreatedAt: now,
		}
	}
	checker := &authztest.Checker{}
	userContext := func(role func() (policyv1.Can, error)) context.Context {
		identityID := uuid.NewString()
		if role != nil {
			checker.For(identityID).Allow(role())
		}
		return auth.WithUser(context.Background(), &auth.UserInfo{
			IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(uuid.NewString()),
			SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
		})
	}
	adminCtx := userContext(policyv1.Platform.IsAdmin)
	authorCtx := userContext(policyv1.Platform.IsAuthor)
	malformedRoleCtx := userContext(nil)

	cases := formAccessVisibilityCases(baseForm)
	cases = append(cases, formAccessAuthenticationCases(baseForm, &requireAuth, adminCtx, authorCtx, malformedRoleCtx)...)
	cases = append(cases, formAccessAvailabilityCases(baseForm, now)...)

	service := &Service{spiceDB: checker}
	reads := noSubmissions{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, err := service.evaluateFormAccess(tc.ctx, reads, tc.form(), tc.opts)
			require.NoError(t, err)
			require.Equal(t, tc.want, reason)
		})
	}
}

func formAccessVisibilityCases(baseForm func() *form.Form) []formAccessPolicyCase {
	return []formAccessPolicyCase{
		{
			name: "draft without preview token",
			ctx:  context.Background(),
			form: func() *form.Form {
				found := baseForm()
				found.Status = form.StatusDraft
				return found
			},
			want: ReasonFormNotPublished,
		},
		{
			name: "draft with preview token bypasses publish and public gates",
			ctx:  context.Background(),
			form: func() *form.Form {
				found := baseForm()
				found.Status = form.StatusDraft
				found.IsPublic = false
				return found
			},
			opts: formAccessOptions{context: ContextURL, hasValidPreviewToken: true},
			want: ReasonAllowed,
		},
		{
			name: "private URL without preview token",
			ctx:  context.Background(),
			form: func() *form.Form {
				found := baseForm()
				found.IsPublic = false
				return found
			},
			opts: formAccessOptions{context: ContextURL},
			want: ReasonNotPublic,
		},
	}
}

func formAccessAuthenticationCases(baseForm func() *form.Form, requireAuth *bool, adminCtx, authorCtx, malformedRoleCtx context.Context) []formAccessPolicyCase {
	return []formAccessPolicyCase{
		{
			name: "required auth blocks anonymous",
			ctx:  context.Background(),
			form: func() *form.Form {
				found := baseForm()
				found.RequireAuth = requireAuth
				return found
			},
			want: ReasonAuthRequired,
		},
		{
			name: "allowed roles block anonymous even without require auth",
			ctx:  context.Background(),
			form: func() *form.Form {
				found := baseForm()
				found.AllowedRoles = []string{policyv1.Role.Admin().ID()}
				return found
			},
			want: ReasonAuthRequired,
		},
		{
			name: "allowed roles reject different role",
			ctx:  authorCtx,
			form: func() *form.Form {
				found := baseForm()
				found.RequireAuth = requireAuth
				found.AllowedRoles = []string{policyv1.Role.Admin().ID()}
				return found
			},
			want: ReasonRoleNotAllowed,
		},
		{
			name: "allowed roles reject a principal without a role",
			ctx:  malformedRoleCtx,
			form: func() *form.Form {
				found := baseForm()
				found.AllowedRoles = []string{policyv1.Role.Admin().ID()}
				return found
			},
			want: ReasonRoleNotAllowed,
		},
		{
			name: "allowed roles permit admin",
			ctx:  adminCtx,
			form: func() *form.Form {
				found := baseForm()
				found.RequireAuth = requireAuth
				found.AllowedRoles = []string{policyv1.Role.Admin().ID()}
				return found
			},
			want: ReasonAllowed,
		},
	}
}

func formAccessAvailabilityCases(baseForm func() *form.Form, now time.Time) []formAccessPolicyCase {
	passwordHash := "$2a$04$WLWqfbCrZp4bLVMi.3wxKODz/gfg0vR7.KYku23QsAFjH85NcIF0i"
	futureOpen := now.Add(time.Hour)
	pastClose := now.Add(-time.Hour)
	currentOpen := now.Add(-time.Hour)
	currentClose := now.Add(time.Hour)
	return []formAccessPolicyCase{
		{
			name: "future open window",
			ctx:  context.Background(),
			form: func() *form.Form {
				found := baseForm()
				found.OpensAt = &futureOpen
				return found
			},
			want: ReasonNotYetOpen,
		},
		{
			name: "past close window",
			ctx:  context.Background(),
			form: func() *form.Form {
				found := baseForm()
				found.ClosesAt = &pastClose
				return found
			},
			want: ReasonClosed,
		},
		{
			name: "current window",
			ctx:  context.Background(),
			form: func() *form.Form {
				found := baseForm()
				found.OpensAt = &currentOpen
				found.ClosesAt = &currentClose
				return found
			},
			want: ReasonAllowed,
		},
		{
			name: "password required",
			ctx:  context.Background(),
			form: func() *form.Form {
				found := baseForm()
				found.AccessPassword = &passwordHash
				return found
			},
			opts: formAccessOptions{enforcePassword: true},
			want: ReasonPasswordRequired,
		},
	}
}

// noSubmissions answers a Form without submissions.
type noSubmissions struct{}

func (noSubmissions) CountSubmissions(context.Context, string) (int64, error) { return 0, nil }
func (noSubmissions) HasMemberSubmission(context.Context, string, string) (bool, error) {
	return false, nil
}
