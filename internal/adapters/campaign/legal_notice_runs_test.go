package campaign

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil/sqlitetest"
)

func TestCancelActiveLegalNoticeDeliveryRunsCancelsOnlyScheduled(t *testing.T) {
	db := legalNoticeCancelTestDB(t)
	require.NoError(t, db.Exec(`INSERT INTO email_delivery_run (id, terms_id, status) VALUES ('scheduled', 'terms-1', 'scheduled'), ('failed', 'terms-1', 'failed')`).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return LegalNoticeRuns{}.CancelScheduledRuns(context.Background(), tx, "terms", "terms-1")
	}))
	require.Equal(t, legalNoticeCancelled, legalNoticeRunStatus(t, db, "scheduled"))
	require.Equal(t, "failed", legalNoticeRunStatus(t, db, "failed"))
}

func TestCancelActiveLegalNoticeDeliveryRunsRejectsStartedRuns(t *testing.T) {
	for _, status := range []string{legalNoticeSending, legalNoticeSent} {
		t.Run(status, func(t *testing.T) {
			db := legalNoticeCancelTestDB(t)
			require.NoError(t, db.Exec(`INSERT INTO email_delivery_run (id, privacy_id, status) VALUES ('run-1', 'privacy-1', ?)`, status).Error)
			err := db.Transaction(func(tx *gorm.DB) error {
				return LegalNoticeRuns{}.CancelScheduledRuns(context.Background(), tx, "privacy", "privacy-1")
			})
			require.Equal(t, connect.CodeFailedPrecondition, errs.CodeOf(err))
			require.Equal(t, status, legalNoticeRunStatus(t, db, "run-1"))
		})
	}
}

func TestPostActivationCleanupCancelsOnlyMatchingScheduledUpdate(t *testing.T) {
	db := legalNoticeCancelTestDB(t)
	require.NoError(t, db.Exec(`INSERT INTO email_delivery_run (id, run_kind, privacy_id, template_event_key, status) VALUES ('update', 'legal_notice', 'privacy-1', 'privacy_update', 'scheduled'), ('effective', 'legal_notice', 'privacy-1', 'privacy_effective', 'scheduled'), ('other', 'legal_notice', 'privacy-2', 'privacy_update', 'scheduled')`).Error)
	LegalNoticeRuns{}.CancelUpdateNoticeAfterActivation(context.Background(), db, "privacy", "privacy-1", time.Now().UTC())
	require.Equal(t, legalNoticeCancelled, legalNoticeRunStatus(t, db, "update"))
	require.Equal(t, legalNoticeScheduled, legalNoticeRunStatus(t, db, "effective"))
	require.Equal(t, legalNoticeScheduled, legalNoticeRunStatus(t, db, "other"))
}

func legalNoticeCancelTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := sqlitetest.Open(t, sqlite.Open, &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE email_delivery_run (id TEXT PRIMARY KEY, run_kind TEXT NOT NULL DEFAULT 'legal_notice', status TEXT NOT NULL, terms_id TEXT, privacy_id TEXT, template_event_key TEXT, completed_at DATETIME, updated_at DATETIME)`).Error)
	return db
}

func legalNoticeRunStatus(t *testing.T, db *gorm.DB, id string) string {
	t.Helper()
	var status string
	require.NoError(t, db.Table("email_delivery_run").Where("id = ?", id).Pluck("status", &status).Error)
	return status
}

func TestPolicyDeletionCancelsOutstandingDeliveryAndPreservesSealedHistory(t *testing.T) {
	for _, kind := range []string{"terms", "privacy"} {
		t.Run(kind, func(t *testing.T) {
			db, err := sqlitetest.Open(t, sqlite.Open, &gorm.Config{})
			require.NoError(t, err)
			for _, sql := range []string{
				`CREATE TABLE email_delivery_run (id TEXT PRIMARY KEY,terms_id TEXT,privacy_id TEXT,status TEXT,definition_sealed BOOLEAN,render_snapshot TEXT,completed_at DATETIME,updated_at DATETIME,skipped_count INTEGER)`,
				`CREATE TABLE email_delivery_recipient (id TEXT PRIMARY KEY,run_id TEXT,status TEXT,error_type TEXT,terminal_at DATETIME,delivery_claim_id TEXT,delivery_claim_expires_at DATETIME,updated_at DATETIME)`,
			} {
				require.NoError(t, db.Exec(sql).Error)
			}
			statuses := []string{"scheduled", "sending", "sent", "failed", "skipped", "cancelled"}
			for _, status := range statuses {
				require.NoError(t, db.Exec("INSERT INTO email_delivery_run (id,"+kind+"_id,status,definition_sealed,render_snapshot,skipped_count) VALUES (?,?,?,true,?,0)", status, "policy", status, `{"sealed":"keep"}`).Error)
				if status == "scheduled" || status == "sending" {
					require.NoError(t, db.Exec(`INSERT INTO email_delivery_recipient (id,run_id,status,delivery_claim_id,delivery_claim_expires_at) VALUES (?,?,'pending','claimed',?)`, status, status, time.Now().Add(time.Hour)).Error)
				}
			}
			require.NoError(t, db.Exec("INSERT INTO email_delivery_run (id,"+kind+"_id,status,definition_sealed,render_snapshot,skipped_count) VALUES ('unrelated','other','sending',true,?,0)", `{"sealed":"keep"}`).Error)
			require.NoError(t, db.Exec(`INSERT INTO email_delivery_recipient (id,run_id,status) VALUES ('already-sent','sending','sent')`).Error)
			// Retrying the cleanup must not double-count skipped recipients.
			for range 2 {
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
					return LegalNoticeRuns{}.StopRunsForDeletedPolicy(t.Context(), tx, kind, "policy")
				}))
			}
			var runs []model.CampaignDeliveryRun
			require.NoError(t, db.Order("id").Find(&runs).Error)
			require.Len(t, runs, 7)
			for _, run := range runs {
				require.True(t, run.DefinitionSealed)
				require.Equal(t, model.JSONFields{"sealed": "keep"}, run.RenderSnapshot)
				switch run.ID {
				case "scheduled", "sending":
					require.Equal(t, "cancelled", run.Status)
					require.Equal(t, 1, run.SkippedCount)
					require.NotNil(t, run.CompletedAt)
				case "unrelated":
					require.Equal(t, "sending", run.Status)
				default:
					require.Equal(t, run.ID, run.Status)
				}
			}
			var recipients []model.CampaignDeliveryRecipient
			require.NoError(t, db.Find(&recipients).Error)
			require.Len(t, recipients, 3)
			for _, recipient := range recipients {
				if recipient.ID == "already-sent" {
					require.Equal(t, "sent", recipient.Status)
					continue
				}
				require.Equal(t, "skipped", recipient.Status)
				require.Equal(t, "policy_deleted", *recipient.ErrorType)
				require.Nil(t, recipient.DeliveryClaimID)
				require.Nil(t, recipient.DeliveryClaimExpiresAt)
				require.NotNil(t, recipient.TerminalAt)
			}
		})
	}
}

func TestPolicyDeletionDeliveryCleanupRollsBackWithTransaction(t *testing.T) {
	db, err := sqlitetest.Open(t, sqlite.Open, &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE email_delivery_run (id TEXT PRIMARY KEY,terms_id TEXT,status TEXT,completed_at DATETIME,updated_at DATETIME,skipped_count INTEGER)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE email_delivery_recipient (id TEXT PRIMARY KEY,run_id TEXT,status TEXT,error_type TEXT,terminal_at DATETIME,delivery_claim_id TEXT,delivery_claim_expires_at DATETIME,updated_at DATETIME)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO email_delivery_run (id,terms_id,status,skipped_count) VALUES ('run','policy','sending',0)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO email_delivery_recipient (id,run_id,status) VALUES ('recipient','run','pending')`).Error)
	failure := errors.New("later policy deletion failed")
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := (LegalNoticeRuns{}).StopRunsForDeletedPolicy(t.Context(), tx, "terms", "policy"); err != nil {
			return err
		}
		return failure
	})
	require.ErrorIs(t, err, failure)
	var run model.CampaignDeliveryRun
	require.NoError(t, db.First(&run, "id = ?", "run").Error)
	require.Equal(t, "sending", run.Status)
	require.Zero(t, run.SkippedCount)
	var recipient model.CampaignDeliveryRecipient
	require.NoError(t, db.First(&recipient, "id = ?", "recipient").Error)
	require.Equal(t, "pending", recipient.Status)
}
