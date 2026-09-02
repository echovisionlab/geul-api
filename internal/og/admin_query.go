package og

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *AdminService) GetOgGeneration(ctx context.Context, req *connect.Request[managev1.GetOgGenerationRequest]) (*connect.Response[managev1.GetOgGenerationResponse], error) {
	if err := s.authorizer.RequireAuthenticated(ctx); err != nil {
		return nil, err
	}
	if req == nil || req.Msg == nil {
		return nil, errs.Required("request")
	}
	generation, target, err := loadOgGenerationView(ctx, s.db, req.Msg.GetGenerationId())
	if err != nil {
		return nil, err
	}
	if err := s.authorizeStoredOgTarget(ctx, target, false); err != nil {
		return nil, err
	}
	view, err := s.ogGenerationToProto(ctx, generation, target)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.GetOgGenerationResponse{Generation: view}), nil
}

func (s *AdminService) GetLatestOgGeneration(ctx context.Context, req *connect.Request[managev1.GetLatestOgGenerationRequest]) (*connect.Response[managev1.GetLatestOgGenerationResponse], error) {
	if err := s.authorizer.RequireAuthenticated(ctx); err != nil {
		return nil, err
	}
	if req == nil || req.Msg == nil {
		return nil, errs.Required("request")
	}
	entityType, entityID, targetKind, locale, err := parseOgGenerationTarget(req.Msg.GetTarget())
	if err != nil {
		return nil, err
	}
	policy, ok := PolicyForEntityName(entityType)
	if !ok {
		return nil, errs.InvalidArgument("entity_type", "unknown OG target entity type")
	}
	if err := s.authorizeOgEntityTarget(ctx, policy.EntityType, entityID, false); err != nil {
		return nil, err
	}
	query := s.db.WithContext(ctx).Where("entity_type = ? AND entity_id = ? AND target_kind = ?", entityType, entityID, targetKind)
	if locale == nil {
		query = query.Where("locale IS NULL")
	} else {
		query = query.Where("locale = ?", *locale)
	}
	var target model.OgGenerationTarget
	if err := query.Take(&target).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("og_generation_target", entityID)
		}
		return nil, errs.Internal(err)
	}
	if target.LatestGenerationID == nil {
		return nil, errs.NotFound("og_generation", entityID)
	}
	generation, _, err := loadOgGenerationView(ctx, s.db, *target.LatestGenerationID)
	if err != nil {
		return nil, err
	}
	view, err := s.ogGenerationToProto(ctx, generation, &target)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.GetLatestOgGenerationResponse{Generation: view}), nil
}

func (s *AdminService) GetOgGenerationRun(ctx context.Context, req *connect.Request[managev1.GetOgGenerationRunRequest]) (*connect.Response[managev1.GetOgGenerationRunResponse], error) {
	if err := s.authorizer.RequireAdmin(ctx); err != nil {
		return nil, err
	}
	if req == nil || req.Msg == nil {
		return nil, errs.Required("request")
	}
	runID := strings.TrimSpace(req.Msg.GetRunId())
	if _, err := uuid.Parse(runID); err != nil {
		return nil, errs.InvalidArgument("run_id", "must be a UUID")
	}
	var run model.OgGenerationRun
	if err := s.db.WithContext(ctx).First(&run, "id = ?", runID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("og_generation_run", runID)
		}
		return nil, errs.Internal(err)
	}
	var generations []model.OgGeneration
	if err := s.db.WithContext(ctx).Where("run_id = ?", runID).Order("request_sequence").Find(&generations).Error; err != nil {
		return nil, errs.Internal(err)
	}
	view := &managev1.OgGenerationRun{
		RunId: run.ID, Status: ogRunStatusToProto(run.Status), GenerationCount: int32(len(generations)),
		CreatedAt: timestamppb.New(run.CreatedAt), UpdatedAt: timestamppb.New(run.UpdatedAt),
	}
	if run.CompletedAt != nil {
		view.CompletedAt = timestamppb.New(*run.CompletedAt)
	}
	for i := range generations {
		generation := &generations[i]
		switch generation.Status {
		case model.OgGenerationStatusQueued:
			view.QueuedCount++
		case model.OgGenerationStatusProcessing:
			view.ProcessingCount++
		case model.OgGenerationStatusReady:
			view.ReadyCount++
		case model.OgGenerationStatusFailed:
			view.FailedCount++
		case model.OgGenerationStatusSuperseded:
			view.SupersededCount++
		case model.OgGenerationStatusCancelled:
			view.CancelledCount++
		}
		if generation.Status == model.OgGenerationStatusFailed {
			var target model.OgGenerationTarget
			if err := s.db.WithContext(ctx).First(&target, "id = ?", generation.TargetID).Error; err != nil {
				return nil, errs.Internal(err)
			}
			protoTarget, err := TargetToProto(&target)
			if err != nil {
				return nil, errs.Internal(err)
			}
			view.Failures = append(view.Failures, &managev1.OgGenerationFailure{
				GenerationId: generation.ID, Target: protoTarget,
				ErrorCode: stringValue(generation.LastErrorCode),
			})
		}
	}
	return connect.NewResponse(&managev1.GetOgGenerationRunResponse{Run: view}), nil
}

