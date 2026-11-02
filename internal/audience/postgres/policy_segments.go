package postgres

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/audience"
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
)

// ActivePolicySegments returns active segments, including ones ineligible
// for authenticated access.
func ActivePolicySegments(ctx context.Context, db *gorm.DB, ids []string) ([]audience.Segment, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []model.AudienceSegment
	if err := db.WithContext(ctx).Where("id IN ? AND archived_at IS NULL", ids).Order("LOWER(name) ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]audience.Segment, len(rows))
	for i, r := range rows {
		result[i] = SegmentOf(r)
	}
	return result, nil
}
