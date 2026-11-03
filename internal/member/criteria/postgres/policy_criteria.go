package postgres

import (
	"context"

	membercriteria "github.com/echovisionlab/geul-api/internal/member/criteria"
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
)

// ActivePolicyCriteriaSet returns active Member criteria, including ones ineligible
// for authenticated access.
func ActivePolicyCriteriaSet(ctx context.Context, db *gorm.DB, ids []string) ([]membercriteria.Criteria, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []model.MemberCriteria
	if err := db.WithContext(ctx).Where("id IN ? AND archived_at IS NULL", ids).Order("LOWER(name) ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]membercriteria.Criteria, len(rows))
	for i, r := range rows {
		result[i] = CriteriaOf(r)
	}
	return result, nil
}
