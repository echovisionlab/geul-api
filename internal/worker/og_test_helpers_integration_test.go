//go:build integration

package worker

import (
	artistadapter "github.com/echovisionlab/geul-api/internal/adapters/artist"
	formogadapter "github.com/echovisionlab/geul-api/internal/adapters/form/og"
	labeladapter "github.com/echovisionlab/geul-api/internal/adapters/label"
	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	"github.com/echovisionlab/geul-api/internal/adapters/ogprojection"
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
	seriesadapter "github.com/echovisionlab/geul-api/internal/adapters/series"
	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/og"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

func workerOGProjections() []ogpostgres.Projection {
	return []ogpostgres.Projection{
		postadapter.NewProjection(), pageadapter.NewProjection(), seriesadapter.NewProjection(),
		workadapter.NewProjection(), labeladapter.NewProjection(), artistadapter.NewProjection(),
		releaseadapter.NewProjection(), formogadapter.NewProjection(), legaladapter.NewProjection(),
		sitesettingsadapter.NewProjection(),
	}
}

func newWorkerOGPlanner(db *gorm.DB, cdnDomain string) *ogpostgres.Planner {
	return ogpostgres.NewPlanner(cdnDomain, sitesettingsadapter.NewRenderConfig(), workerOGProjections()...)
}

func newWorkerOGLifecycle(db *gorm.DB, cdnDomain string) *og.Lifecycle {
	return og.NewLifecycle(newWorkerOGUnits(db, cdnDomain))
}

func newWorkerOGUnits(db *gorm.DB, cdnDomain string) mutation.UnitOfWork[og.Store] {
	requests := postadapter.NewRequests()
	units, err := ogpostgres.NewUnits(db, ogpostgres.Ports{
		CDNDomain: cdnDomain, RenderConfig: sitesettingsadapter.NewRenderConfig(), Projections: workerOGProjections(),
		Requests: ogpostgres.NewResolver(requests), AllRequests: ogpostgres.NewCollector(requests),
		Reconciler: ogpostgres.NoopGlobalReconciler{}, References: ogprojection.AssetReferences(),
	})
	if err != nil {
		panic(err)
	}
	return units
}

func workerOGRequest(
	entityType managev1.OgEntityType,
	entityID, title string,
	locale, featuredImageFileID *string,
) og.Request {
	policy, _ := og.PolicyForEntityType(entityType)
	kind := "entity"
	if locale == nil && policy.LocaleStrategy == og.LocaleStrategyTranslated {
		defaultLocale := "en"
		locale = &defaultLocale
	}
	if locale != nil {
		kind = "locale"
	}
	return og.Request{
		Target:              og.Target{EntityType: policy.Name, EntityID: entityID, Locale: locale, Kind: kind},
		Title:               title,
		FeaturedImageFileID: featuredImageFileID,
	}
}
