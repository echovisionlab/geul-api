package campaign

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestRequireTranslationSourceMutableOwnsCampaignAndDeliveryFences(t *testing.T) {
	db := newCampaignTranslationMutationTestDB(t)
	ctx := context.Background()

	require.NoError(t, insertCampaignTranslationMutationRoot(
		db,
		"draft",
		managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(),
	))
	require.NoError(t, RequireTranslationSourceMutable(ctx, db, "draft"))

	require.NoError(t, insertCampaignTranslationMutationRoot(
		db,
		"scheduled-campaign",
		managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED.String(),
	))
	err := RequireTranslationSourceMutable(ctx, db, "scheduled-campaign")
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	require.NoError(t, insertCampaignTranslationMutationRoot(
		db,
		"active-run",
		managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(),
	))
	require.NoError(t, db.Exec(
		"INSERT INTO email_delivery_run (id, campaign_id, status) VALUES (?, ?, ?)",
		"run-scheduled",
		"active-run",
		CampaignDeliveryRunStatusScheduled,
	).Error)
	err = RequireTranslationSourceMutable(ctx, db, "active-run")
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	require.NoError(t, insertCampaignTranslationMutationRoot(
		db,
		"terminal-run",
		managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(),
	))
	require.NoError(t, db.Exec(
		"INSERT INTO email_delivery_run (id, campaign_id, status) VALUES (?, ?, ?)",
		"run-sent",
		"terminal-run",
		CampaignDeliveryRunStatusSent,
	).Error)
	require.NoError(t, RequireTranslationSourceMutable(ctx, db, "terminal-run"))

	err = RequireTranslationSourceMutable(ctx, db, "missing")
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func newCampaignTranslationMutationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE campaign (
			id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			updated_at DATETIME NOT NULL
		)
	`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE email_delivery_run (
			id TEXT PRIMARY KEY,
			campaign_id TEXT,
			status TEXT NOT NULL
		)
	`).Error)
	return db
}

func insertCampaignTranslationMutationRoot(
	db *gorm.DB,
	id string,
	status string,
) error {
	return db.Exec(
		"INSERT INTO campaign (id, status, updated_at) VALUES (?, ?, ?)",
		id,
		status,
		time.Unix(0, 0).UTC(),
	).Error
}
