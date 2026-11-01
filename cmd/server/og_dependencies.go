package main

import (
	"context"

	artistadapter "github.com/echovisionlab/geul-api/internal/adapters/artist"
	formogadapter "github.com/echovisionlab/geul-api/internal/adapters/form/og"
	labeladapter "github.com/echovisionlab/geul-api/internal/adapters/label"
	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
	seriesadapter "github.com/echovisionlab/geul-api/internal/adapters/series"
	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/adapters/ogprojection"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/og"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"
	"gorm.io/gorm"
)

// ogDependencies is the one composition root for domain-neutral OG lifecycle
// dependencies and entity-owned persistence adapters.
type ogDependencies struct {
	planner     *ogpostgres.Planner
	resolver    *ogpostgres.Resolver
	collector   *ogpostgres.Collector
	refresher   *ogpostgres.Refresher
	legal       *legaladapter.Requests
	units       mutation.UnitOfWork[og.Store]
}

func newOGDependencies(db *gorm.DB, cdnDomain string) (*ogDependencies, error) {
	postRequests := postadapter.NewRequests()
	pageRequests := pageadapter.NewRequests()
	seriesRequests := seriesadapter.NewRequests()
	workRequests := workadapter.NewRequests()
	labelRequests := labeladapter.NewRequests()
	artistRequests := artistadapter.NewRequests()
	formRequests := formogadapter.NewRequests()
	legalRequests := legaladapter.NewRequests()
	siteRequests := sitesettingsadapter.NewRequests()

	projections := []ogpostgres.Projection{
		postadapter.NewProjection(), pageadapter.NewProjection(), seriesadapter.NewProjection(),
		workadapter.NewProjection(), labeladapter.NewProjection(), artistadapter.NewProjection(),
		releaseadapter.NewProjection(), formogadapter.NewProjection(), legaladapter.NewProjection(),
		sitesettingsadapter.NewProjection(),
	}
	requestSources := []ogpostgres.RequestSource{
		postRequests, pageRequests, seriesRequests, workRequests, labelRequests, artistRequests,
		formRequests, legalRequests, siteRequests,
	}
	allSources := []ogpostgres.AllRequestSource{
		postRequests, pageRequests, seriesRequests, workRequests, labelRequests, artistRequests,
		formRequests, legalRequests, siteRequests,
	}
	renderConfig := sitesettingsadapter.NewRenderConfig()
	planner := ogpostgres.NewPlanner(cdnDomain, renderConfig, projections...)
	resolver := ogpostgres.NewResolver(requestSources...)
	collector := ogpostgres.NewCollector(allSources...)
	units, err := ogpostgres.NewUnits(db, ogpostgres.Ports{
		CDNDomain: cdnDomain, RenderConfig: renderConfig, Projections: projections,
		Requests: resolver, AllRequests: collector, Reconciler: labeladapter.NewGlobalReconciler(),
		References: ogprojection.AssetReferences(),
	})
	if err != nil {
		return nil, err
	}
	return &ogDependencies{
		planner: planner, resolver: resolver, collector: collector,
		refresher: ogpostgres.NewRefresher(planner, resolver), legal: legalRequests, units: units,
	}, nil
}

func (d *ogDependencies) siteInvalidator() *sitesettingsadapter.Invalidator {
	return sitesettingsadapter.NewInvalidator(
		d.planner,
		func(ctx context.Context, db *gorm.DB) ([]og.Request, error) { return d.collector.Collect(ctx, db) },
		func(ctx context.Context, db *gorm.DB, kind string, background *string) ([]og.Request, error) {
			return legaladapter.CurrentRequests(ctx, db, kind, background)
		},
	)
}
