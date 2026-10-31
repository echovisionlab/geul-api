package campaign

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/emailcontent"

	"github.com/echovisionlab/geul-api/internal/testutil/sqlitetest"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type campaignLocaleAuditCapture struct {
	record sharedtelemetry.AuditRecord
	err    error
}

func (capture *campaignLocaleAuditCapture) AppendDomainAuditInTransaction(
	_ context.Context,
	_ *gorm.DB,
	record sharedtelemetry.AuditRecord,
) error {
	capture.record = record
	return capture.err
}

func TestCampaignAIDocumentLocaleAuditIsMemberOwnedAndMinimal(t *testing.T) {
	t.Parallel()
	memberID := uuid.NewString()
	capture := &campaignLocaleAuditCapture{}
	require.NoError(t, appendCampaignLocaleContentAudit(
		t.Context(), nil, capture, memberID, "campaign-1", "ja",
		sharedtelemetry.AuditItemOperationUpdated,
	))
	require.NoError(t, capture.record.Validate())
	require.Equal(t, sharedtelemetry.AuditCampaignUpdated, capture.record.Action)
	require.Equal(t, "campaign", capture.record.TargetType)
	require.Equal(t, memberID, capture.record.MemberID)
	require.Equal(t, sharedtelemetry.ActorKindMember, capture.record.Kind)
	require.Equal(t, []string{"locale_content"}, capture.record.ChangedFields)
	require.Equal(t, "ja", capture.record.Locale)
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, capture.record.ItemOperation)
	require.Empty(t, capture.record.ContributorMemberIDs)
}

func TestCampaignLocaleAuditFailureRollsBackOwningTransaction(t *testing.T) {
	db, err := sqlitetest.Open(t, sqlite.Open, &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE mutation_fact (id TEXT PRIMARY KEY)`).Error)
	want := errors.New("append failed")
	capture := &campaignLocaleAuditCapture{err: want}

	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO mutation_fact (id) VALUES ('campaign-1')`).Error; err != nil {
			return err
		}
		return appendCampaignLocaleContentAudit(
			t.Context(), tx, capture, uuid.NewString(), "campaign-1", "ja",
			sharedtelemetry.AuditItemOperationCreated,
		)
	})
	require.ErrorIs(t, err, want)
	var count int64
	require.NoError(t, db.Table("mutation_fact").Count(&count).Error)
	require.Zero(t, count)
}

func TestCampaignLocaleAuditOperationDistinguishesSourceAndTargetCRUD(t *testing.T) {
	t.Parallel()
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, emailcontent.LocaleContentOperation(true, false, false, true))
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, emailcontent.LocaleContentOperation(false, true, false, false))
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, emailcontent.LocaleContentOperation(false, false, false, false))
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, emailcontent.LocaleContentOperation(false, false, false, true))
	require.Equal(t, sharedtelemetry.AuditItemOperationDeleted, emailcontent.LocaleContentOperation(false, false, true, true))
}
