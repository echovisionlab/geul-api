package filemedia

import (
	"context"

	filemediapostgres "github.com/echovisionlab/geul-api/internal/filemedia/postgres"
	"github.com/echovisionlab/geul-api/internal/transcode/application"
	"gorm.io/gorm"
)

type DerivativeResults struct{}

func (DerivativeResults) Upsert(ctx context.Context, tx *gorm.DB, result application.Derivative) error {
	return filemediapostgres.UpsertResultDerivative(ctx, tx, result.ID, result.FileID, result.Type, result.AssetID, result.MediaGenerationID)
}
