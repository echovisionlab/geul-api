package referencecatalogadapter

import (
	"context"

	postpostgres "github.com/echovisionlab/geul-api/internal/post/postgres"
	programeventpostgres "github.com/echovisionlab/geul-api/internal/programevent/postgres"
	workpostgres "github.com/echovisionlab/geul-api/internal/work/postgres"
	"gorm.io/gorm"
)

type DeletionReferences struct{}

func (DeletionReferences) MapPlaceInUse(ctx context.Context, tx *gorm.DB, id string) (bool, error) {
	for _, read := range []func(context.Context, *gorm.DB, string) (bool, error){postpostgres.MapPlaceInUse, workpostgres.MapPlaceInUse, programeventpostgres.MapPlaceInUse} {
		used, err := read(ctx, tx, id)
		if err != nil || used {
			return used, err
		}
	}
	return false, nil
}
func (DeletionReferences) ClientInUse(ctx context.Context, tx *gorm.DB, id string) (bool, error) {
	for _, read := range []func(context.Context, *gorm.DB, string) (bool, error){workpostgres.ClientInUse, programeventpostgres.ClientInUse} {
		used, err := read(ctx, tx, id)
		if err != nil || used {
			return used, err
		}
	}
	return false, nil
}
