package og

import (
	"context"
	"strings"

	"gorm.io/gorm"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Refresher requests canonical OG work after an owning entity mutation.
type Refresher struct {
	planner  *Planner
	resolver *Resolver
}

func NewRefresher(planner *Planner, resolver *Resolver) *Refresher {
	if planner == nil || resolver == nil {
		panic("OG refresh dependencies are required")
	}
	return &Refresher{planner: planner, resolver: resolver}
}

func (r *Refresher) RequestCurrentWithDB(
	ctx context.Context,
	tx *gorm.DB,
	entityType managev1.OgEntityType,
	entityID string,
	locale string,
	allLocales bool,
	reason string,
) (*Plan, error) {
	selection, err := refreshSelection(locale, allLocales)
	if err != nil {
		return nil, err
	}
	request := &managev1.RegenerateOgImageRequest{
		EntityType: entityType,
		EntityId:   &entityID,
		Selection:  selection,
	}
	requests, err := r.resolver.Resolve(ctx, tx, request)
	if err != nil {
		return nil, err
	}
	return r.planner.RequestBulkReloadedWithDB(
		ctx,
		tx,
		"automatic",
		reason,
		requests,
		func(reloadCtx context.Context, reloadTx *gorm.DB) ([]Request, error) {
			return r.resolver.Resolve(reloadCtx, reloadTx, request)
		},
	)
}

func refreshSelection(locale string, allLocales bool) (*managev1.OgTargetSelection, error) {
	locale = strings.TrimSpace(locale)
	if locale != "" {
		return &managev1.OgTargetSelection{
			Target: &managev1.OgTargetSelection_Locale{Locale: locale},
		}, nil
	}
	if allLocales {
		return &managev1.OgTargetSelection{
			Target: &managev1.OgTargetSelection_AllLocales{
				AllLocales: &managev1.OgAllLocaleTargets{},
			},
		}, nil
	}
	return &managev1.OgTargetSelection{
		Target: &managev1.OgTargetSelection_Primary{Primary: &managev1.OgPrimaryTarget{}},
	}, nil
}
