package main

import (
	"context"

	formogadapter "github.com/echovisionlab/geul-api/internal/adapters/form/og"
	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	seriesadapter "github.com/echovisionlab/geul-api/internal/adapters/series"
	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/og"
	"gorm.io/gorm"
)

// ogDependencies is the one composition root for domain-neutral OG lifecycle
// dependencies and entity-owned persistence adapters.
type ogDependencies struct {
	planner     *og.Planner
	resolver    *og.Resolver
	collector   *og.Collector
	refresher   *og.Refresher
	legal       *legaladapter.Requests
	projections []og.Projection
}

func newOGDependencies(db *gorm.DB, cdnDomain string) *ogDependencies {
	postRequests := postadapter.NewRequests()
	pageRequests := pageadapter.NewRequests()
	seriesRequests := seriesadapter.NewRequests()
	workRequests := workadapter.NewRequests()
	formRequests := formogadapter.NewRequests()
	legalRequests := legaladapter.NewRequests()
	siteRequests := sitesettingsadapter.NewRequests()

	projections := []og.Projection{
		postadapter.NewProjection(), pageadapter.NewProjection(), seriesadapter.NewProjection(),
		workadapter.NewProjection(), formogadapter.NewProjection(), legaladapter.NewProjection(),
		sitesettingsadapter.NewProjection(),
	}
	requestSources := []og.RequestSource{
		postRequests, pageRequests, seriesRequests, workRequests,
		formRequests, legalRequests, siteRequests,
	}
	allSources := []og.AllRequestSource{
		postRequests, pageRequests, seriesRequests, workRequests,
		formRequests, legalRequests, siteRequests,
	}
	planner := og.NewPlanner(db, cdnDomain, sitesettingsadapter.NewRenderConfig(), projections...)
	resolver := og.NewResolver(requestSources...)
	collector := og.NewCollector(allSources...)
	return &ogDependencies{
		planner: planner, resolver: resolver, collector: collector,
		refresher: og.NewRefresher(planner, resolver), legal: legalRequests, projections: projections,
	}
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
