//go:build integration

package worker

import (
	formogadapter "github.com/echovisionlab/geul-api/internal/adapters/form/og"
	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	seriesadapter "github.com/echovisionlab/geul-api/internal/adapters/series"
	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/og"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

func workerOGProjections() []og.Projection {
	return []og.Projection{
		postadapter.NewProjection(), pageadapter.NewProjection(), seriesadapter.NewProjection(),
		workadapter.NewProjection(), formogadapter.NewProjection(), legaladapter.NewProjection(),
		sitesettingsadapter.NewProjection(),
	}
}

func newWorkerOGPlanner(db *gorm.DB, cdnDomain string) *og.Planner {
	return og.NewPlanner(db, cdnDomain, sitesettingsadapter.NewRenderConfig(), workerOGProjections()...)
}

func newWorkerOGLifecycle(db *gorm.DB, cdnDomain string) *og.Lifecycle {
	return og.NewLifecycle(db, cdnDomain, workerOGProjections()...)
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
