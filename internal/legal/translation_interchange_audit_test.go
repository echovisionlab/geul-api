package legal

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type legalTranslationInterchangeAuditCapture struct {
	record sharedtelemetry.AuditRecord
}

func (capture *legalTranslationInterchangeAuditCapture) AppendDomainAuditInTransaction(
	_ context.Context,
	_ *gorm.DB,
	record sharedtelemetry.AuditRecord,
) error {
	capture.record = record
	return nil
}

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
			capture := &legalTranslationInterchangeAuditCapture{}
			require.NoError(t, appendLegalTargetLocaleContentAudit(
				t.Context(), nil, capture, memberID, testCase.entityType,
				policyID, 7, "ko", testCase.operation,
			))
			require.NoError(t, capture.record.Validate())
			require.Equal(t, sharedtelemetry.AuditLegalPolicyUpdated, capture.record.Action)
			require.Equal(t, "legal_policy", capture.record.TargetType)
			require.Equal(t, policyID, capture.record.TargetID)
			require.Equal(t, memberID, capture.record.MemberID)
			require.Equal(t, sharedtelemetry.ActorKindMember, capture.record.Kind)
			require.Equal(t, []string{"locale_content"}, capture.record.ChangedFields)
			require.Equal(t, "ko", capture.record.Locale)
			require.Equal(t, testCase.operation, capture.record.ItemOperation)
			require.Equal(t, testCase.policyType, capture.record.PolicyType)
			require.NotNil(t, capture.record.VersionNumber)
			require.EqualValues(t, 7, *capture.record.VersionNumber)
			require.Empty(t, capture.record.ContributorMemberIDs)
		})
	}
}
