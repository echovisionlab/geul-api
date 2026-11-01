package filemediapolicy

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/work"
	workpostgres "github.com/echovisionlab/geul-api/internal/work/postgres"
	"gorm.io/gorm"
)

type WorkExistence struct{}

func (WorkExistence) RequireExists(ctx context.Context, tx *gorm.DB, id string) error {
	return work.RequireExists(ctx, workpostgres.Bind(tx, workpostgres.Ports{}, nil).Store, id)
}
