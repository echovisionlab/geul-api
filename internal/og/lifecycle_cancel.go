package og

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *Lifecycle) CancelEntityWithDB(
	ctx context.Context,
	tx *gorm.DB,
	entityType managev1.OgEntityType,
	entityID string,
) error {
	policy, ok := PolicyForEntityType(entityType)
	if !ok {
		return errs.InvalidEntityType(entityType.String())
	}
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return errs.Required("entity_id")
	}
	if policy.CanonicalEntityID != "" && entityID != policy.CanonicalEntityID {
		if policy.EntityType == managev1.OgEntityType_OG_ENTITY_TYPE_SITE {
			return errs.InvalidArgument("entity_id", "site OG targets must use the canonical site identity")
		}
		return errs.InvalidArgument("entity_id", "legal OG targets must use the canonical route identity")
	}
	now := s.now().UTC()
	var targets []model.OgGenerationTarget
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("entity_type = ? AND entity_id = ?", policy.Name, entityID).
		Order("COALESCE(locale, ''), id").Find(&targets).Error; err != nil {
		return err
	}
	return cancelOgGenerationTargetsWithDB(ctx, tx, targets, now)
}

func (s *Lifecycle) CancelEntity(ctx context.Context, entityType managev1.OgEntityType, entityID string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.CancelEntityWithDB(ctx, tx, entityType, entityID)
	})
}

func (s *Lifecycle) CancelTargetWithDB(
	ctx context.Context,
	tx *gorm.DB,
	entityType managev1.OgEntityType,
	entityID string,
	locale string,
) error {
	policy, ok := PolicyForEntityType(entityType)
	if !ok {
		return errs.InvalidEntityType(entityType.String())
	}
	entityID = strings.TrimSpace(entityID)
	locale = strings.TrimSpace(locale)
	if entityID == "" {
		return errs.Required("entity_id")
	}
	if locale == "" {
		return errs.Required("locale")
	}
	if policy.LocaleStrategy == LocaleStrategyBaseOnly {
		return errs.InvalidArgument("locale", "base-only OG targets do not have localized targets")
	}
	if policy.CanonicalEntityID != "" && entityID != policy.CanonicalEntityID {
		return errs.InvalidArgument("entity_id", "legal OG targets must use the canonical route identity")
	}
	var targets []model.OgGenerationTarget
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("entity_type = ? AND entity_id = ? AND locale = ?", policy.Name, entityID, locale).
		Order("id").Find(&targets).Error; err != nil {
		return err
	}
	return cancelOgGenerationTargetsWithDB(ctx, tx, targets, s.now().UTC())
}

func cancelOgGenerationTargetsWithDB(
	ctx context.Context,
	tx *gorm.DB,
	targets []model.OgGenerationTarget,
	now time.Time,
) error {
	runIDs := make(map[string]struct{})
	for i := range targets {
		var generations []model.OgGeneration
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("target_id = ? AND status IN ?", targets[i].ID, []string{
				model.OgGenerationStatusQueued,
				model.OgGenerationStatusProcessing,
			}).Order("request_sequence, id").Find(&generations).Error; err != nil {
			return err
		}
		for j := range generations {
			generation := &generations[j]
			result := tx.Model(&model.OgGeneration{}).
				Where("id = ? AND status = ?", generation.ID, generation.Status).
				Updates(structured.Fields{
					"status": model.OgGenerationStatusCancelled, "cancelled_at": now, "completed_at": now,
					"lease_token": nil, "lease_expires_at": nil,
					"updated_at": now,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errs.FailedPrecondition("OG generation changed while cancelling entity")
			}
			generation.Status = model.OgGenerationStatusCancelled
			generation.CancelledAt = &now
			generation.CompletedAt = &now
			notifyOgLifecycle(tx, generation, &targets[i], model.OgGenerationStatusCancelled, nil, nil, now)
			runIDs[generation.RunID] = struct{}{}
		}
		if err := tx.Model(&model.OgGenerationTarget{}).Where("id = ?", targets[i].ID).
			Updates(structured.Fields{"latest_generation_id": nil, "updated_at": now}).Error; err != nil {
			return err
		}
	}
	for runID := range runIDs {
		if err := refreshOgRunStatus(tx, runID, now); err != nil {
			return err
		}
	}
	return nil
}
