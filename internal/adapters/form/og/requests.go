package formog

import (
	"context"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/og"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

// Requests reads exact-current Form locale snapshots for OG generation.
type Requests struct{ localized *og.LocalizedRequests }

func NewRequests() *Requests {
	return &Requests{localized: og.NewLocalizedRequests(og.LocalizedRequestSpec{
		EntityType:            "form",
		Table:                 "form",
		TranslationTable:      "form_translation",
		SourceTitleExpression: formdomain.FormSourceTitleSQL("form"),
	})}
}

func (*Requests) Handles(entityType string) bool { return entityType == "form" }

func (r *Requests) Resolve(
	ctx context.Context,
	db *gorm.DB,
	entityType, entityID string,
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
