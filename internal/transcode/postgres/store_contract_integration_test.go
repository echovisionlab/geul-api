//go:build integration

package transcodepostgres_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	"github.com/echovisionlab/geul-api/internal/transcode"
	transcodepostgres "github.com/echovisionlab/geul-api/internal/transcode/postgres"
	"github.com/echovisionlab/geul-api/internal/transcode/transcodetest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func newDB(t *testing.T) *gorm.DB {
	t.Helper()
	return pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
}

func TestPostgresStoreKeepsTheStoreContractIntegration(t *testing.T) {
	t.Parallel()
	transcodetest.RunStoreContract(t, func(t *testing.T) transcodetest.Harness {
		db := newDB(t)
		return transcodetest.Harness{
			Harness: mutationtest.PostgresHarness(db, transcodepostgres.NewUnits(db)),
			SeedWaveformJob: func(t *testing.T, job transcode.WaveformJob) {
				require.NoError(t, db.Create(&model.WaveformJob{
					EventID: job.EventID, EntityType: job.EntityType, EntityID: job.EntityID, FileID: job.FileID,
					Status: job.Status, CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
				}).Error)
			},
		}
	})
}

// Registration joins the caller's transaction: a File ingest that rolls back
// leaves no job behind.
func TestRegistrationRollsBackWithTheCallersTransactionIntegration(t *testing.T) {
	t.Parallel()
	db := newDB(t)
	tracker := transcodepostgres.NewTracker(db, transcodetest.NopPublisher{})
	audio, video := transcodetest.AudioJob(t, uuid.NewString()), transcodetest.VideoJob(t, uuid.NewString())
	rollback := errors.New("rollback File ingest allocation")

	err := db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tracker.RegisterTranscodeAudio(t.Context(), tx, audio))
		require.NoError(t, tracker.RegisterTranscodeVideo(t.Context(), tx, video))
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	var count int64
	require.NoError(t, db.Model(&model.TranscodeJob{}).Where("event_id IN ?", []string{audio.EventId, video.EventId}).Count(&count).Error)
	require.Zero(t, count)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return tracker.RegisterTranscodeAudio(t.Context(), tx, audio)
	}))
	require.NoError(t, db.Model(&model.TranscodeJob{}).Where("event_id = ?", audio.EventId).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.ErrorContains(t, tracker.RegisterTranscodeAudio(t.Context(), nil, audio), "transaction is required")
	require.Equal(t, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, audio.GetEntityType())
}

// PGMQ owns delivery attempts; the job table keeps no broker transport state.
func TestJobSchemaOmitsBrokerTransportColumnsIntegration(t *testing.T) {
	t.Parallel()
	db := newDB(t)
	var columns []string
	require.NoError(t, db.Raw(`
SELECT column_name
FROM information_schema.columns
WHERE table_schema = current_schema()
  AND table_name = 'transcode_job'
  AND column_name IN ('attempts', 'last_heartbeat')
ORDER BY column_name
`).Scan(&columns).Error)
	require.Empty(t, columns)
}
