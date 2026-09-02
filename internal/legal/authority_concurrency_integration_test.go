//go:build integration

package legal_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestLegalVersionCreateRejectsAdminRevokedWhileVersionLockWaitsIntegration(t *testing.T) {
	for _, policy := range []struct {
		name     string
		lockName string
		table    string
	}{
		{name: "terms", lockName: "terms_history_version", table: "terms_history"},
		{name: "privacy", lockName: "privacy_history_version", table: "privacy_history"},
	} {
		t.Run(policy.name, func(t *testing.T) {
			stack := testutil.SetupOryStack(t)
			store, err := contentblock.NewGeneratedStore(
				filemedia.NewContentBlockFileReuseAuthorizer(stack.SpiceDBClient),
			)
			require.NoError(t, err)
			actor := stack.CreateUser(t, policyv1.Role.Admin().ID())
			ctx := auth.WithUser(t.Context(), actor.AuthUserInfo())
			var before int64
			require.NoError(t, stack.DB.Table(policy.table).Count(&before).Error)

			var create func() error
			switch policy.name {
			case "terms":
				service := legaldomain.NewTermsService(
					stack.DB, "", stack.SpiceDBClient,
					legalIntegrationDependencies(stack.DB, ""),
					legaldomain.WithTermsContentBlockStore(store),
				)
				create = func() error {
					_, createErr := service.CreateTermsVersion(ctx, connect.NewRequest(
						&managev1.CreateTermsVersionRequest{
							Document: legalPolicyDocumentFixture("en", "terms authority fence"),
						},
					))
					return createErr
				}
			case "privacy":
				service := legaldomain.NewPrivacyService(
					stack.DB, "", stack.SpiceDBClient,
					legalIntegrationDependencies(stack.DB, ""),
					legaldomain.WithPrivacyContentBlockStore(store),
				)
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

			err = runLegalVersionCreateRevocationAttempt(
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
	stack *testutil.OryStack,
	actor *testutil.OryUser,
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
	requirePendingLegalMutationLock(t, stack.DB)
	demoteLegalIntegrationIdentity(t, stack, actor.IdentityID)
	_, err = lockConn.ExecContext(ctx, "SELECT pg_advisory_unlock(hashtext($1))", lockName)
	require.NoError(t, err)
	locked = false
	return <-result
}

func demoteLegalIntegrationIdentity(
	t *testing.T,
	stack *testutil.OryStack,
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

func requirePendingLegalMutationLock(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.Eventually(t, func() bool {
		var waiting bool
		err := db.Raw(`
			SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity
				WHERE wait_event_type = 'Lock'
				  AND cardinality(pg_blocking_pids(pid)) > 0
			)`).Scan(&waiting).Error
		return err == nil && waiting
	}, 5*time.Second, 20*time.Millisecond)
}
