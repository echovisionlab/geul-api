//go:build integration

package og_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestOgGenerationNewerCompletionWinsBeforeSupersededCompletionIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	workID := seedOgGenerationConcurrencyWork(t, db)
	planner := newWorkOGPlannerForConcurrencyTest(db)
	first, err := requestOgGenerationForTest(ctx, planner, "automatic", "first", []og.Request{
		ogTestRequest(managev1.OgEntityType_OG_ENTITY_TYPE_WORK, workID, "First", stringPtr("en"), nil),
	})
	require.NoError(t, err)
	firstLease := markOgGenerationProcessingIntegration(t, db, first.RunID, first.GenerationIDs[0])

	second, err := requestOgGenerationForTest(ctx, planner, "automatic", "second", []og.Request{
		ogTestRequest(managev1.OgEntityType_OG_ENTITY_TYPE_WORK, workID, "Second", stringPtr("en"), nil),
	})
	require.NoError(t, err)
	secondLease := markOgGenerationProcessingIntegration(t, db, second.RunID, second.GenerationIDs[0])

	lifecycle := newWorkOGLifecycleForConcurrencyTest(db)
	status, _, err := lifecycle.Complete(ctx, second.GenerationIDs[0], secondLease, validOgWriteResult(second.GenerationIDs[0]))
	require.NoError(t, err)
	require.Equal(t, model.OgGenerationStatusReady, status)
	status, _, err = lifecycle.Complete(ctx, first.GenerationIDs[0], firstLease, validOgWriteResult(first.GenerationIDs[0]))
	require.NoError(t, err)
	require.Equal(t, model.OgGenerationStatusSuperseded, status)

	var workTranslation struct {
		OgAssetID *string `gorm:"column:og_asset_id"`
	}
	require.NoError(t, db.Table("work_translation").First(&workTranslation, "entity_id = ? AND locale = ?", workID, "en").Error)
	require.Equal(t, second.GenerationIDs[0], ptrStringValue(workTranslation.OgAssetID))
	var target model.OgGenerationTarget
	require.NoError(t, db.First(&target, "entity_type = ? AND entity_id = ? AND locale = ?", "work", workID, "en").Error)
	require.Equal(t, second.GenerationIDs[0], ptrStringValue(target.LatestGenerationID))
	var firstBindingCount int64
	require.NoError(t, db.Model(&model.PublicAssetBinding{}).Where("asset_id = ?", first.GenerationIDs[0]).Count(&firstBindingCount).Error)
	assert.Zero(t, firstBindingCount)
}

func TestOgGenerationRequestAndCompletionUseDeadlockFreeTargetFirstLockOrderIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	workID := seedOgGenerationConcurrencyWork(t, db)
	planner := newWorkOGPlannerForConcurrencyTest(db)
	first, err := requestOgGenerationForTest(ctx, planner, "automatic", "overlap-first", []og.Request{
		ogTestRequest(managev1.OgEntityType_OG_ENTITY_TYPE_WORK, workID, "First", stringPtr("en"), nil),
	})
	require.NoError(t, err)
	firstLease := markOgGenerationProcessingIntegration(t, db, first.RunID, first.GenerationIDs[0])

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	completeResult := make(chan error, 1)
	requestResult := make(chan struct {
		plan *og.Plan
		err  error
	}, 1)
	go func() {
		defer wg.Done()
		<-start
		_, _, completeErr := newWorkOGLifecycleForConcurrencyTest(db).Complete(
			ctx,
			first.GenerationIDs[0],
			firstLease,
			validOgWriteResult(first.GenerationIDs[0]),
		)
		completeResult <- completeErr
	}()
	go func() {
		defer wg.Done()
		<-start
		plan, requestErr := requestOgGenerationForTest(ctx, planner, "automatic", "overlap-second", []og.Request{
			ogTestRequest(managev1.OgEntityType_OG_ENTITY_TYPE_WORK, workID, "Second", stringPtr("en"), nil),
		})
		requestResult <- struct {
			plan *og.Plan
			err  error
		}{plan: plan, err: requestErr}
	}()
	close(start)
	wg.Wait()
	require.NoError(t, <-completeResult)
	requested := <-requestResult
	require.NoError(t, requested.err)
	require.NotNil(t, requested.plan)

	secondLease := markOgGenerationProcessingIntegration(t, db, requested.plan.RunID, requested.plan.GenerationIDs[0])
	status, _, err := newWorkOGLifecycleForConcurrencyTest(db).Complete(
		ctx,
		requested.plan.GenerationIDs[0],
		secondLease,
		validOgWriteResult(requested.plan.GenerationIDs[0]),
	)
	require.NoError(t, err)
	require.Equal(t, model.OgGenerationStatusReady, status)

	var workTranslation struct {
		OgAssetID *string `gorm:"column:og_asset_id"`
	}
	require.NoError(t, db.Table("work_translation").
		Where("entity_id = ? AND locale = ?", workID, "en").Take(&workTranslation).Error)
	require.Equal(t, requested.plan.GenerationIDs[0], ptrStringValue(workTranslation.OgAssetID))
	var target model.OgGenerationTarget
	require.NoError(t, db.First(&target, "entity_type = ? AND entity_id = ? AND locale = ?", "work", workID, "en").Error)
	require.Equal(t, requested.plan.GenerationIDs[0], ptrStringValue(target.LatestGenerationID))
}

