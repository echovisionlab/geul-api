//go:build integration

package emaildelivery

import (
	"database/sql"
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestMailEventStorageMigrationBoundaryIntegration(t *testing.T) {
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB

	var emailEventTable sql.NullString
	require.NoError(t, db.Raw(`SELECT to_regclass(?)`, "public.email_event").Scan(&emailEventTable).Error)
	require.False(t, emailEventTable.Valid, "email_event must not exist after current migrations")

	var suppressionTable sql.NullString
	require.NoError(t, db.Raw(`SELECT to_regclass(?)`, "public.email_suppression").Scan(&suppressionTable).Error)
	require.True(t, suppressionTable.Valid, "email_suppression must remain the durable suppression store")
	require.Equal(t, "email_suppression", suppressionTable.String)

	var providerMessageIDColumnCount int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'email_delivery_recipient' AND column_name = 'provider_message_id'`).Scan(&providerMessageIDColumnCount).Error)
	require.EqualValues(t, 1, providerMessageIDColumnCount)

	for _, removedRelation := range []string{"public.email_send_outbox", "public.auth_email_command_fence"} {
		var relation sql.NullString
		require.NoError(t, db.Raw(`SELECT to_regclass(?)`, removedRelation).Scan(&relation).Error)
		require.False(t, relation.Valid, "%s must not exist after the durable queue cutover", removedRelation)
	}

	var removedTransportColumnCount int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'email_delivery_recipient' AND column_name IN ('attempts', 'next_attempt_at', 'claimed_at', 'claim_token', 'last_error', 'queued_at', 'rendering_at', 'sending_at', 'published_at', 'sent_at')`).Scan(&removedTransportColumnCount).Error)
	require.Zero(t, removedTransportColumnCount)

	var removedCampaignTransportColumnCount int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'email_delivery_run' AND column_name IN ('fanout_started_at', 'fanout_completed_at', 'fanout_error', 'queued_count')`).Scan(&removedCampaignTransportColumnCount).Error)
	require.Zero(t, removedCampaignTransportColumnCount)
}
