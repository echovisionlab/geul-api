package release

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
)

func lockReleaseForUpdate(ctx context.Context, tx *gorm.DB, releaseID string) (*model.Release, error) {
	var release model.Release
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&release, "id = ?", releaseID).Error; err != nil {
		return nil, err
	}
	return &release, nil
}
