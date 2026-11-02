//go:build integration

package legal_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"

	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestLegalVersionCreateRejectsAdminRevokedWhileVersionLockWaitsIntegration(t *testing.T) {
	t.Parallel()
	for _, policy := range []struct {
		name     string
		lockName string
		table    string
	}{
		{name: "terms", lockName: "terms_history_version", table: "terms_history"},
		{name: "privacy", lockName: "privacy_history_version", table: "privacy_history"},
	} {
		t.Run(policy.name, func(t *testing.T) {
			stack := orytest.SetupOryStack(t)
			integration := newLegalIntegration(t, stack.DB, stack.SpiceDBClient, &audittest.Recorder{})
			actor := stack.CreateUser(t, policyv1.Role.Admin().ID())
			ctx := auth.WithUser(t.Context(), actor.AuthUserInfo())
			var before int64
			require.NoError(t, stack.DB.Table(policy.table).Count(&before).Error)

			var create func() error
			switch policy.name {
			case "terms":
				service := integration.terms()
				create = func() error {
					_, createErr := service.CreateTermsVersion(ctx, connect.NewRequest(
						&managev1.CreateTermsVersionRequest{
							Document: legalPolicyDocumentFixture("en", "terms authority fence"),
						},
					))
					return createErr
				}
			case "privacy":
				service := integration.privacy()
				create = func() error {
					_, createErr := service.CreatePrivacyVersion(ctx, connect.NewRequest(
						&managev1.CreatePrivacyVersionRequest{
							Document: legalPolicyDocumentFixture("en", "privacy authority fence"),
						},
					))
					return createErr
				}
			default:
				t.Fatalf("unexpected policy %q", policy.name)
			}

			err := runLegalVersionCreateRevocationAttempt(
				t, stack, actor, ctx, policy.lockName, create,
			)
			require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
			var after int64
			require.NoError(t, stack.DB.Table(policy.table).Count(&after).Error)
			require.Equal(t, before, after)
		})
	}
}

func runLegalVersionCreateRevocationAttempt(
	t *testing.T,
	stack *orytest.OryStack,
	actor *orytest.OryUser,
	ctx context.Context,
	lockName string,
	create func() error,
) error {
	t.Helper()
	sqlDB, err := stack.DB.DB()
	require.NoError(t, err)
	lockConn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lockConn.Close() })
	_, err = lockConn.ExecContext(ctx, "SELECT pg_advisory_lock(hashtext($1))", lockName)
	require.NoError(t, err)
	locked := true
	t.Cleanup(func() {
		if locked {
			_, _ = lockConn.ExecContext(
				context.Background(), "SELECT pg_advisory_unlock(hashtext($1))", lockName,
			)
		}
	})

	result := make(chan error, 1)
	go func() { result <- create() }()
	pgtest.RequireLockWaiters(t, stack.DB, 1)
	demoteLegalIntegrationIdentity(t, stack, actor.IdentityID)
	_, err = lockConn.ExecContext(ctx, "SELECT pg_advisory_unlock(hashtext($1))", lockName)
	require.NoError(t, err)
	locked = false
	return <-result
}

func demoteLegalIntegrationIdentity(
	t *testing.T,
	stack *orytest.OryStack,
	identityID string,
) {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.SyncAccountIdentityGlobalRole(
		t.Context(), subject, policyv1.Role.User(),
	)
	require.NoError(t, err)
}
