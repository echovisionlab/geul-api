package page

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
)

var PageFilterConfig = &queryutil.FilterConfig{Fields: map[string]queryutil.FieldDef{
	"search": {
		Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps,
		SearchColumns: []string{PageSourceTitleSQL("page")},
	},
	"status": {
		Column: "status", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps,
		EnumValues: []string{
			string(model.PageStatusDraft),
			string(model.PageStatusPublished),
		},
	},
}}

func isSlugAvailable(
	ctx context.Context,
	db *gorm.DB,
	modelValue structured.Value,
	slug string,
	excludeID string,
) (bool, error) {
	if slug == "" {
		return true, nil
	}
	query := db.WithContext(ctx).Model(modelValue).Where("slug = ?", slug)
	if excludeID != "" {
		query = query.Where("id != ?", excludeID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count == 0, nil
}

func ensureSlugAvailable(
	ctx context.Context,
	db *gorm.DB,
	modelValue structured.Value,
	entity string,
	slug string,
	excludeID string,
) error {
	available, err := isSlugAvailable(ctx, db, modelValue, slug, excludeID)
	if err != nil {
		return err
	}
	if !available {
		return domainerr.Duplicate(entity, "slug", slug)
	}
	return nil
}
