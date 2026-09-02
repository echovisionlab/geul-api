//go:build integration

package og_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestOgGenerationBulkBatchesThousandsOfTargetsInOnePostgresTransactionIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	const targetCount = 3200
	requests := make([]og.Request, targetCount)
	for index := range requests {
		requests[index] = ogTestRequest(
			managev1.OgEntityType_OG_ENTITY_TYPE_WORK,
			uuid.NewString(),
			fmt.Sprintf("Bulk target %04d", index),
			stringPtr("en"),
			nil,
		)
	}

	plan, err := requestOgGenerationForTest(
		t.Context(), newOGPlannerForTest(db, "https://cdn.example.com"), "manual", "postgres_bind_limit_regression", requests,
	)
	require.NoError(t, err)
	require.Len(t, plan.GenerationIDs, targetCount)

	var generationCount int64
	require.NoError(t, db.Model(&model.OgGeneration{}).Where("run_id = ?", plan.RunID).Count(&generationCount).Error)
	require.EqualValues(t, targetCount, generationCount)
}
