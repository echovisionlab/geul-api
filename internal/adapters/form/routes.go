package form

import (
	"context"

	"gorm.io/gorm"

	formpostgres "github.com/echovisionlab/geul-api/internal/form/postgres"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
)

type Routes struct{}

func NewRoutes() *Routes { return &Routes{} }

func (*Routes) SlugAvailable(ctx context.Context, db *gorm.DB, slug string) (bool, error) {
	return routeregistry.IsResourceRouteAvailable(ctx, db, "forms", slug)
}

func (*Routes) EnsureAvailableLocked(ctx context.Context, db *gorm.DB, slug string) error {
	return routeregistry.EnsureResourceRouteAvailableInTx(ctx, db, "form", "forms", slug)
}

var _ formpostgres.Routes = (*Routes)(nil)
