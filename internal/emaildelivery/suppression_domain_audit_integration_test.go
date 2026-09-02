//go:build integration

package emaildelivery

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEmailSuppressionReleaseDomainAuditIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	baseContext, spiceDB := testutil.IntegrationAdminContext(t, db)
	admin := auth.GetUser(baseContext)
	require.NotNil(t, admin)
	ctx := testutil.NewAuditContext(t, string(admin.IdentityID), string(admin.MemberID))

	t.Run("release stores exact non-PII transition and repeat is unaudited", func(t *testing.T) {
		suppression := seedSuppressionReleaseAuditFixture(t, db, "suppressed-audit@example.test")
		service := NewAuditedEmailSuppressionService(
			db, apitelemetry.NewDurableWriter(db), spiceDB,
		)
		request := connect.NewRequest(&managev1.ReleaseEmailSuppressionRequest{Email: suppression.Email})
		_, err := service.ReleaseEmailSuppression(ctx, request)
		require.NoError(t, err)
		_, err = service.ReleaseEmailSuppression(ctx, request)
		require.NoError(t, err)

		var rows []emailSuppressionAuditRow
		require.NoError(t, db.Table("public.domain_audit").
			Select("action, target_type, target_id, attributes").
			Where("action = ? AND target_id = ?", sharedtelemetry.AuditEmailSuppressionUpdated, suppression.ID).
			Find(&rows).Error)
		require.Len(t, rows, 1)
		row := rows[0]
		require.Equal(t, string(sharedtelemetry.AuditEmailSuppressionUpdated), row.Action)
		require.Equal(t, "email_suppression", row.TargetType)
		require.Equal(t, suppression.ID, row.TargetID)
		require.JSONEq(t, `{
			"changed_fields":["status"],
			"previous_state":"active",
			"new_state":"released"
		}`, string(row.Attributes))
	})

	t.Run("audit failure rolls back release", func(t *testing.T) {
		suppression := seedSuppressionReleaseAuditFixture(t, db, "suppressed-rollback@example.test")
		_, err := NewAuditedEmailSuppressionService(
			db, failingSuppressionAuditAppender{}, spiceDB,
		).ReleaseEmailSuppression(ctx, connect.NewRequest(
			&managev1.ReleaseEmailSuppressionRequest{Email: suppression.Email},
		))
		require.Error(t, err)
		require.Equal(t, connect.CodeInternal, connect.CodeOf(err))

		var persisted model.EmailSuppression
		require.NoError(t, db.First(&persisted, "id = ?", suppression.ID).Error)
		require.Nil(t, persisted.ReleasedAt)
		require.Nil(t, persisted.ReleasedBy)

		var auditCount int64
		require.NoError(t, db.Table("public.domain_audit").
			Where("action = ? AND target_id = ?", sharedtelemetry.AuditEmailSuppressionUpdated, suppression.ID).
			Count(&auditCount).Error)
		require.Zero(t, auditCount)
	})
}

type emailSuppressionAuditRow struct {
	Action     string
	TargetType string `gorm:"column:target_type"`
	TargetID   string `gorm:"column:target_id"`
	Attributes []byte
}

func seedSuppressionReleaseAuditFixture(t *testing.T, db *gorm.DB, email string) model.EmailSuppression {
	t.Helper()
	suppression := model.EmailSuppression{
		ID:           uuid.NewString(),
		Email:        email,
		Reason:       EmailSuppressionReasonManual,
		Source:       EmailSuppressionSourceAdmin,
		SuppressedAt: time.Now().UTC(),
	}
	require.NoError(t, db.Create(&suppression).Error)
	return suppression
}

type failingSuppressionAuditAppender struct{}

func (failingSuppressionAuditAppender) AppendDomainAuditInTransaction(
	context.Context,
	*gorm.DB,
	sharedtelemetry.AuditRecord,
) error {
	return errors.New("audit unavailable")
}

var _ domainaudit.Appender = failingSuppressionAuditAppender{}
