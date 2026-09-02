package og

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
)

const (
	FailureCodeInvalidClaim       = "invalid_claim"
	FailureCodeSourceRejected     = "source_rejected"
	FailureCodeProcessingFailed   = "processing_failed"
	FailureCodeIntegrityFailed    = "integrity_failed"
	FailureCodeCompletionRejected = "completion_rejected"
)

func (s *Lifecycle) Fail(
	ctx context.Context,
	generationID string,
	leaseToken string,
	code string,
) error {
	code, err := boundedOgFailureCode(code)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		generation, target, err := lockOgGenerationAndTarget(tx, generationID)
		if err != nil {
			return err
		}
		if generation.Status == model.OgGenerationStatusSuperseded || generation.Status == model.OgGenerationStatusCancelled {
			return nil
		}
		if err := validateOgGenerationLease(generation, leaseToken, now); err != nil {
			return err
		}
		if target.LatestGenerationID == nil {
			return markOgGenerationFailed(tx, generation, FailureCodeCompletionRejected, now)
		}
		if *target.LatestGenerationID != generation.ID {
			return markOgGenerationSuperseded(tx, generation, target.LatestGenerationID, now)
		}
		return markOgGenerationFailed(tx, generation, code, now)
	})
}

func boundedOgFailureCode(code string) (string, error) {
	switch code = strings.TrimSpace(code); code {
	case FailureCodeInvalidClaim,
		FailureCodeSourceRejected,
		FailureCodeProcessingFailed,
		FailureCodeIntegrityFailed,
		FailureCodeCompletionRejected:
		return code, nil
	default:
		return "", errs.InvalidArgument("error_code", "unsupported OG failure code")
	}
}

func lockOgGenerationAndTarget(
	tx *gorm.DB,
	generationID string,
) (*model.OgGeneration, *model.OgGenerationTarget, error) {
	generation, target, locked, err := lockOgGenerationAndTargetWithOptions(tx, generationID, "")
	if err != nil {
		return nil, nil, err
	}
	if !locked {
		return nil, nil, gorm.ErrRecordNotFound
	}
	return generation, target, nil
}

func lockOgGenerationAndTargetWithOptions(
	tx *gorm.DB,
	generationID string,
	lockingOptions string,
) (*model.OgGeneration, *model.OgGenerationTarget, bool, error) {
	generationID = strings.TrimSpace(generationID)
	var generationRef struct {
		TargetID string `gorm:"column:target_id"`
	}
	if err := tx.Model(&model.OgGeneration{}).
		Select("target_id").
		Where("id = ?", generationID).
		Take(&generationRef).Error; err != nil {
		return nil, nil, false, err
	}
	locking := clause.Locking{Strength: "UPDATE", Options: lockingOptions}
	var target model.OgGenerationTarget
	if err := tx.Clauses(locking).First(&target, "id = ?", generationRef.TargetID).Error; err != nil {
		if lockingOptions != "" && errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, false, nil
		}
		return nil, nil, false, err
	}
	var generation model.OgGeneration
	if err := tx.Clauses(locking).First(&generation, "id = ?", generationID).Error; err != nil {
		if lockingOptions != "" && errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, false, nil
		}
		return nil, nil, false, err
	}
	if generation.TargetID != target.ID {
		return nil, nil, false, errs.FailedPrecondition("OG generation target changed while locking")
	}
	return &generation, &target, true, nil
}

func validateOgGenerationLease(generation *model.OgGeneration, leaseToken string, now time.Time) error {
	leaseToken = strings.TrimSpace(leaseToken)
	if generation.Status != model.OgGenerationStatusProcessing ||
		generation.LeaseToken == nil || *generation.LeaseToken != leaseToken {
		return errs.FailedPrecondition("OG generation lease is not active")
	}
	if generation.LeaseExpiresAt == nil || !generation.LeaseExpiresAt.After(now) {
		return errs.FailedPrecondition("OG generation lease has expired")
	}
	return nil
}

func validateOgGenerationCompletionLease(generation *model.OgGeneration, leaseToken string, now time.Time) error {
	leaseToken = strings.TrimSpace(leaseToken)
	if generation.Status != model.OgGenerationStatusProcessing && generation.Status != model.OgGenerationStatusSuperseded {
		return errs.FailedPrecondition("OG generation is not completable")
	}
	if generation.LeaseToken == nil || *generation.LeaseToken != leaseToken {
		return errs.FailedPrecondition("OG generation lease is not active")
	}
	if generation.LeaseExpiresAt == nil || !generation.LeaseExpiresAt.After(now) {
		return errs.FailedPrecondition("OG generation lease has expired")
	}
	return nil
}