func newWorkOGPlannerForConcurrencyTest(db *gorm.DB) *og.Planner {
	return og.NewPlanner(db, "https://cdn.example.com", migratedRenderConfig{}, workadapter.NewProjection())
}

func newWorkOGLifecycleForConcurrencyTest(db *gorm.DB) *og.Lifecycle {
	return og.NewLifecycle(db, "https://cdn.example.com", workadapter.NewProjection())
}

func TestOgGenerationConcurrentTerminalUpdatesRefreshRunAfterSerializationIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	firstWorkID := seedOgGenerationConcurrencyWork(t, db)
	secondWorkID := seedOgGenerationConcurrencyWork(t, db)
	plan, err := requestOgGenerationForTest(ctx, newOGPlannerForTest(db, "https://cdn.example.com"), "manual", "two targets", []og.Request{
		ogTestRequest(managev1.OgEntityType_OG_ENTITY_TYPE_WORK, firstWorkID, "First", stringPtr("en"), nil),
		ogTestRequest(managev1.OgEntityType_OG_ENTITY_TYPE_WORK, secondWorkID, "Second", stringPtr("en"), nil),
	})
	require.NoError(t, err)
	leases := []string{
		markOgGenerationProcessingIntegration(t, db, plan.RunID, plan.GenerationIDs[0]),
		markOgGenerationProcessingIntegration(t, db, plan.RunID, plan.GenerationIDs[1]),
	}

	start := make(chan struct{})
	errs := make(chan error, len(plan.GenerationIDs))
	var wg sync.WaitGroup
	for i, generationID := range plan.GenerationIDs {
		i := i
		generationID := generationID
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			status, _, completeErr := newOGLifecycleForTest(db, "https://cdn.example.com").Complete(
				ctx,
				generationID,
				leases[i],
				validOgWriteResult(generationID),
			)
			if completeErr == nil && status != model.OgGenerationStatusReady {
				completeErr = assert.AnError
			}
			errs <- completeErr
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for completeErr := range errs {
		require.NoError(t, completeErr)
	}

	var run model.OgGenerationRun
	require.NoError(t, db.First(&run, "id = ?", plan.RunID).Error)
	require.Equal(t, model.OgGenerationRunStatusReady, run.Status)
	require.NotNil(t, run.CompletedAt)
	var readyCount int64
	require.NoError(t, db.Model(&model.OgGeneration{}).
		Where("run_id = ? AND status = ?", plan.RunID, model.OgGenerationStatusReady).
		Count(&readyCount).Error)
	require.Equal(t, int64(2), readyCount)
}

func TestOgGenerationClaimSerializesBehindRowsOwnedByAnotherReplicaIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	for _, lock := range []struct {
		name  string
		table string
	}{
		{name: "target", table: "og_generation_target"},
		{name: "generation", table: "og_generation"},
	} {
		t.Run(lock.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			workID := seedOgGenerationConcurrencyWork(t, db)
			plan, err := requestOgGenerationForTest(
				ctx,
				newOGPlannerForTest(db, "https://cdn.example.com"),
				"automatic",
				"claim lock serialization",
				[]og.Request{ogTestRequest(
					managev1.OgEntityType_OG_ENTITY_TYPE_WORK,
					workID,
					"Claim serialization",
					stringPtr("en"),
					nil,
				)},
			)
			require.NoError(t, err)
			var generation model.OgGeneration
			require.NoError(t, db.First(&generation, "id = ?", plan.GenerationIDs[0]).Error)
			lockID := generation.ID
			if lock.table == "og_generation_target" {
				lockID = generation.TargetID
			}
			owner := db.WithContext(ctx).Begin()
			require.NoError(t, owner.Error)
			t.Cleanup(func() { _ = owner.Rollback().Error })
			require.NoError(t, owner.Exec("SELECT 1 FROM "+lock.table+" WHERE id = ? FOR UPDATE", lockID).Error)

			claimDB, applicationName := newOGClaimIntegrationDB(t)
			result := make(chan ogClaimIntegrationResult, 1)
			go func() {
				claim, claimErr := newOGLifecycleForTest(claimDB, "https://cdn.example.com").Claim(ctx, generation.ID)
				result <- ogClaimIntegrationResult{claim: claim, err: claimErr}
			}()
			requireOGClaimWaitingOnLock(t, db, applicationName, result)
			require.NoError(t, owner.Rollback().Error)
			outcome := <-result
			require.NoError(t, outcome.err)
			require.Equal(t, og.Claimed, outcome.claim.Result)
		})
	}
}

