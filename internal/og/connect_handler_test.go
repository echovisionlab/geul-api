package og_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/og/ogtest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type authorization struct {
	authenticated, admin error
	entity               error
	calls                []string
}

func (a *authorization) RequireAuthenticated(context.Context) error {
	a.calls = append(a.calls, "authenticated")
	return a.authenticated
}

func (a *authorization) RequireAdmin(context.Context) error {
	a.calls = append(a.calls, "admin")
	return a.admin
}

func (a *authorization) AuthorizeEntity(_ context.Context, entityType, entityID string, requireEdit bool) error {
	verb := "view"
	if requireEdit {
		verb = "edit"
	}
	a.calls = append(a.calls, verb+" "+entityType+" "+entityID)
	return a.entity
}

func TestRegenerateOgImageAuthorizesTheEntityBeforePlanning(t *testing.T) {
	store, units := ogtest.New()
	entityID := uuid.NewString()
	store.Sources["work"] = func(id string, selection og.Selection) ([]og.Request, error) {
		require.Equal(t, og.Selection{Kind: og.SelectionLocale, Locale: "ko"}, selection)
		return []og.Request{localeRequest("work", id, "Work", "ko")}, nil
	}
	authorizer := &authorization{}
	handler := og.NewAdminConnectHandler(og.NewAdmin(units, authorizer))

	response, err := handler.RegenerateOgImage(t.Context(), connect.NewRequest(&managev1.RegenerateOgImageRequest{
		EntityType: managev1.OgEntityType_OG_ENTITY_TYPE_WORK, EntityId: &entityID,
		Selection: &managev1.OgTargetSelection{Target: &managev1.OgTargetSelection_Locale{Locale: "ko"}},
	}))
	require.NoError(t, err)
	require.Len(t, response.Msg.GetGenerationIds(), 1)
	require.Equal(t, []string{"authenticated", "authenticated", "edit work " + entityID}, authorizer.calls)
	stored := run(t, store, response.Msg.GetRunId())
	require.Equal(t, "manual", stored.TriggerKind)
	require.Equal(t, "admin_regenerate", stored.Reason)

	authorizer.entity = errs.NoPermission("access", "work")
	_, err = handler.RegenerateOgImage(t.Context(), connect.NewRequest(&managev1.RegenerateOgImageRequest{
		EntityType: managev1.OgEntityType_OG_ENTITY_TYPE_WORK, EntityId: &entityID,
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	authorizer.entity = nil
	_, err = handler.RegenerateOgImage(t.Context(), connect.NewRequest(&managev1.RegenerateOgImageRequest{}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.ErrorContains(t, err, "OG_ENTITY_TYPE_UNSPECIFIED")
}

func TestRegenerateOgImageAuthorizesStaticTargetsAsTheirCanonicalRoute(t *testing.T) {
	store, units := ogtest.New()
	store.Sources["privacy"] = func(id string, _ og.Selection) ([]og.Request, error) {
		require.Equal(t, "ignored", id)
		return nil, og.ErrEntityNotFound
	}
	authorizer := &authorization{}
	_, err := og.NewAdminConnectHandler(og.NewAdmin(units, authorizer)).RegenerateOgImage(t.Context(), connect.NewRequest(&managev1.RegenerateOgImageRequest{
		EntityType: managev1.OgEntityType_OG_ENTITY_TYPE_PRIVACY, EntityId: new("ignored"),
	}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	require.Contains(t, authorizer.calls, "edit privacy "+og.PrivacyRouteEntityID)
}

func TestRegenerateAllOgImagesReconcilesAndPlansOneRun(t *testing.T) {
	store, units := ogtest.New()
	store.All = []og.Request{localeRequest("post", uuid.NewString(), "Post", "ko"), entityRequest("label", uuid.NewString(), "Label")}
	authorizer := &authorization{}
	handler := og.NewAdminConnectHandler(og.NewAdmin(units, authorizer))

	response, err := handler.RegenerateAllOgImages(t.Context(), connect.NewRequest(&managev1.RegenerateAllOgImagesRequest{}))
	require.NoError(t, err)
	require.EqualValues(t, 2, response.Msg.GetGenerationCount())
	require.Equal(t, 1, store.Reconciled())
	require.Equal(t, "admin_regenerate_all", run(t, store, response.Msg.GetRunId()).Reason)

	authorizer.admin = errs.AdminRequired()
	_, err = handler.RegenerateAllOgImages(t.Context(), connect.NewRequest(&managev1.RegenerateAllOgImagesRequest{}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Equal(t, 1, store.Reconciled())
}

func TestGetOgGenerationRendersTheGenerationAndItsReadyAsset(t *testing.T) {
	store, units := ogtest.New()
	entityID := uuid.NewString()
	id := mustPlan(t, units, localeRequest("work", entityID, "Work", "en")).GenerationIDs[0]
	_, _, err := og.NewLifecycle(units).Complete(t.Context(), id, leased(store, id, time.Now().Add(time.Minute)), written(id))
	require.NoError(t, err)
	authorizer := &authorization{}
	handler := og.NewAdminConnectHandler(og.NewAdmin(units, authorizer))

	response, err := handler.GetOgGeneration(t.Context(), connect.NewRequest(&managev1.GetOgGenerationRequest{GenerationId: id}))
	require.NoError(t, err)
	generation := response.Msg.GetGeneration()
	require.Equal(t, managev1.OgGenerationStatus_OG_GENERATION_STATUS_READY, generation.GetStatus())
	require.Equal(t, "en", generation.GetTarget().GetLocale().GetLocale())
	require.Equal(t, managev1.OgEntityType_OG_ENTITY_TYPE_WORK, generation.GetTarget().GetEntityType())
	require.Equal(t, id, generation.GetAsset().GetAssetId())
	require.Equal(t, commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE, generation.GetAsset().GetDisposition())
	require.NotNil(t, generation.GetCompletedAt())
	require.Equal(t, []string{"authenticated", "authenticated", "view work " + entityID}, authorizer.calls)

	latest, err := handler.GetLatestOgGeneration(t.Context(), connect.NewRequest(&managev1.GetLatestOgGenerationRequest{
		Target: &managev1.OgGenerationTarget{
			EntityType: managev1.OgEntityType_OG_ENTITY_TYPE_WORK, EntityId: entityID,
			Scope: &managev1.OgGenerationTarget_Locale{Locale: &managev1.OgLocaleTarget{Locale: "en"}},
		},
	}))
	require.NoError(t, err)
	require.Equal(t, id, latest.Msg.GetGeneration().GetGenerationId())
}

func TestGetOgGenerationKeepsReadyHistoryAfterTheAssetIsRetired(t *testing.T) {
	store, units := ogtest.New()
	id := mustPlan(t, units, localeRequest("work", uuid.NewString(), "Work", "en")).GenerationIDs[0]
	store.UpdateGeneration(id, func(generation *og.Generation) { generation.Status = og.StatusReady })

	response, err := og.NewAdminConnectHandler(og.NewAdmin(units, &authorization{})).GetOgGeneration(
		t.Context(), connect.NewRequest(&managev1.GetOgGenerationRequest{GenerationId: id}))
	require.NoError(t, err)
	require.Equal(t, managev1.OgGenerationStatus_OG_GENERATION_STATUS_READY, response.Msg.GetGeneration().GetStatus())
	require.Nil(t, response.Msg.GetGeneration().GetAsset())
}

func TestOgGenerationReadsAnswerMissingRows(t *testing.T) {
	_, units := ogtest.New()
	handler := og.NewAdminConnectHandler(og.NewAdmin(units, &authorization{}))
	_, err := handler.GetOgGeneration(t.Context(), connect.NewRequest(&managev1.GetOgGenerationRequest{GenerationId: "bad"}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = handler.GetOgGeneration(t.Context(), connect.NewRequest(&managev1.GetOgGenerationRequest{GenerationId: uuid.NewString()}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = handler.GetOgGenerationRun(t.Context(), connect.NewRequest(&managev1.GetOgGenerationRunRequest{RunId: uuid.NewString()}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = handler.GetLatestOgGeneration(t.Context(), connect.NewRequest(&managev1.GetLatestOgGenerationRequest{
		Target: &managev1.OgGenerationTarget{
			EntityType: managev1.OgEntityType_OG_ENTITY_TYPE_LABEL, EntityId: uuid.NewString(),
			Scope: &managev1.OgGenerationTarget_Entity{Entity: &managev1.OgEntityTarget{}},
		},
	}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	require.ErrorContains(t, err, "og_generation_target")
	_, err = handler.GetLatestOgGeneration(t.Context(), connect.NewRequest(&managev1.GetLatestOgGenerationRequest{}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestGetOgGenerationRunCountsStatusesAndNamesFailures(t *testing.T) {
	store, units := ogtest.New()
	plan := mustPlan(t, units, localeRequest("work", uuid.NewString(), "A", "en"), localeRequest("work", uuid.NewString(), "B", "en"))
	failing := plan.GenerationIDs[1]
	require.NoError(t, og.NewLifecycle(units).Fail(t.Context(), failing, leased(store, failing, time.Now().Add(time.Minute)), og.FailureCodeIntegrityFailed))

	response, err := og.NewAdminConnectHandler(og.NewAdmin(units, &authorization{})).GetOgGenerationRun(
		t.Context(), connect.NewRequest(&managev1.GetOgGenerationRunRequest{RunId: plan.RunID}))
	require.NoError(t, err)
	view := response.Msg.GetRun()
	require.EqualValues(t, 2, view.GetGenerationCount())
	require.EqualValues(t, 1, view.GetQueuedCount())
	require.EqualValues(t, 1, view.GetFailedCount())
	require.Equal(t, managev1.OgGenerationRunStatus_OG_GENERATION_RUN_STATUS_PROCESSING, view.GetStatus())
	require.Len(t, view.GetFailures(), 1)
	require.Equal(t, failing, view.GetFailures()[0].GetGenerationId())
	require.Equal(t, og.FailureCodeIntegrityFailed, view.GetFailures()[0].GetErrorCode())
}

func TestInternalHandlerRendersClaimsCompletionsAndFailures(t *testing.T) {
	store, units := ogtest.New()
	id := mustPlan(t, units, localeRequest("work", uuid.NewString(), "Work", "en")).GenerationIDs[0]
	handler := og.NewInternalConnectHandler(og.NewLifecycle(units))

	claim, err := handler.ClaimOgGeneration(t.Context(), connect.NewRequest(&intrav1.ClaimOgGenerationRequest{GenerationId: id}))
	require.NoError(t, err)
	require.Equal(t, intrav1.OgGenerationClaimResult_OG_GENERATION_CLAIM_RESULT_CLAIMED, claim.Msg.GetResult())
	require.Equal(t, "Work", claim.Msg.GetTitle())
	require.Equal(t, id, claim.Msg.GetOutput().GetAssetId())
	require.Equal(t, "asset/"+id+".webp", claim.Msg.GetOutput().GetObjectKey())
	require.Equal(t, "#b02d23", claim.Msg.GetRenderConfig().GetPrimaryColor())
	require.Equal(t, "en", claim.Msg.GetTarget().GetLocale().GetLocale())

	completed, err := handler.CompleteOgGeneration(t.Context(), connect.NewRequest(&intrav1.CompleteOgGenerationRequest{
		GenerationId: id, LeaseToken: claim.Msg.GetLeaseToken(),
		Written: &commonv1.AssetWriteResult{AssetId: id, FileSize: 123, Sha256: written(id).SHA256},
	}))
	require.NoError(t, err)
	require.Equal(t, managev1.OgGenerationStatus_OG_GENERATION_STATUS_READY, completed.Msg.GetStatus())
	require.Equal(t, ogtest.CDNDomain+"/asset/"+id+".webp", completed.Msg.GetAsset().GetUrl())

	_, err = handler.FailOgGeneration(t.Context(), connect.NewRequest(&intrav1.FailOgGenerationRequest{
		GenerationId: id, LeaseToken: claim.Msg.GetLeaseToken(), ErrorCode: og.FailureCodeProcessingFailed,
	}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	_, err = handler.FailOgGeneration(t.Context(), connect.NewRequest(&intrav1.FailOgGenerationRequest{
		GenerationId: uuid.NewString(), ErrorCode: og.FailureCodeProcessingFailed,
	}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	require.Equal(t, og.StatusReady, generation(t, store, id).Status)
}
