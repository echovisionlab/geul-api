//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type blockingWorkPolicyChecker struct {
	entered chan policyv1.AuthorizationDecision
	release chan struct{}
}

func (c *blockingWorkPolicyChecker) Can(_ context.Context, decision policyv1.AuthorizationDecision) (bool, error) {
	c.entered <- decision
	<-c.release
	return true, nil
}

func (c *blockingWorkPolicyChecker) CheckActorCan(
	_ context.Context,
	_ policyv1.Actor,
	_ policyv1.Can,
) (bool, error) {
	return true, nil
}

func TestWorkPolicyAuthorityLocksArchivedRootAndActivePrincipalIntegration(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		requirePolicy  func(context.Context, *workdomain.PolicyAuthority, *gorm.DB, string) error
		expectedAction func(string) (policyv1.Can, error)
		mutate         func(*gorm.DB, string, string) error
	}{
		{
			name: "view locks Work root",
			requirePolicy: func(ctx context.Context, authority *workdomain.PolicyAuthority, tx *gorm.DB, workID string) error {
				return authority.RequireLockedView(ctx, tx, workID)
			},
			expectedAction: policyv1.Work.ViewArchived,
			mutate: func(db *gorm.DB, workID, _ string) error {
				return db.Exec(`UPDATE work SET status = ? WHERE id = ?::uuid`, managev1.WorkStatus_WORK_STATUS_DRAFT.String(), workID).Error
			},
		},
		{
			name: "edit locks active principal",
			requirePolicy: func(ctx context.Context, authority *workdomain.PolicyAuthority, tx *gorm.DB, workID string) error {
				return authority.RequireLockedEdit(ctx, tx, workID)
			},
			expectedAction: policyv1.Work.EditArchived,
			mutate: func(db *gorm.DB, _, memberID string) error {
				return db.Exec(`UPDATE member SET onboarded = FALSE WHERE id = ?::uuid`, memberID).Error
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := newConcurrentServiceIntegrationDB(t)
			identityID := integrationTestUUID()
			memberID := seedExternalKratosIdentityWithTraits(t, db, identityID, "Work Policy Authority")
			documentID, workID := integrationTestUUID(), integrationTestUUID()
			require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile, revision) VALUES (?::uuid, 'work', ?::uuid)`, documentID, integrationTestUUID()).Error)
			require.NoError(t, db.Exec(`INSERT INTO work (id, type, status, year, month, is_present, content_document_id) VALUES (?::uuid, ?::work_type, ?, 2026, 8, TRUE, ?::uuid)`, workID, managev1.WorkType_WORK_TYPE_ARTICLE.String(), managev1.WorkStatus_WORK_STATUS_ARCHIVED.String(), documentID).Error)

			checker := &blockingWorkPolicyChecker{entered: make(chan policyv1.AuthorizationDecision, 1), release: make(chan struct{})}
			authority := workdomain.NewPolicyAuthority(checker)
			ctx := auth.WithUser(t.Context(), &auth.UserInfo{
				IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
				SessionID: auth.SessionID(integrationTestUUID()), Authenticated: true, Onboarded: true,
			})
			authorized := make(chan error, 1)
			go func() {
				authorized <- db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					return testCase.requirePolicy(ctx, authority, tx, workID)
				})
			}()

			var decision policyv1.AuthorizationDecision
			select {
			case decision = <-checker.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("Work policy authority did not reach the exact archived permission check")
			}
			expected, err := testCase.expectedAction(workID)
			require.NoError(t, err)
			require.Equal(t, expected.Action().Permission(), decision.Action().Permission())

			mutationStarted := make(chan struct{})
			mutationDone := make(chan error, 1)
			go func() {
				close(mutationStarted)
				mutationDone <- testCase.mutate(db, workID, memberID)
			}()
			<-mutationStarted
			select {
			case err := <-mutationDone:
				require.NoError(t, err)
				t.Fatal("Work root or active-principal mutation committed while policy authorization held its lock")
			case <-time.After(150 * time.Millisecond):
			}

			close(checker.release)
			require.NoError(t, <-authorized)
			select {
			case err := <-mutationDone:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("Work root or principal mutation did not commit after policy authorization released its locks")
			}
		})
	}
}