type ogClaimIntegrationResult struct {
	claim *og.Claim
	err   error
}

func newOGClaimIntegrationDB(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	stack := testutil.PrepareOryIntegrationConcurrentTest(t)
	require.NotNil(t, stack)
	applicationName := "geul_og_claim_" + uuid.NewString()
	db, err := gorm.Open(gormpostgres.Open(stack.PostgresDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.Exec(`SELECT set_config('application_name', ?, false)`, applicationName).Error)
	return db, applicationName
}

func requireOGClaimWaitingOnLock(
	t *testing.T,
	db *gorm.DB,
	applicationName string,
	result <-chan ogClaimIntegrationResult,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case outcome := <-result:
			require.FailNow(t, "OG claim returned before reaching the locked row", "error: %v", outcome.err)
		default:
		}
		var waiting bool
		require.NoError(t, db.Raw(`SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE application_name = ?
			  AND wait_event_type = 'Lock'
			  AND cardinality(pg_blocking_pids(pid)) > 0
		)`, applicationName).Scan(&waiting).Error)
		if waiting {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.FailNow(t, "OG claim did not reach the locked row")
}

func TestOgGenerationDuplicateClaimNeverRewritesTerminalHistoryIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	for _, testCase := range []struct {
		name        string
		terminal    string
		terminalize func(*testing.T, *gorm.DB, *og.Lifecycle, *og.Plan)
	}{
		{
			name: "ready", terminal: model.OgGenerationStatusReady,
			terminalize: func(t *testing.T, db *gorm.DB, lifecycle *og.Lifecycle, plan *og.Plan) {
				lease := markOgGenerationProcessingIntegration(t, db, plan.RunID, plan.GenerationIDs[0])
				status, _, err := lifecycle.Complete(t.Context(), plan.GenerationIDs[0], lease, validOgWriteResult(plan.GenerationIDs[0]))
				require.NoError(t, err)
				require.Equal(t, model.OgGenerationStatusReady, status)
			},
		},
		{
			name: "failed", terminal: model.OgGenerationStatusFailed,
			terminalize: func(t *testing.T, db *gorm.DB, lifecycle *og.Lifecycle, plan *og.Plan) {
				lease := markOgGenerationProcessingIntegration(t, db, plan.RunID, plan.GenerationIDs[0])
				require.NoError(t, lifecycle.Fail(t.Context(), plan.GenerationIDs[0], lease, og.FailureCodeProcessingFailed))
			},
		},
		{
			name: "cancelled", terminal: model.OgGenerationStatusCancelled,
			terminalize: func(t *testing.T, db *gorm.DB, lifecycle *og.Lifecycle, plan *og.Plan) {
				var target model.OgGenerationTarget
				require.NoError(t, db.First(&target, "latest_generation_id = ?", plan.GenerationIDs[0]).Error)
				require.NoError(t, cancelOgGenerationEntityForTest(t.Context(), lifecycle, managev1.OgEntityType_OG_ENTITY_TYPE_WORK, target.EntityID))
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			workID := seedOgGenerationConcurrencyWork(t, db)
			planner := newOGPlannerForTest(db, "https://cdn.example.com")
			first, err := requestOgGenerationForTest(t.Context(), planner, "automatic", "terminal duplicate", []og.Request{
				ogTestRequest(managev1.OgEntityType_OG_ENTITY_TYPE_WORK, workID, "First", stringPtr("en"), nil),
			})
			require.NoError(t, err)
			lifecycle := newOGLifecycleForTest(db, "https://cdn.example.com")
			testCase.terminalize(t, db, lifecycle, first)

			var originalRun model.OgGenerationRun
			require.NoError(t, db.First(&originalRun, "id = ?", first.RunID).Error)
			_, err = requestOgGenerationForTest(t.Context(), planner, "automatic", "replacement", []og.Request{
				ogTestRequest(managev1.OgEntityType_OG_ENTITY_TYPE_WORK, workID, "Second", stringPtr("en"), nil),
			})
			require.NoError(t, err)

			claim, err := lifecycle.Claim(t.Context(), first.GenerationIDs[0])
			require.NoError(t, err)
			require.Equal(t, og.ClaimSkipped, claim.Result)
			require.Equal(t, testCase.terminal, claim.Generation.Status)

			var stored model.OgGeneration
			require.NoError(t, db.First(&stored, "id = ?", first.GenerationIDs[0]).Error)
			require.Equal(t, testCase.terminal, stored.Status)
			var storedRun model.OgGenerationRun
			require.NoError(t, db.First(&storedRun, "id = ?", first.RunID).Error)
			require.Equal(t, originalRun.Status, storedRun.Status)
			require.Equal(t, originalRun.CompletedAt, storedRun.CompletedAt)
		})
	}
}

