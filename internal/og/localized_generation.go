package og

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
)

type LocalizedGenerationDisposition int

const (
	LocalizedGenerationMissing LocalizedGenerationDisposition = iota
	LocalizedGenerationPending
	LocalizedGenerationReady
	LocalizedGenerationTerminal
)

// ResolveExactLocalizedGeneration classifies the latest generation attached to
// an existing locale target. Source identity is render provenance, not a
// currentness gate.
func ResolveExactLocalizedGeneration(
	ctx context.Context,
	db *gorm.DB,
	entityType, entityID, locale string,
) (LocalizedGenerationDisposition, error) {
	var row struct {
		Status string `gorm:"column:status"`
	}
	err := db.WithContext(ctx).Table("og_generation_target AS target").
		Select("generation.status").
		Joins("JOIN og_generation AS generation ON generation.id = target.latest_generation_id").
		Where("target.entity_type = ? AND target.entity_id = ? AND target.target_kind = 'locale' AND target.locale = ?", entityType, entityID, locale).
		Take(&row).Error
	if err == gorm.ErrRecordNotFound {
		return LocalizedGenerationMissing, nil
	}
	if err != nil {
		return LocalizedGenerationMissing, err
	}
	switch row.Status {
	case model.OgGenerationStatusQueued, model.OgGenerationStatusProcessing:
		return LocalizedGenerationPending, nil
	case model.OgGenerationStatusReady:
		return LocalizedGenerationReady, nil
	default:
		return LocalizedGenerationTerminal, nil
	}
}
