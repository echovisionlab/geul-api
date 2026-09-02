package page

import (
	"context"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/og"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

const sourceTitleSQL = "COALESCE((SELECT translation.title FROM page_translation AS translation " +
	"WHERE translation.entity_id = page.id AND translation.locale = page.source_locale LIMIT 1), '')"

type Requests struct{ localized *og.LocalizedRequests }

func NewRequests() *Requests {
	return &Requests{localized: og.NewLocalizedRequests(og.LocalizedRequestSpec{
		EntityType:            "page",
		Table:                 "page",
		TranslationTable:      "page_translation",
		SourceTitleExpression: sourceTitleSQL,
	})}
}

func (*Requests) Handles(entityType string) bool { return entityType == "page" }

func (r *Requests) Resolve(
	ctx context.Context,
	db *gorm.DB,
	entityType string,
	entityID string,
	selection *managev1.OgTargetSelection,
) ([]og.Request, error) {
	if !r.Handles(entityType) {
		return nil, errs.InvalidEntityType(entityType)
	}
	return r.localized.Resolve(ctx, db, entityID, selection)
}

func (r *Requests) All(ctx context.Context, db *gorm.DB) ([]og.Request, error) {
	return r.localized.All(ctx, db)
}
