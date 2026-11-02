package legal

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestLegalTranslationInterchangeAuditIsExactMemberLocaleContent(t *testing.T) {
	memberID := uuid.NewString()
	policyID := uuid.NewString()
	for _, testCase := range []struct {
		name       string
		entityType string
		policyType sharedtelemetry.AuditPolicyType
		operation  sharedtelemetry.AuditItemOperation
	}{
		{
			name: "privacy created", entityType: "privacy",
			policyType: sharedtelemetry.AuditPolicyTypePrivacy,
			operation:  sharedtelemetry.AuditItemOperationCreated,
		},
		{
			name: "privacy deleted", entityType: "privacy",
			policyType: sharedtelemetry.AuditPolicyTypePrivacy,
			operation:  sharedtelemetry.AuditItemOperationDeleted,
		},
		{
			name: "terms updated", entityType: "terms",
			policyType: sharedtelemetry.AuditPolicyTypeTerms,
			operation:  sharedtelemetry.AuditItemOperationUpdated,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := &audittest.Recorder{}
			change := legalLocaleContentChange{
				kind: testCase.entityType, policyID: policyID, version: 7, locale: "ko",
				operation: testCase.operation, memberID: memberID,
			}
			require.NoError(t, domainaudit.AppendMember(
				t.Context(), nil, recorder, change.memberID, sharedtelemetry.AuditLegalPolicyUpdated, change.record,
			))
			require.Len(t, recorder.Records(), 1)
			record := recorder.Records()[0]
			require.NoError(t, record.Validate())
			require.Equal(t, sharedtelemetry.AuditLegalPolicyUpdated, record.Action)
			require.Equal(t, "legal_policy", record.TargetType)
			require.Equal(t, policyID, record.TargetID)
			require.Equal(t, memberID, record.MemberID)
			require.Equal(t, sharedtelemetry.ActorKindMember, record.Kind)
			require.Equal(t, []string{"locale_content"}, record.ChangedFields)
			require.Equal(t, "ko", record.Locale)
			require.Equal(t, testCase.operation, record.ItemOperation)
			require.Equal(t, testCase.policyType, record.PolicyType)
			require.NotNil(t, record.VersionNumber)
			require.EqualValues(t, 7, *record.VersionNumber)
			require.Empty(t, record.ContributorMemberIDs)
		})
	}
}
