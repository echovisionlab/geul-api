package og

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
)

type ClaimResult int

const (
	Claimed ClaimResult = iota + 1
	ClaimSkipped
)

type Claim struct {
	Result               ClaimResult
	Generation           model.OgGeneration
	Target               model.OgGenerationTarget
	EntitySnapshot       ogEntitySnapshot
	RenderConfigSnapshot []byte
	ConfigRevision       string
	LeaseToken           string
	LeaseExpiresAt       *time.Time
}

func (s *Lifecycle) Claim(ctx context.Context, generationID string) (*Claim, error) {
	generationID = strings.TrimSpace(generationID)
	if _, err := uuid.Parse(generationID); err != nil {
		return nil, errs.InvalidArgument("generation_id", "must be a UUID")
	}
	now := s.now().UTC()
	claim := &Claim{Result: ClaimSkipped}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.claimOgGenerationWithDB(tx, generationID, claim, now)
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFound("og_generation", generationID)
		}
		return nil, err
	}
	return claim, nil
}

func (s *Lifecycle) claimOgGenerationWithDB(
	tx *gorm.DB,
	generationID string,
	claim *Claim,
	now time.Time,
) error {
	generation, target, err := lockOgGenerationAndTarget(tx, generationID)
	if err != nil {
		return err
	}
	claim.Generation = *generation
	claim.Target = *target
	eligible, err := s.resolveOgGenerationClaimDisposition(tx, generation, target, claim, now)
	if err != nil || !eligible {
		return err
	}
	return s.startOgGenerationClaim(tx, generation, target, claim, now)
}

func (s *Lifecycle) resolveOgGenerationClaimDisposition(
	tx *gorm.DB,
	generation *model.OgGeneration,
	target *model.OgGenerationTarget,
	claim *Claim,
	now time.Time,
) (bool, error) {
	if isTerminalOgGenerationStatus(generation.Status) {
		return false, nil
	}
	if target.LatestGenerationID == nil {
		err := markOgGenerationFailed(tx, generation, FailureCodeInvalidClaim, now)
		claim.Generation = *generation
		return false, err
	}
	if *target.LatestGenerationID != generation.ID {
		err := markOgGenerationSuperseded(tx, generation, target.LatestGenerationID, now)
		claim.Generation = *generation
		return false, err
	}
	if !generation.DeadlineAt.After(now) {
		err := markOgGenerationFailed(tx, generation, FailureCodeProcessingFailed, now)
		claim.Generation = *generation
		return false, err
	}
	if hasActiveOgGenerationLease(generation, now) {
		claim.LeaseExpiresAt = generation.LeaseExpiresAt
		return false, nil
	}
	return isClaimableOgGenerationDelivery(generation, now), nil
}

func hasActiveOgGenerationLease(generation *model.OgGeneration, now time.Time) bool {
	return generation.Status == model.OgGenerationStatusProcessing &&
		generation.LeaseExpiresAt != nil && generation.LeaseExpiresAt.After(now)
}

func isClaimableOgGenerationDelivery(generation *model.OgGeneration, now time.Time) bool {
	return generation.Status == model.OgGenerationStatusQueued ||
		generation.Status == model.OgGenerationStatusProcessing
}

func (s *Lifecycle) startOgGenerationClaim(
	tx *gorm.DB,
	generation *model.OgGeneration,
	target *model.OgGenerationTarget,
	claim *Claim,
	now time.Time,
) error {
	leaseToken := uuid.NewString()
	leaseExpiresAt := now.Add(s.lease)
	if err := persistStartedOgGenerationClaim(tx, generation.ID, leaseToken, leaseExpiresAt, now); err != nil {
		return err
	}
	applyStartedOgGenerationClaim(generation, leaseToken, leaseExpiresAt, now)
	if err := json.Unmarshal(generation.EntitySnapshot, &claim.EntitySnapshot); err != nil {
		return fmt.Errorf("decode OG entity snapshot: %w", err)
	}
	var run model.OgGenerationRun
	if err := tx.First(&run, "id = ?", generation.RunID).Error; err != nil {
		return err
	}
	populateStartedOgGenerationClaim(claim, generation, run, leaseToken, leaseExpiresAt)
	notifyOgLifecycle(tx, generation, target, model.OgGenerationStatusProcessing, nil, nil, now)
	return markOgRunStarted(tx, run.ID, now)
}

func persistStartedOgGenerationClaim(
	tx *gorm.DB,
	generationID string,
	leaseToken string,
	leaseExpiresAt time.Time,
	now time.Time,
) error {
	return tx.Model(&model.OgGeneration{}).Where("id = ?", generationID).Updates(structured.Fields{
		"status":        model.OgGenerationStatusProcessing,
		"processing_at": now, "lease_token": leaseToken, "lease_expires_at": leaseExpiresAt,
		"last_error_code": nil, "updated_at": now,
	}).Error
}

func applyStartedOgGenerationClaim(
	generation *model.OgGeneration,
	leaseToken string,
	leaseExpiresAt time.Time,
	now time.Time,
) {
	generation.Status = model.OgGenerationStatusProcessing
	generation.ProcessingAt = &now
	generation.LeaseToken = &leaseToken
	generation.LeaseExpiresAt = &leaseExpiresAt
	generation.LastErrorCode = nil
}

func populateStartedOgGenerationClaim(
	claim *Claim,
	generation *model.OgGeneration,
	run model.OgGenerationRun,
	leaseToken string,
	leaseExpiresAt time.Time,
) {
	claim.Result = Claimed
	claim.Generation = *generation
	claim.LeaseToken = leaseToken
	claim.LeaseExpiresAt = &leaseExpiresAt
	claim.RenderConfigSnapshot = append([]byte(nil), run.RenderConfigSnapshot...)
	claim.ConfigRevision = run.ConfigRevision
}
