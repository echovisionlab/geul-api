package release

import (
	"context"
	"testing"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type releaseLocaleAuditCapture struct {
	record sharedtelemetry.AuditRecord
}

func (capture *releaseLocaleAuditCapture) AppendDomainAuditInTransaction(
	_ context.Context,
	_ *gorm.DB,
	record sharedtelemetry.AuditRecord,
) error {
	capture.record = record
	return nil
}

func TestReleaseTargetLocaleAuditIsMemberOwnedAndMinimal(t *testing.T) {
	memberID := uuid.NewString()
	releaseID := uuid.NewString()
	capture := &releaseLocaleAuditCapture{}
	require.NoError(t, appendReleaseMemberTargetLocaleAudit(
		t.Context(), nil, capture, memberID, releaseID, "ja", sharedtelemetry.AuditItemOperationUpdated,
	))
	require.NoError(t, capture.record.Validate())
	require.Equal(t, sharedtelemetry.AuditReleaseUpdated, capture.record.Action)
	require.Equal(t, "release", capture.record.TargetType)
	require.Equal(t, memberID, capture.record.MemberID)
	require.Equal(t, sharedtelemetry.ActorKindMember, capture.record.Kind)
	require.Equal(t, []string{"locale_content"}, capture.record.ChangedFields)
	require.Equal(t, "ja", capture.record.Locale)
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, capture.record.ItemOperation)
	require.Empty(t, capture.record.ContributorMemberIDs)
}

func TestReleaseTargetLocaleAuditOperationReflectsCRUD(t *testing.T) {
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, releaseTargetLocaleAuditOperation(true, false, false))
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, releaseTargetLocaleAuditOperation(false, false, false))
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, releaseTargetLocaleAuditOperation(false, false, true))
	require.Equal(t, sharedtelemetry.AuditItemOperationDeleted, releaseTargetLocaleAuditOperation(false, true, true))
}
