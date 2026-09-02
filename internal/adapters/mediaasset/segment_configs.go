package mediaasset

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/audience"
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
)

type SegmentConfigs struct{}

func NewSegmentConfigs() SegmentConfigs { return SegmentConfigs{} }

func (SegmentConfigs) LoadSegmentConfigs(ctx context.Context, db *gorm.DB, segments []*model.AudienceSegment) error {
	return audience.LoadSegmentConfigs(ctx, db, segments)
}
