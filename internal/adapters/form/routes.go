package form

import (
	"context"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"gorm.io/gorm"
)

type Routes struct{}

func NewRoutes() *Routes { return &Routes{} }
func (*Routes) SlugAvailable(ctx context.Context, db *gorm.DB, slug, _ string) (bool, error) {
	return routeregistry.IsResourceRouteAvailable(ctx, db, "forms", slug)
}
func (*Routes) EnsureAvailable(ctx context.Context, db *gorm.DB, slug, _ string) error {
	available, err := routeregistry.IsResourceRouteAvailable(ctx, db, "forms", slug)
	if err != nil {
		return err
	}
	if !available {
		return errs.SlugAlreadyExists("form", slug)
	}
	return nil
}
func (*Routes) EnsureAvailableLocked(ctx context.Context, db *gorm.DB, slug, _ string) error {
	return routeregistry.EnsureResourceRouteAvailableInTx(ctx, db, "form", "forms", slug)
}

var _ formdomain.Routes = (*Routes)(nil)
