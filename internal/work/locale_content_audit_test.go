package work

import (
	"context"
	"testing"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type workLocaleContentAuditCapture struct {
	records []sharedtelemetry.AuditRecord
}

func (capture *workLocaleContentAuditCapture) AppendDomainAuditInTransaction(
	_ context.Context,
	_ *gorm.DB,
	record sharedtelemetry.AuditRecord,
) error {
	capture.records = append(capture.records, record)
	return nil
}

func TestWorkLocaleContentAuditAttributesSourceAndTargetToOriginMember(t *testing.T) {
	memberID := uuid.NewString()
	capture := &workLocaleContentAuditCapture{}
	for _, locale := range []string{"en", "ko"} {
		require.NoError(t, appendWorkMemberLocaleContentAudit(
			t.Context(), nil, capture, memberID, "work-1", locale,
			sharedtelemetry.AuditItemOperationUpdated,
		))
	}
	require.Len(t, capture.records, 2)
	for index, locale := range []string{"en", "ko"} {
		record := capture.records[index]
		require.NoError(t, record.Validate())
		require.Equal(t, sharedtelemetry.AuditWorkUpdated, record.Action)
		require.Equal(t, memberID, record.MemberID)
		require.Equal(t, sharedtelemetry.ActorKindMember, record.Kind)
		require.Equal(t, []string{"locale_content"}, record.ChangedFields)
		require.Equal(t, locale, record.Locale)
	}
}
