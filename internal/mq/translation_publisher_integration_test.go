//go:build integration

package mq

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/stretchr/testify/require"
)

func TestTranslationCommandSharesDomainTransactionVisibilityIntegration(t *testing.T) {
	t.Parallel()
	queue := eventpkg.QueueTranslationGenerate
	pg := newMQIntegrationPostgres(t, queue)
	ctx := t.Context()
	_, err := pg.SQLDB.ExecContext(ctx, `CREATE TABLE translation_enqueue_probe (id TEXT PRIMARY KEY)`)
	require.NoError(t, err)

	for _, commit := range []bool{false, true} {
		jobID := "rollback-job"
		if commit {
			jobID = "commit-job"
		}
		tx := pg.DB.WithContext(ctx).Begin()
		require.NoError(t, tx.Error)
		t.Cleanup(func() { _ = tx.Rollback().Error })
		require.NoError(t, tx.Exec(`INSERT INTO translation_enqueue_probe (id) VALUES (?)`, jobID).Error)
		require.NoError(t, (&Publisher{}).EnqueueTranslationGenerateWithDB(ctx, tx, &managev1.TranslationGenerateEvent{JobId: jobID}))
		// A separate worker connection must see neither the job nor its command
		// while the producer is still constructing a multi-locale transaction.
		var visibleJobs int
		require.NoError(t, pg.SQLDB.QueryRowContext(ctx, `SELECT count(*) FROM translation_enqueue_probe WHERE id = $1`, jobID).Scan(&visibleJobs))
		require.Zero(t, visibleJobs)
		messages, err := testutil.ReadPGMQ(ctx, pg.SQLDB, queue, time.Minute, 1)
		require.NoError(t, err)
		require.Empty(t, messages)

		if commit {
			require.NoError(t, tx.Commit().Error)
		} else {
			require.NoError(t, tx.Rollback().Error)
		}
		require.NoError(t, pg.SQLDB.QueryRowContext(ctx, `SELECT count(*) FROM translation_enqueue_probe WHERE id = $1`, jobID).Scan(&visibleJobs))
		messages, err = testutil.ReadPGMQ(ctx, pg.SQLDB, queue, time.Minute, 1)
		require.NoError(t, err)
		if commit {
			require.Equal(t, 1, visibleJobs)
			require.Len(t, messages, 1)
			require.Equal(t, jobID, messages[0].Envelope.MessageID)
			require.NoError(t, testutil.CompletePGMQ(ctx, pg.SQLDB, queue, messages[0].TransportID))
		} else {
			require.Zero(t, visibleJobs)
			require.Empty(t, messages)
		}
	}
}