func markOgGenerationSuperseded(
	tx *gorm.DB,
	generation *model.OgGeneration,
	replacementID *string,
	now time.Time,
) error {
	if generation.Status == model.OgGenerationStatusSuperseded {
		return nil
	}
	if generation.Status != model.OgGenerationStatusQueued &&
		generation.Status != model.OgGenerationStatusProcessing {
		return errs.FailedPrecondition("only an active OG generation can be superseded")
	}
	updates := structured.Fields{
		"status":        model.OgGenerationStatusSuperseded,
		"superseded_at": now,
		"completed_at":  now,
		"updated_at":    now,
	}
	if generation.Status != model.OgGenerationStatusProcessing && generation.Status != model.OgGenerationStatusSuperseded {
		updates["lease_token"] = nil
		updates["lease_expires_at"] = nil
	}
	if replacementID != nil && *replacementID != generation.ID {
		updates["superseded_by_id"] = *replacementID
	}
	result := tx.Model(&model.OgGeneration{}).
		Where("id = ? AND status IN ?", generation.ID, []string{
			model.OgGenerationStatusQueued,
			model.OgGenerationStatusProcessing,
		}).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errs.FailedPrecondition("OG generation changed while superseding")
	}
	generation.Status = model.OgGenerationStatusSuperseded
	generation.SupersededAt = &now
	generation.CompletedAt = &now
	var target model.OgGenerationTarget
	if err := tx.First(&target, "id = ?", generation.TargetID).Error; err != nil {
		return err
	}
	notifyOgLifecycle(tx, generation, &target, model.OgGenerationStatusSuperseded, replacementID, nil, now)
	return refreshOgRunStatus(tx, generation.RunID, now)
}

func markOgGenerationFailed(
	tx *gorm.DB,
	generation *model.OgGeneration,
	code string,
	now time.Time,
) error {
	boundedCode, err := boundedOgFailureCode(code)
	if err != nil {
		return err
	}
	result := tx.Model(&model.OgGeneration{}).
		Where("id = ? AND status = ?", generation.ID, generation.Status).
		Updates(structured.Fields{
			"status":           model.OgGenerationStatusFailed,
			"failed_at":        now,
			"completed_at":     now,
			"lease_token":      nil,
			"lease_expires_at": nil,
			"last_error_code":  optionalString(&boundedCode),
			"updated_at":       now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errs.FailedPrecondition("OG generation changed while failing")
	}
	generation.Status = model.OgGenerationStatusFailed
	generation.FailedAt = &now
	generation.CompletedAt = &now
	generation.LastErrorCode = optionalString(&boundedCode)
	var target model.OgGenerationTarget
	if err := tx.First(&target, "id = ?", generation.TargetID).Error; err != nil {
		return err
	}
	notifyOgLifecycle(tx, generation, &target, model.OgGenerationStatusFailed, nil, nil, now)
	return refreshOgRunStatus(tx, generation.RunID, now)
}

func markOgRunStarted(tx *gorm.DB, runID string, now time.Time) error {
	return tx.Model(&model.OgGenerationRun{}).
		Where("id = ? AND status = ?", runID, model.OgGenerationRunStatusQueued).
		Updates(structured.Fields{
			"status":     model.OgGenerationRunStatusRunning,
			"started_at": now,
			"updated_at": now,
		}).Error
}

func refreshOgRunStatus(tx *gorm.DB, runID string, now time.Time) error {
	var run model.OgGenerationRun
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&run, "id = ?", runID).Error; err != nil {
		return err
	}
	type countRow struct {
		Status string `gorm:"column:status"`
		Count  int64  `gorm:"column:count"`
	}
	var rows []countRow
	if err := tx.Model(&model.OgGeneration{}).
		Select("status, COUNT(*) AS count").
		Where("run_id = ?", runID).
		Group("status").
		Scan(&rows).Error; err != nil {
		return err
	}
	counts := make(map[string]int64, len(rows))
	var total int64
	for _, row := range rows {
		counts[row.Status] = row.Count
		total += row.Count
	}
	terminal := counts[model.OgGenerationStatusReady] +
		counts[model.OgGenerationStatusFailed] +
		counts[model.OgGenerationStatusSuperseded] +
		counts[model.OgGenerationStatusCancelled]
	status := model.OgGenerationRunStatusRunning
	var completedAt structured.Value
	if total > 0 && terminal == total {
		completedAt = now
		switch {
		case counts[model.OgGenerationStatusCancelled]+counts[model.OgGenerationStatusSuperseded] == total:
			status = model.OgGenerationRunStatusCancelled
		case counts[model.OgGenerationStatusReady] == total:
			status = model.OgGenerationRunStatusReady
		case counts[model.OgGenerationStatusFailed] > 0 && counts[model.OgGenerationStatusReady] > 0:
			status = model.OgGenerationRunStatusPartialFailed
		case counts[model.OgGenerationStatusFailed] == 0:
			status = model.OgGenerationRunStatusCancelled
		default:
			status = model.OgGenerationRunStatusFailed
		}
	}
	updates := structured.Fields{"status": status, "completed_at": completedAt, "updated_at": now}
	if status == model.OgGenerationRunStatusRunning || completedAt != nil {
		updates["started_at"] = gorm.Expr("COALESCE(started_at, created_at, ?)", now)
	}
	return tx.Model(&model.OgGenerationRun{}).Where("id = ?", runID).Updates(updates).Error
}

func isTerminalOgGenerationStatus(status string) bool {
	switch status {
	case model.OgGenerationStatusReady,
		model.OgGenerationStatusFailed,
		model.OgGenerationStatusSuperseded,
		model.OgGenerationStatusCancelled:
		return true
	default:
		return false
	}
}
