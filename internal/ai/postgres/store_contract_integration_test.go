//go:build integration

package aipostgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/ai"
	"github.com/echovisionlab/geul-api/internal/ai/aitest"
	aipostgres "github.com/echovisionlab/geul-api/internal/ai/postgres"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"

	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

func openStore(t *testing.T) (*gorm.DB, *mq.Publisher) {
	t.Helper()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
	sqlDB, err := db.DB()
	require.NoError(t, err)
	publisher, err := mq.NewPublisher(sqlDB)
	require.NoError(t, err)
	return db, publisher
}

func TestPostgresStoreKeepsTheMetadataAIStoreContractIntegration(t *testing.T) {
	t.Parallel()
	aitest.RunStoreContract(t, func(t *testing.T) aitest.Harness {
		db, publisher := openStore(t)
		units, err := aipostgres.NewUnits(db, publisher)
		require.NoError(t, err)
		return aitest.Harness{
			Harness: mutationtest.PostgresHarness(db, units),
			SeedMember: func(t *testing.T) string {
				return orytest.SeedPostIntegrationIdentity(t, db, uuid.NewString(), "Metadata requester")
			},
			SeedTransport: func(t *testing.T, jobID string) {
				require.NoError(t, publisher.EnqueueProtobuf(t.Context(), eventpkg.QueueAiMetadataGenerate, jobID,
					&managev1.MetadataGenerationQueueEvent{JobId: jobID}))
			},
		}
	})
}

// A recovery skips the jobs another recovery holds instead of waiting for
// them, so two scheduler ticks never republish one job twice.
func TestRecoverySkipsTheJobsAnotherRecoveryLockedIntegration(t *testing.T) {
	t.Parallel()
	db, publisher := openStore(t)
	units, err := aipostgres.NewUnits(db, publisher)
	require.NoError(t, err)
	h := mutationtest.PostgresHarness(db, units)
	member := orytest.SeedPostIntegrationIdentity(t, db, uuid.NewString(), "Metadata requester")
	now := time.Now().UTC().Truncate(time.Microsecond)
	var job ai.Job
	require.NoError(t, mutationtest.Transact(t, h, func(ctx context.Context, store ai.Store) (err error) {
		job, err = store.InsertJob(ctx, aitest.NewJob(member))
		if err != nil {
			return err
		}
		_, err = store.TouchExpiredJob(ctx, job, now.Add(-time.Hour))
		return err
	}))

	require.NoError(t, mutationtest.Transact(t, h, func(ctx context.Context, store ai.Store) error {
		held, err := store.LockExpiredJobs(ctx, now.Add(-10*time.Minute), now.Add(-3*time.Minute), 25)
		require.NoError(t, err)
		require.Len(t, held, 1)
		return mutationtest.Transact(t, h, func(ctx context.Context, other ai.Store) error {
			skipped, err := other.LockExpiredJobs(ctx, now.Add(-10*time.Minute), now.Add(-3*time.Minute), 25)
			require.Empty(t, skipped)
			return err
		})
	}))
}
