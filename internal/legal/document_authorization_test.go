package legal

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type legalPermissionCheck struct {
	decision policyv1.AuthorizationDecision
}

func TestLegalBatchSourceLifecycleClassificationPreservesTargetEditing(t *testing.T) {
	t.Parallel()

	require.False(t, legalBatchTouchesSource(nil, "en"))
	require.False(t, legalBatchTouchesSource(&contentblock.Batch{
		LocaleGroups: []contentblock.LocaleMutationGroup{{Locale: "ko"}},
	}, "en"))
	require.True(t, legalBatchTouchesSource(&contentblock.Batch{
		LocaleGroups: []contentblock.LocaleMutationGroup{{Locale: "en"}},
	}, "en"))
	require.True(t, legalBatchTouchesSource(&contentblock.Batch{
		Deletes: []uuid.UUID{uuid.MustParse("33333333-3333-4333-8333-333333333333")},
	}, "en"))
}

func TestLegalTargetLocalesExcludeSourceAndDuplicates(t *testing.T) {
	t.Parallel()

	require.Equal(t, []string{"ja", "ko"}, legalTargetLocales("en", &contentblock.Batch{
		LocaleGroups: []contentblock.LocaleMutationGroup{
			{Locale: "ko"}, {Locale: "en"}, {Locale: "ja"}, {Locale: "ko"},
		},
	}))
}

type recordingLegalPermissionChecker struct {
	calls []legalPermissionCheck
}

func (checker *recordingLegalPermissionChecker) Can(
	_ context.Context,
	decision policyv1.AuthorizationDecision,
) (bool, error) {
	checker.calls = append(checker.calls, legalPermissionCheck{decision: decision})
	return true, nil
}

func (checker *recordingLegalPermissionChecker) CheckActorCan(
	_ context.Context,
	_ policyv1.Actor,
	_ policyv1.Can,
) (bool, error) {
	return true, nil
}

func TestLegalPermissionSelectionUsesArchivedCapabilitiesExclusively(t *testing.T) {
	t.Parallel()

	for _, entityType := range []string{"terms", "privacy"} {
		policy, err := legalDocumentPolicyForType(entityType)
		require.NoError(t, err)
		require.Equal(t, legalActionViewArchived, legalViewAction(policy, policy.archivedStatus))
		activeStatus := managev1.TermsStatus_TERMS_STATUS_ACTIVE.String()
		if entityType == "privacy" {
			activeStatus = managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE.String()
		}
		for _, status := range []string{policy.draftStatus, policy.scheduledStatus, activeStatus} {
			require.Equal(t, legalActionView, legalViewAction(policy, status))
		}
		for _, normal := range []legalAction{
			legalActionEdit,
			legalActionDelete,
			legalActionPublish,
			legalActionManage,
			legalActionManageShareLinks,
		} {
			require.Equal(t, legalActionEditArchived, legalMutationAction(policy, policy.archivedStatus, normal))
			for _, status := range []string{policy.draftStatus, policy.scheduledStatus, activeStatus} {
				require.Equal(t, normal, legalMutationAction(policy, status, normal))
			}
		}
	}
}

func TestRequireLegalPermissionForPrincipalChecksExactObjectActionOnce(t *testing.T) {
	t.Parallel()

	checker := &recordingLegalPermissionChecker{}
	principal := &auth.UserInfo{
		IdentityID:    "11111111-1111-4111-8111-111111111111",
		MemberID:      "33333333-3333-4333-8333-333333333333",
		SessionID:     "legal-session",
		Authenticated: true,
		Onboarded:     true,
	}
	require.NoError(t, requireLegalPermissionForPrincipal(
		context.Background(), checker,
		intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_TERMS_HISTORY,
		"22222222-2222-4222-8222-222222222222",
		legalActionViewArchived,
		principal,
	))
	require.Len(t, checker.calls, 1)
	decision := checker.calls[0].decision
	require.Equal(t, principal.IdentityID.String(), decision.Actor().AccountIdentityID())
	require.Equal(t, "terms_history", decision.Resource().Type())
	require.Equal(t, "22222222-2222-4222-8222-222222222222", decision.Resource().ID())
	require.Equal(t, "view_archived", decision.Action().Name())
	require.Equal(t, "view_archived", decision.Action().Permission())
	require.Equal(t, policyv1.DelegationDirectSession, decision.Delegation().Kind())
}

func TestLegalTargetLocaleAuditOperationReflectsTranslationLifecycle(t *testing.T) {
	t.Parallel()

	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, legalTargetLocaleContentOperation(AITranslationCreate, false))
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, legalTargetLocaleContentOperation(AITranslationUnchanged, false))
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, legalTargetLocaleContentOperation(AITranslationUnchanged, true))
	require.Equal(t, sharedtelemetry.AuditItemOperationDeleted, legalTargetLocaleContentOperation(AITranslationDelete, true))
}

var _ CollaborationPermissionChecker = (*recordingLegalPermissionChecker)(nil)
