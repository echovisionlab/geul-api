package emailauthoring

import (
	"context"
	"errors"
	"testing"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type emailAuthoringLocaleAuditCapture struct {
	record sharedtelemetry.AuditRecord
	err    error
}

func (capture *emailAuthoringLocaleAuditCapture) AppendDomainAuditInTransaction(
	_ context.Context,
	_ *gorm.DB,
	record sharedtelemetry.AuditRecord,
) error {
	capture.record = record
	return capture.err
}

func TestEmailAuthoringAIDocumentLocaleAuditsAreMemberOwnedAndMinimal(t *testing.T) {
	t.Parallel()

	memberID := uuid.NewString()
	tests := []struct {
		name       string
		action     sharedtelemetry.AuditAction
		targetType string
		append     func(*emailAuthoringLocaleAuditCapture) error
	}{
		{
			name: "Email Template", action: sharedtelemetry.AuditEmailTemplateUpdated,
			targetType: "email_template",
			append: func(capture *emailAuthoringLocaleAuditCapture) error {
				return appendEmailTemplateLocaleContentAudit(
					t.Context(), nil, capture, memberID, "template-1", "ko",
					sharedtelemetry.AuditItemOperationUpdated,
				)
			},
		},
		{
			name: "Email Layout", action: sharedtelemetry.AuditEmailLayoutUpdated,
			targetType: "email_layout",
			append: func(capture *emailAuthoringLocaleAuditCapture) error {
				return appendEmailLayoutLocaleContentAudit(
					t.Context(), nil, capture, memberID, "layout-1", "ko",
					sharedtelemetry.AuditItemOperationUpdated,
				)
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			capture := &emailAuthoringLocaleAuditCapture{}
			require.NoError(t, test.append(capture))
			require.NoError(t, capture.record.Validate())
			require.Equal(t, test.action, capture.record.Action)
			require.Equal(t, test.targetType, capture.record.TargetType)
			require.Equal(t, memberID, capture.record.MemberID)
			require.Equal(t, sharedtelemetry.ActorKindMember, capture.record.Kind)
			require.Equal(t, []string{"locale_content"}, capture.record.ChangedFields)
			require.Equal(t, "ko", capture.record.Locale)
			require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, capture.record.ItemOperation)
			require.Empty(t, capture.record.ContributorMemberIDs)
		})
	}
}

func TestEmailAuthoringLocaleAuditFailureRollsBackOwningTransaction(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE mutation_fact (id TEXT PRIMARY KEY)`).Error)
	want := errors.New("append failed")
	capture := &emailAuthoringLocaleAuditCapture{err: want}

	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO mutation_fact (id) VALUES ('layout-1')`).Error; err != nil {
			return err
		}
		return appendEmailLayoutLocaleContentAudit(
			t.Context(), tx, capture, uuid.NewString(), "layout-1", "ko",
			sharedtelemetry.AuditItemOperationCreated,
		)
	})
	require.ErrorIs(t, err, want)
	var count int64
	require.NoError(t, db.Table("mutation_fact").Count(&count).Error)
	require.Zero(t, count)
}

func TestEmailAuthoringLocaleAuditOperationDistinguishesSourceAndTargetCRUD(t *testing.T) {
	t.Parallel()
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, emailAuthoringLocaleContentOperation(true, false, false, true))
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, emailAuthoringLocaleContentOperation(false, true, false, false))
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, emailAuthoringLocaleContentOperation(false, false, false, false))
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, emailAuthoringLocaleContentOperation(false, false, false, true))
	require.Equal(t, sharedtelemetry.AuditItemOperationDeleted, emailAuthoringLocaleContentOperation(false, false, true, true))
}