func TestOgGenerationPartialCancellationStartsStillRunningRunIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	firstWorkID := seedOgGenerationConcurrencyWork(t, db)
	secondWorkID := seedOgGenerationConcurrencyWork(t, db)
	plan, err := requestOgGenerationForTest(
		t.Context(), newOGPlannerForTest(db, "https://cdn.example.com"), "automatic", "partial cancellation", []og.Request{
			ogTestRequest(managev1.OgEntityType_OG_ENTITY_TYPE_WORK, firstWorkID, "First", stringPtr("en"), nil),
			ogTestRequest(managev1.OgEntityType_OG_ENTITY_TYPE_WORK, secondWorkID, "Second", stringPtr("en"), nil),
		},
	)
	require.NoError(t, err)
	require.NoError(t, cancelOgGenerationEntityForTest(
		t.Context(), newOGLifecycleForTest(db, "https://cdn.example.com"), managev1.OgEntityType_OG_ENTITY_TYPE_WORK, firstWorkID,
	))

	var run model.OgGenerationRun
	require.NoError(t, db.First(&run, "id = ?", plan.RunID).Error)
	require.Equal(t, model.OgGenerationRunStatusRunning, run.Status)
	require.NotNil(t, run.StartedAt)
	require.Nil(t, run.CompletedAt)
	var second model.OgGeneration
	require.NoError(t, db.First(&second, "id = ?", plan.GenerationIDs[1]).Error)
	require.Equal(t, model.OgGenerationStatusQueued, second.Status)
}

func seedOgGenerationConcurrencyWork(t *testing.T, db *gorm.DB) string {
	t.Helper()
	documentID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile, revision) VALUES (?, 'work', ?)`,
		documentID,
		uuid.NewString(),
	).Error)
	work := model.Work{
		ID: uuid.NewString(), ContentDocumentID: &documentID, Type: "WORK_TYPE_MUSIC_PROJECT",
		Year: 2026, Month: 7, IsPresent: true, Status: "WORK_STATUS_PUBLISHED", SourceLocale: "en",
	}
	require.NoError(t, db.Create(&work).Error)
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`
		INSERT INTO work_translation (
			entity_id, locale, title, created_at, updated_at
		) VALUES (?, 'en', 'Work', ?, ?)
	`, work.ID, now, now).Error)
	return work.ID
}

func markOgGenerationProcessingIntegration(t *testing.T, db *gorm.DB, runID string, generationID string) string {
	t.Helper()
	now := time.Now().UTC()
	leaseToken := uuid.NewString()
	result := db.Model(&model.OgGeneration{}).
		Where("id = ? AND status = ?", generationID, model.OgGenerationStatusQueued).
		Updates(structured.Fields{
			"status":           model.OgGenerationStatusProcessing,
			"processing_at":    now,
			"lease_token":      leaseToken,
			"lease_expires_at": now.Add(10 * time.Minute),
			"updated_at":       now,
		})
	require.NoError(t, result.Error)
	require.Equal(t, int64(1), result.RowsAffected)
	require.NoError(t, db.Model(&model.OgGenerationRun{}).
		Where("id = ? AND status = ?", runID, model.OgGenerationRunStatusQueued).
		Updates(structured.Fields{
			"status":     model.OgGenerationRunStatusRunning,
			"started_at": now,
			"updated_at": now,
		}).Error)
	return leaseToken
}
