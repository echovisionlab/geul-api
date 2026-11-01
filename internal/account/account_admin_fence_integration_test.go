//go:build integration

package account

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
)

// The administrator mutations that only checked the SpiceDB role before the
// pipeline now apply the identity fence: a deactivated or banned
// administrator with a live session changes nothing.
func TestAccountAdminMutationsRejectStaleAdministratorIdentityIntegration(t *testing.T) {
	t.Parallel()
	for name, stale := range map[string]func(*testing.T, *testutil.OryStack, *testutil.OryUser){
		"deactivated identity": func(t *testing.T, stack *testutil.OryStack, admin *testutil.OryUser) {
			require.NoError(t, stack.KratosClient.SetIdentityState(t.Context(), admin.IdentityID, auth.KratosStateInactive))
		},
		"banned identity": func(t *testing.T, stack *testutil.OryStack, admin *testutil.OryUser) {
			require.NoError(t, stack.KratosClient.UpdateIdentityMetadataAdmin(t.Context(), admin.IdentityID, structured.Fields{"banned": true}))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stack := testutil.SetupOryStack(t)
			admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
			// A second administrator keeps the last-admin fence out of the way.
			stack.CreateUser(t, policyv1.Role.Admin().ID())
			target := stack.CreateUser(t, policyv1.Role.User().ID())
			handler := newTestHandler(t, Deps{
				DB: stack.DB, Identity: stack.KratosClient, SpiceDB: stack.SpiceDBClient, Publisher: accountRoleNoopLifecyclePublisher{},
				Newsletter: memberNewsletterSubscriptionIntegration{}, MemberDeletion: memberDeletionIntegrationAdapter{},
				MemberEmails: memberEmailProjectionIntegration{}, Audit: apitelemetry.NewDurableWriter(stack.DB),
			})
			ctx := auditedOryMemberContext(t, admin)
			stale(t, stack, admin)

			_, err := handler.BanAccount(ctx, connect.NewRequest(&managev1.BanAccountRequest{MemberId: target.MemberID}))
			requireAdminRequired(t, err)
			_, err = handler.UnbanAccount(ctx, connect.NewRequest(&managev1.UnbanAccountRequest{MemberId: target.MemberID}))
			requireAdminRequired(t, err)
			_, err = handler.SetAccountCanonicalEmail(ctx, connect.NewRequest(&managev1.SetAccountCanonicalEmailRequest{
				MemberId: target.MemberID, Email: "stale-admin-" + target.Email,
			}))
			requireAdminRequired(t, err)
			_, err = handler.RemoveAccountSsoProvider(ctx, connect.NewRequest(&managev1.RemoveAccountSsoProviderRequest{
				MemberId: target.MemberID, Provider: "google", Identifier: "stale-admin-subject",
			}))
			requireAdminRequired(t, err)
			_, err = handler.UnsubscribeAccountFromNewsletter(ctx, connect.NewRequest(&managev1.UnsubscribeAccountFromNewsletterRequest{MemberId: target.MemberID}))
			requireAdminRequired(t, err)
			_, err = handler.DeleteAccount(ctx, connect.NewRequest(&managev1.DeleteAccountRequest{MemberId: target.MemberID}))
			requireAdminRequired(t, err)

			identity, err := stack.KratosClient.GetIdentity(t.Context(), target.IdentityID)
			require.NoError(t, err)
			require.Equal(t, auth.KratosStateActive, identity.State)
			require.False(t, identity.IsBanned())
			var requests, audits int64
			require.NoError(t, stack.DB.Model(&model.UserDeletionRequest{}).Where("member_id = ?::uuid", target.MemberID).Count(&requests).Error)
			require.Zero(t, requests)
			require.NoError(t, stack.DB.Table("public.domain_audit").Where("target_id = ?", target.MemberID).Count(&audits).Error)
			require.Zero(t, audits)
		})
	}
}

func requireAdminRequired(t *testing.T, err error) {
	t.Helper()
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	require.Equal(t, "admin access required", connectErr.Message())
}