func loadOgGenerationView(ctx context.Context, db *gorm.DB, generationID string) (*model.OgGeneration, *model.OgGenerationTarget, error) {
	generationID = strings.TrimSpace(generationID)
	if _, err := uuid.Parse(generationID); err != nil {
		return nil, nil, errs.InvalidArgument("generation_id", "must be a UUID")
	}
	var generation model.OgGeneration
	if err := db.WithContext(ctx).First(&generation, "id = ?", generationID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil, errs.NotFound("og_generation", generationID)
		}
		return nil, nil, errs.Internal(err)
	}
	var target model.OgGenerationTarget
	if err := db.WithContext(ctx).First(&target, "id = ?", generation.TargetID).Error; err != nil {
		return nil, nil, errs.Internal(err)
	}
	return &generation, &target, nil
}

func (s *AdminService) ogGenerationToProto(ctx context.Context, generation *model.OgGeneration, target *model.OgGenerationTarget) (*managev1.OgGeneration, error) {
	protoTarget, err := TargetToProto(target)
	if err != nil {
		return nil, errs.Internal(err)
	}
	view := &managev1.OgGeneration{
		GenerationId: generation.ID, RunId: generation.RunID, Target: protoTarget,
		Status:                  StatusToProto(generation.Status),
		ErrorCode:               optionalString(generation.LastErrorCode),
		ReplacementGenerationId: optionalString(generation.SupersededByID),
		CreatedAt:               timestamppb.New(generation.CreatedAt), UpdatedAt: timestamppb.New(generation.UpdatedAt),
	}
	if generation.CompletedAt != nil {
		view.CompletedAt = timestamppb.New(*generation.CompletedAt)
	}
	if generation.Status != model.OgGenerationStatusReady {
		return view, nil
	}
	asset, err := s.loadReadyOgGenerationAsset(ctx, generation.ID)
	if err != nil {
		return nil, err
	}
	view.Asset = asset
	return view, nil
}

func (s *AdminService) loadReadyOgGenerationAsset(ctx context.Context, generationID string) (*commonv1.AssetRef, error) {
	var storedAsset model.PublicAsset
	if err := s.db.WithContext(ctx).First(&storedAsset, "id = ?", generationID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, errs.Internal(err)
	}
	if storedAsset.Status != model.PublicAssetStatusReady {
		return nil, nil
	}
	return mediaasset.NewLifecycle(s.db, s.cdnDomain).AssetRef(storedAsset)
}

func parseOgGenerationTarget(target *managev1.OgGenerationTarget) (string, string, string, *string, error) {
	if target == nil {
		return "", "", "", nil, errs.Required("target")
	}
	policy, ok := PolicyForEntityType(target.GetEntityType())
	if !ok {
		return "", "", "", nil, errs.InvalidEntityType(target.GetEntityType().String())
	}
	entityID := strings.TrimSpace(target.GetEntityId())
	if entityID == "" {
		return "", "", "", nil, errs.Required("target.entity_id")
	}
	if policy.EntityType == managev1.OgEntityType_OG_ENTITY_TYPE_SITE && entityID != "default" {
		return "", "", "", nil, errs.InvalidArgument("target.entity_id", "site OG target must use the canonical site identity")
	}
	if policy.LocaleStrategy == LocaleStrategyStatic && entityID != policy.CanonicalEntityID {
		return "", "", "", nil, errs.InvalidArgument("target.entity_id", "legal OG target must use the canonical route identity")
	}
	if target.GetEntity() != nil {
		if policy.LocaleStrategy != LocaleStrategyBaseOnly {
			return "", "", "", nil, errs.InvalidArgument("target.scope", "this OG entity requires a locale scope")
		}
		return policy.Name, entityID, "entity", nil, nil
	}
	if target.GetLocale() != nil {
		if policy.LocaleStrategy == LocaleStrategyBaseOnly {
			return "", "", "", nil, errs.InvalidArgument("target.scope", "base-only OG entities require entity scope")
		}
		locale := strings.TrimSpace(target.GetLocale().GetLocale())
		if locale == "" {
			return "", "", "", nil, errs.Required("target.locale.locale")
		}
		return policy.Name, entityID, "locale", &locale, nil
	}
	return "", "", "", nil, errs.Required("target.scope")
}

func ogRunStatusToProto(status string) managev1.OgGenerationRunStatus {
	switch status {
	case model.OgGenerationRunStatusQueued:
		return managev1.OgGenerationRunStatus_OG_GENERATION_RUN_STATUS_QUEUED
	case model.OgGenerationRunStatusRunning:
		return managev1.OgGenerationRunStatus_OG_GENERATION_RUN_STATUS_PROCESSING
	case model.OgGenerationRunStatusReady:
		return managev1.OgGenerationRunStatus_OG_GENERATION_RUN_STATUS_READY
	case model.OgGenerationRunStatusPartialFailed:
		return managev1.OgGenerationRunStatus_OG_GENERATION_RUN_STATUS_PARTIALLY_FAILED
	case model.OgGenerationRunStatusFailed:
		return managev1.OgGenerationRunStatus_OG_GENERATION_RUN_STATUS_FAILED
	case model.OgGenerationRunStatusCancelled:
		return managev1.OgGenerationRunStatus_OG_GENERATION_RUN_STATUS_CANCELLED
	default:
		return managev1.OgGenerationRunStatus_OG_GENERATION_RUN_STATUS_UNSPECIFIED
	}
}
