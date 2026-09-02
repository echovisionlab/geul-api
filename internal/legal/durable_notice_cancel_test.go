package legal

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCancelActiveLegalNoticeDeliveryRunsCancelsOnlyScheduled(t *testing.T) {
	db := legalNoticeCancelTestDB(t)
	require.NoError(t, db.Exec(`INSERT INTO email_delivery_run (id, terms_id, status) VALUES ('scheduled', 'terms-1', 'scheduled'), ('failed', 'terms-1', 'failed')`).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return cancelActiveLegalNoticeDeliveryRuns(context.Background(), tx, EmailDeliveryReferenceTypeTerms, "terms-1")
	}))
	require.Equal(t, CampaignDeliveryRunStatusCancelled, legalNoticeRunStatus(t, db, "scheduled"))
	require.Equal(t, "failed", legalNoticeRunStatus(t, db, "failed"))
}

func TestCancelActiveLegalNoticeDeliveryRunsRejectsStartedRuns(t *testing.T) {
	for _, status := range []string{CampaignDeliveryRunStatusSending, CampaignDeliveryRunStatusSent} {
		t.Run(status, func(t *testing.T) {
			db := legalNoticeCancelTestDB(t)
			require.NoError(t, db.Exec(`INSERT INTO email_delivery_run (id, privacy_id, status) VALUES ('run-1', 'privacy-1', ?)`, status).Error)
			err := db.Transaction(func(tx *gorm.DB) error {
				return cancelActiveLegalNoticeDeliveryRuns(context.Background(), tx, EmailDeliveryReferenceTypePrivacy, "privacy-1")
			})
			require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			require.Equal(t, status, legalNoticeRunStatus(t, db, "run-1"))
		})
	}
}

func TestPostActivationCleanupCancelsOnlyMatchingScheduledUpdate(t *testing.T) {
	db := legalNoticeCancelTestDB(t)
	require.NoError(t, db.Exec(`INSERT INTO email_delivery_run (id, run_kind, privacy_id, template_event_key, status) VALUES ('update', 'legal_notice', 'privacy-1', 'privacy_update', 'scheduled'), ('effective', 'legal_notice', 'privacy-1', 'privacy_effective', 'scheduled'), ('other', 'legal_notice', 'privacy-2', 'privacy_update', 'scheduled')`).Error)
	cancelScheduledLegalUpdateNoticeAfterActivation(context.Background(), db, EmailDeliveryReferenceTypePrivacy, "privacy-1", time.Now().UTC())
	require.Equal(t, CampaignDeliveryRunStatusCancelled, legalNoticeRunStatus(t, db, "update"))
	require.Equal(t, CampaignDeliveryRunStatusScheduled, legalNoticeRunStatus(t, db, "effective"))
	require.Equal(t, CampaignDeliveryRunStatusScheduled, legalNoticeRunStatus(t, db, "other"))
}

func legalNoticeCancelTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
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
