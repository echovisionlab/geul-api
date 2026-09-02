package og_test

import (
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestOgGenerationClaimExposesActiveLeaseAndReclaimsOnlyAfterExpiry(t *testing.T) {
	db := newServiceUnitDB(t)
	setupOgLifecycleUnitTables(t, db)
	now := time.Now().UTC()
	activeLease := now.Add(time.Minute)
	generation := seedProcessingOgGeneration(t, db, managev1.OgEntityType_OG_ENTITY_TYPE_WORK, uuid.NewString(), nil, uuid.NewString(), now.Add(time.Hour), activeLease)
	internal := og.NewInternalService(db, "https://cdn.example.com")
	active, err := internal.ClaimOgGeneration(t.Context(), connect.NewRequest(&intrav1.ClaimOgGenerationRequest{GenerationId: generation.ID}))
	require.NoError(t, err)
	require.Equal(t, intrav1.OgGenerationClaimResult_OG_GENERATION_CLAIM_RESULT_SKIP, active.Msg.Result)
	require.Equal(t, managev1.OgGenerationStatus_OG_GENERATION_STATUS_PROCESSING, active.Msg.GenerationStatus)
	require.NotNil(t, active.Msg.LeaseExpiresAt)
	require.Equal(t, activeLease.Unix(), active.Msg.LeaseExpiresAt.AsTime().Unix())
	require.Nil(t, active.Msg.LeaseToken)
	require.NoError(t, db.Model(&model.OgGeneration{}).Where("id = ?", generation.ID).Update("lease_expires_at", now.Add(-time.Second)).Error)
	reclaimed, err := internal.ClaimOgGeneration(t.Context(), connect.NewRequest(&intrav1.ClaimOgGenerationRequest{GenerationId: generation.ID}))
	require.NoError(t, err)
	require.Equal(t, intrav1.OgGenerationClaimResult_OG_GENERATION_CLAIM_RESULT_CLAIMED, reclaimed.Msg.Result)
	require.NotNil(t, reclaimed.Msg.LeaseExpiresAt)
	require.NotNil(t, reclaimed.Msg.LeaseToken)
	_, err = internal.FailOgGeneration(t.Context(), connect.NewRequest(&intrav1.FailOgGenerationRequest{GenerationId: generation.ID, LeaseToken: reclaimed.Msg.GetLeaseToken(), ErrorCode: og.FailureCodeProcessingFailed, Error: "render failed"}))
	require.NoError(t, err)
	terminal, err := internal.ClaimOgGeneration(t.Context(), connect.NewRequest(&intrav1.ClaimOgGenerationRequest{GenerationId: generation.ID}))
	require.NoError(t, err)
	require.Equal(t, intrav1.OgGenerationClaimResult_OG_GENERATION_CLAIM_RESULT_SKIP, terminal.Msg.Result)
	require.Equal(t, managev1.OgGenerationStatus_OG_GENERATION_STATUS_FAILED, terminal.Msg.GenerationStatus)
	require.Nil(t, terminal.Msg.LeaseExpiresAt)
}

func TestOgGenerationCompletePastDeadlineStaysFailed(t *testing.T) {
	db := newServiceUnitDB(t)
	setupOgLifecycleUnitTables(t, db)
	now := time.Now().UTC()
	leaseToken := uuid.NewString()
	generation := seedProcessingOgGeneration(t, db, managev1.OgEntityType_OG_ENTITY_TYPE_WORK, uuid.NewString(), nil, leaseToken, now.Add(-time.Second), now.Add(time.Hour))
	status, _, err := newOGLifecycleForTest(db, "https://cdn.example.com").Complete(t.Context(), generation.ID, leaseToken, validOgWriteResult(generation.ID))
	require.NoError(t, err)
	assert.Equal(t, model.OgGenerationStatusFailed, status)
	var stored model.OgGeneration
	require.NoError(t, db.First(&stored, "id = ?", generation.ID).Error)
	assert.Equal(t, model.OgGenerationStatusFailed, stored.Status)
	assert.Equal(t, og.FailureCodeCompletionRejected, ptrStringValue(stored.LastErrorCode))
	assert.Nil(t, stored.ReadyAt)
	var bindingCount int64
	require.NoError(t, db.Model(&model.PublicAssetBinding{}).Where("asset_id = ?", generation.ID).Count(&bindingCount).Error)
	assert.Zero(t, bindingCount)
}

func TestOgGenerationCancelRejectsNonCanonicalSiteAndLegalTargets(t *testing.T) {
	db := newServiceUnitDB(t)
	setupOgLifecycleUnitTables(t, db)
	lifecycle := newOGLifecycleForTest(db, "")
	for _, testCase := range []struct {
		name       string
		entityType managev1.OgEntityType
		localized  bool
	}{{"site", managev1.OgEntityType_OG_ENTITY_TYPE_SITE, false}, {"privacy", managev1.OgEntityType_OG_ENTITY_TYPE_PRIVACY, true}, {"terms", managev1.OgEntityType_OG_ENTITY_TYPE_TERMS, true}} {
		t.Run(testCase.name, func(t *testing.T) {
			err := lifecycle.CancelEntityWithDB(t.Context(), db, testCase.entityType, uuid.NewString())
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			if testCase.localized {
				err = lifecycle.CancelTargetWithDB(t.Context(), db, testCase.entityType, uuid.NewString(), "ko")
				require.Error(t, err)
				assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			}
		})
	}
}

func TestOgGenerationBulkPreservesInputOrderAndSupersedesOverlappingRun(t *testing.T) {
	db := newServiceUnitDB(t)
	setupOgLifecycleUnitTables(t, db)
	entityID, ko, fr := uuid.NewString(), "ko", "fr"
	requests := []og.Request{
		ogTestRequest(managev1.OgEntityType_OG_ENTITY_TYPE_POST, entityID, "한국어", &ko, nil),
		ogTestRequest(managev1.OgEntityType_OG_ENTITY_TYPE_POST, entityID, "Français", &fr, nil),
	}
	planner := newOGPlannerForTest(db, "")
	first, err := requestOgGenerationForTest(t.Context(), planner, "manual", "first", requests)
	require.NoError(t, err)
	require.Len(t, first.GenerationIDs, 2)
	var firstKo model.OgGeneration
	require.NoError(t, db.First(&firstKo, "id = ?", first.GenerationIDs[0]).Error)
	var snapshot ogEntitySnapshotForTest
	require.NoError(t, json.Unmarshal(firstKo.EntitySnapshot, &snapshot))
	assert.Equal(t, "ko", ptrStringValue(snapshot.Locale))
	second, err := requestOgGenerationForTest(t.Context(), planner, "manual", "second", requests)
	require.NoError(t, err)
	require.Len(t, second.GenerationIDs, 2)
	for i, firstID := range first.GenerationIDs {
		var old model.OgGeneration
		require.NoError(t, db.First(&old, "id = ?", firstID).Error)
		assert.Equal(t, model.OgGenerationStatusSuperseded, old.Status)
		assert.Equal(t, second.GenerationIDs[i], ptrStringValue(old.SupersededByID))
	}
	for i, locale := range []string{"ko", "fr"} {
		var target model.OgGenerationTarget
		require.NoError(t, db.First(&target, "entity_type = ? AND entity_id = ? AND locale = ?", "post", entityID, locale).Error)
		assert.Equal(t, second.GenerationIDs[i], ptrStringValue(target.LatestGenerationID))
	}
}

func TestOgGenerationBulkRollsBackEntireRunWhenFeaturedAssetIsMissing(t *testing.T) {
	db := newServiceUnitDB(t)
	setupOgLifecycleUnitTables(t, db)
	missingFileID := uuid.NewString()
	_, err := requestOgGenerationForTest(t.Context(), newOGPlannerForTest(db, ""), "manual", "rollback", []og.Request{ogTestRequest(managev1.OgEntityType_OG_ENTITY_TYPE_WORK, uuid.NewString(), "Work", nil, &missingFileID)})
	require.Error(t, err)
	var runCount, generationCount, assetCount int64
	require.NoError(t, db.Model(&model.OgGenerationRun{}).Count(&runCount).Error)
	require.NoError(t, db.Model(&model.OgGeneration{}).Count(&generationCount).Error)
	require.NoError(t, db.Model(&model.PublicAsset{}).Count(&assetCount).Error)
	assert.Zero(t, runCount)
	assert.Zero(t, generationCount)
	assert.Zero(t, assetCount)
}

func TestOgGenerationCancelEntityIsDurableAcrossAllLocales(t *testing.T) {
	db := newServiceUnitDB(t)
	setupOgLifecycleUnitTables(t, db)
	entityID, ko, fr := uuid.NewString(), "ko", "fr"
	plan, err := requestOgGenerationForTest(t.Context(), newOGPlannerForTest(db, ""), "automatic", "delete", []og.Request{
		ogTestRequest(managev1.OgEntityType_OG_ENTITY_TYPE_POST, entityID, "KO", &ko, nil),
		ogTestRequest(managev1.OgEntityType_OG_ENTITY_TYPE_POST, entityID, "FR", &fr, nil),
	})
	require.NoError(t, err)
	require.NoError(t, cancelOgGenerationEntityForTest(t.Context(), newOGLifecycleForTest(db, ""), managev1.OgEntityType_OG_ENTITY_TYPE_POST, entityID))
	for _, generationID := range plan.GenerationIDs {
		var generation model.OgGeneration
		require.NoError(t, db.First(&generation, "id = ?", generationID).Error)
		assert.Equal(t, model.OgGenerationStatusCancelled, generation.Status)
		assert.NotNil(t, generation.CancelledAt)
	}
	var targets []model.OgGenerationTarget
	require.NoError(t, db.Where("entity_type = ? AND entity_id = ?", "post", entityID).Find(&targets).Error)
	require.Len(t, targets, 2)
	for _, target := range targets {
		assert.Nil(t, target.LatestGenerationID)
	}
	var run model.OgGenerationRun
	require.NoError(t, db.First(&run, "id = ?", plan.RunID).Error)
	assert.Equal(t, model.OgGenerationRunStatusCancelled, run.Status)
}

func seedProcessingOgGeneration(t *testing.T, db *gorm.DB, entityType managev1.OgEntityType, entityID string, locale *string, leaseToken string, deadlineAt, leaseExpiresAt time.Time) model.OgGeneration {
	t.Helper()
	now := deadlineAt.Add(-time.Minute)
	asset, _, err := mediaasset.NewLifecycle(db, "").AllocatePublicAsset(t.Context(), mediaasset.Allocation{Kind: "og", Extension: "webp", MimeType: "image/webp", Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE})
	require.NoError(t, err)
	run := model.OgGenerationRun{ID: uuid.NewString(), TriggerKind: "manual", Reason: "test", RenderConfigSnapshot: []byte(`{}`), ConfigRevision: "revision", Status: model.OgGenerationRunStatusRunning, StartedAt: &now, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&run).Error)
	policy, ok := og.PolicyForEntityType(entityType)
	require.True(t, ok)
	target := model.OgGenerationTarget{ID: uuid.NewString(), EntityType: policy.Name, EntityID: entityID, TargetKind: "entity", Locale: locale, LatestGenerationID: &asset.ID, CreatedAt: now, UpdatedAt: now}
	if locale != nil {
		target.TargetKind = "locale"
	}
	require.NoError(t, db.Create(&target).Error)
	snapshot, err := json.Marshal(ogEntitySnapshotForTest{EntityType: target.EntityType, EntityID: entityID, Title: "title", Locale: locale, Output: ogOutputSnapshotForTest{AssetID: asset.ID, ObjectKey: asset.ObjectKey, Extension: asset.Extension, MimeType: asset.MimeType}})
	require.NoError(t, err)
	generation := model.OgGeneration{ID: asset.ID, RunID: run.ID, TargetID: target.ID, Status: model.OgGenerationStatusProcessing, EntitySnapshot: snapshot, ProcessingAt: &now, LeaseToken: &leaseToken, LeaseExpiresAt: &leaseExpiresAt, DeadlineAt: deadlineAt, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&generation).Error)
	return generation
}

func validOgWriteResult(assetID string) *commonv1.AssetWriteResult {
	digest := make([]byte, 32)
	digest[0] = 7
	return &commonv1.AssetWriteResult{AssetId: assetID, FileSize: 123, Sha256: digest}
}

func ptrStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
