package og

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func (s *Lifecycle) Complete(
	ctx context.Context,
	generationID string,
	leaseToken string,
	written *commonv1.AssetWriteResult,
) (string, *model.PublicAsset, error) {
	now := s.now().UTC()
	status := model.OgGenerationStatusReady
	var completedAsset *model.PublicAsset
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		resultStatus, asset, err := s.completeOgGenerationWithDB(ctx, tx, generationID, leaseToken, written, now)
		status = resultStatus
		completedAsset = asset
		return err
	})
	return status, completedAsset, err
}

func (s *Lifecycle) completeOgGenerationWithDB(
	ctx context.Context,
	tx *gorm.DB,
	generationID string,
	leaseToken string,
	written *commonv1.AssetWriteResult,
	now time.Time,
) (string, *model.PublicAsset, error) {
	generation, target, err := lockOgGenerationAndTarget(tx, generationID)
	if err != nil {
		return model.OgGenerationStatusReady, nil, err
	}
	if written == nil || strings.TrimSpace(written.GetAssetId()) != generation.ID {
		return model.OgGenerationStatusReady, nil, errs.InvalidArgument("written.asset_id", "must equal generation_id")
	}
	if generation.Status == model.OgGenerationStatusReady || generation.Status == model.OgGenerationStatusSuperseded {
		return s.completeReplayedOgGeneration(ctx, tx, generation, leaseToken, written)
	}
	if err := validateOgGenerationCompletionLease(generation, leaseToken, now); err != nil {
		return model.OgGenerationStatusReady, nil, err
	}
	asset, err := mediaasset.NewLifecycle(tx, s.cdnDomain).CompletePublicAsset(ctx, written)
	if err != nil {
		return model.OgGenerationStatusReady, nil, err
	}
	status, err := s.finalizeNewOgGenerationCompletion(ctx, tx, generation, target, asset, leaseToken, now)
	return status, asset, err
}

func (s *Lifecycle) completeReplayedOgGeneration(
	ctx context.Context,
	tx *gorm.DB,
	generation *model.OgGeneration,
	leaseToken string,
	written *commonv1.AssetWriteResult,
) (string, *model.PublicAsset, error) {
	if generation.LeaseToken == nil || *generation.LeaseToken != strings.TrimSpace(leaseToken) {
		return model.OgGenerationStatusReady, nil, errs.FailedPrecondition("OG generation completion lease does not match")
	}
	asset, err := mediaasset.NewLifecycle(tx, s.cdnDomain).CompletePublicAsset(ctx, written)
	if err != nil {
		return model.OgGenerationStatusReady, nil, err
	}
	return generation.Status, asset, nil
}

func (s *Lifecycle) finalizeNewOgGenerationCompletion(
	ctx context.Context,
	tx *gorm.DB,
	generation *model.OgGeneration,
	target *model.OgGenerationTarget,
	asset *model.PublicAsset,
	leaseToken string,
	now time.Time,
) (string, error) {
	if !generation.DeadlineAt.After(now) {
		return model.OgGenerationStatusFailed, markOgGenerationFailed(
			tx, generation, FailureCodeCompletionRejected, now,
		)
	}
	if target.LatestGenerationID == nil {
		return model.OgGenerationStatusFailed, markOgGenerationFailed(
			tx, generation, FailureCodeCompletionRejected, now,
		)
	}
	if generation.Status == model.OgGenerationStatusSuperseded || *target.LatestGenerationID != generation.ID {
		return model.OgGenerationStatusSuperseded, completeSupersededOgGeneration(tx, generation, target, now)
	}
	if err := claimLatestOgGenerationTarget(tx, generation, target, now); err != nil {
		return model.OgGenerationStatusReady, err
	}
	if err := s.bindCompletedOgAsset(ctx, tx, generation, target, asset, now); err != nil {
		if errors.Is(err, ErrTranslationTargetMissing) {
			if err := markOgGenerationSuperseded(tx, generation, nil, now); err != nil {
				return model.OgGenerationStatusSuperseded, err
			}
			return model.OgGenerationStatusSuperseded, refreshOgRunStatus(tx, generation.RunID, now)
		}
		return model.OgGenerationStatusReady, err
	}
	if err := markOgGenerationReady(tx, generation, leaseToken, now); err != nil {
		return model.OgGenerationStatusReady, err
	}
	assetRef, err := mediaasset.NewLifecycle(tx, s.cdnDomain).AssetRef(*asset)
	if err != nil {
		return model.OgGenerationStatusReady, err
	}
	notifyOgLifecycle(tx, generation, target, model.OgGenerationStatusReady, nil, assetRef, now)
	return model.OgGenerationStatusReady, refreshOgRunStatus(tx, generation.RunID, now)
}

func completeSupersededOgGeneration(
	tx *gorm.DB,
	generation *model.OgGeneration,
	target *model.OgGenerationTarget,
	now time.Time,
) error {
	if generation.Status != model.OgGenerationStatusSuperseded {
		if err := markOgGenerationSuperseded(tx, generation, target.LatestGenerationID, now); err != nil {
			return err
		}
	}
	return refreshOgRunStatus(tx, generation.RunID, now)
}

func claimLatestOgGenerationTarget(
	tx *gorm.DB,
	generation *model.OgGeneration,
	target *model.OgGenerationTarget,
	now time.Time,
) error {
	result := tx.Model(&model.OgGenerationTarget{}).
		Where("id = ? AND latest_generation_id = ?", target.ID, generation.ID).
		Update("updated_at", now)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errs.FailedPrecondition("OG generation is no longer the latest target")
	}
	return nil
}

func (s *Lifecycle) bindCompletedOgAsset(
	ctx context.Context,
	tx *gorm.DB,
	generation *model.OgGeneration,
	target *model.OgGenerationTarget,
	asset *model.PublicAsset,
	now time.Time,
) error {
	var snapshot ogEntitySnapshot
	if err := json.Unmarshal(generation.EntitySnapshot, &snapshot); err != nil {
		return errs.FailedPrecondition("OG generation entity snapshot is invalid")
	}
	pinnedTarget := Target{
		EntityType: target.EntityType,
		EntityID:   target.EntityID,
		Locale:     target.Locale,
		Kind:       target.TargetKind,
	}
	projection, err := projectionFor(s.projections, pinnedTarget)
	if err != nil {
		return err
	}
	return projection.Complete(ctx, tx, pinnedTarget, asset.ID, now, s.cdnDomain)
}

func markOgGenerationReady(tx *gorm.DB, generation *model.OgGeneration, leaseToken string, now time.Time) error {
	result := tx.Model(&model.OgGeneration{}).
		Where("id = ? AND status = ? AND lease_token = ?", generation.ID, model.OgGenerationStatusProcessing, leaseToken).
		Updates(structured.Fields{
			"status": model.OgGenerationStatusReady, "ready_at": now,
			"completed_at": now, "updated_at": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errs.FailedPrecondition("OG generation lease changed while completing")
	}
	generation.Status = model.OgGenerationStatusReady
	return nil
}
