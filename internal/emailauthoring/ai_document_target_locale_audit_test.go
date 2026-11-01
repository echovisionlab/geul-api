package emailauthoring

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"

	"github.com/echovisionlab/geul-api/internal/testutil/sqlitetest"
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

// runLocaleAudit commits change through the mutation pipeline with record as
// its audit.
func runLocaleAudit(t *testing.T, capture *emailAuthoringLocaleAuditCapture, record mutation.Audit, change func(*gorm.DB) error) (*gorm.DB, error) {
	t.Helper()
	db, err := sqlitetest.Open(t, sqlite.Open, &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE mutation_fact (id TEXT PRIMARY KEY)`).Error)
	runner, err := mutation.NewRunner(db, &auth.SpiceDBClient{}, identitystate.PostgresFence{}, capture)
	require.NoError(t, err)
	_, err = mutation.Run(t.Context(), runner, mutation.Spec[struct{}]{
		Access: emailcontent.CollaborationAccess,
		Change: func(_ context.Context, tx *gorm.DB) (struct{}, error) { return struct{}{}, change(tx) },
		Audit:  func(struct{}) mutation.Audit { return record },
	})
	return db, err
}

func TestEmailAuthoringAIDocumentLocaleAuditsAreMemberOwnedAndMinimal(t *testing.T) {
	t.Parallel()

	memberID := uuid.NewString()
	tests := []struct {
		name       string
		action     sharedtelemetry.AuditAction
		targetType string
		record     mutation.Audit
	}{
		{
			name: "Email Template", action: sharedtelemetry.AuditEmailTemplateUpdated,
			targetType: "email_template",
			record:     emailTemplateContent.LocaleContentChange(memberID, "template-1", "ko", sharedtelemetry.AuditItemOperationUpdated),
		},
		{
			name: "Email Layout", action: sharedtelemetry.AuditEmailLayoutUpdated,
			targetType: "email_layout",
			record:     emailLayoutLocaleContentChange(memberID, "layout-1", "ko", sharedtelemetry.AuditItemOperationUpdated),
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			capture := &emailAuthoringLocaleAuditCapture{}
			_, err := runLocaleAudit(t, capture, test.record, func(*gorm.DB) error { return nil })
			require.NoError(t, err)
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
	want := errors.New("append failed")
	capture := &emailAuthoringLocaleAuditCapture{err: want}
	record := emailLayoutLocaleContentChange(uuid.NewString(), "layout-1", "ko", sharedtelemetry.AuditItemOperationCreated)
	db, err := runLocaleAudit(t, capture, record, func(tx *gorm.DB) error {
		return tx.Exec(`INSERT INTO mutation_fact (id) VALUES ('layout-1')`).Error
	})
	require.ErrorIs(t, err, want)
	var count int64
	require.NoError(t, db.Table("mutation_fact").Count(&count).Error)
	require.Zero(t, count)
}

func TestEmailAuthoringLocaleAuditOperationDistinguishesSourceAndTargetCRUD(t *testing.T) {
	t.Parallel()
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, emailcontent.LocaleContentOperation(true, false, false, true))
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, emailcontent.LocaleContentOperation(false, true, false, false))
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, emailcontent.LocaleContentOperation(false, false, false, false))
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, emailcontent.LocaleContentOperation(false, false, false, true))
	require.Equal(t, sharedtelemetry.AuditItemOperationDeleted, emailcontent.LocaleContentOperation(false, false, true, true))
}
