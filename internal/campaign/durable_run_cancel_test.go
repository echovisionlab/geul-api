package campaign

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCampaignDeliveryRuntimeCancelsOnlyScheduledRuns(t *testing.T) {
	db := campaignCancelTestDB(t)
	require.NoError(t, db.Exec(`INSERT INTO campaign (id) VALUES ('campaign-1')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO email_delivery_run (id, campaign_id, status) VALUES ('scheduled', 'campaign-1', 'scheduled'), ('terminal', 'campaign-1', 'sent')`).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return (&CampaignDeliveryRuntime{}).CancelActiveRuns(context.Background(), tx, "campaign-1", time.Now().UTC())
	}))
	require.Equal(t, CampaignDeliveryRunStatusCancelled, campaignCancelRunStatus(t, db, "scheduled"))
	require.Equal(t, CampaignDeliveryRunStatusSent, campaignCancelRunStatus(t, db, "terminal"))
}

func TestCampaignDeliveryRuntimeRejectsSendingRun(t *testing.T) {
	db := campaignCancelTestDB(t)
	require.NoError(t, db.Exec(`INSERT INTO campaign (id) VALUES ('campaign-1')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO email_delivery_run (id, campaign_id, status) VALUES ('sending', 'campaign-1', 'sending')`).Error)
	err := db.Transaction(func(tx *gorm.DB) error {
		return (&CampaignDeliveryRuntime{}).CancelActiveRuns(context.Background(), tx, "campaign-1", time.Now().UTC())
	})
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Equal(t, CampaignDeliveryRunStatusSending, campaignCancelRunStatus(t, db, "sending"))
}

func campaignCancelTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE campaign (id TEXT PRIMARY KEY); CREATE TABLE email_delivery_run (id TEXT PRIMARY KEY, campaign_id TEXT, status TEXT NOT NULL, completed_at DATETIME, updated_at DATETIME)`).Error)
	return db
}

func campaignCancelRunStatus(t *testing.T, db *gorm.DB, id string) string {
	t.Helper()
	var status string
	require.NoError(t, db.Table("email_delivery_run").Where("id = ?", id).Pluck("status", &status).Error)
	return status
}
