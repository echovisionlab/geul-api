package filemediapostgres

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UpsertResultDerivative applies one processing result in the caller's transaction.
func UpsertResultDerivative(ctx context.Context, tx *gorm.DB, id, fileID, kind string, assetID, generationID *string) error {
	row := model.FileDerivative{ID: id, FileID: fileID, Type: managev1.FileDerivativeType(managev1.FileDerivativeType_value[kind]), AssetID: assetID, MediaGenerationID: generationID}
	return tx.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "file_id"}, {Name: "type"}}, DoUpdates: clause.Assignments(structured.Fields{"asset_id": row.AssetID, "media_generation_id": row.MediaGenerationID})}).Create(&row).Error
}
