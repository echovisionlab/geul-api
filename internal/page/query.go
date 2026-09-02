package page

import (
	"context"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
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
			managev1.PageStatus_PAGE_STATUS_DRAFT.String(),
			managev1.PageStatus_PAGE_STATUS_PUBLISHED.String(),
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
		return false, errs.Internal(err)
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
		return errs.SlugAlreadyExists(entity, slug)
	}
	return nil
}
