package mediaasset

import (
	"context"

	audiencepostgres "github.com/echovisionlab/geul-api/internal/audience/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
)

type SegmentConfigs struct{}

func NewSegmentConfigs() SegmentConfigs { return SegmentConfigs{} }

func (SegmentConfigs) LoadSegmentConfigs(ctx context.Context, db *gorm.DB, segments []*model.AudienceSegment) error {
	return audiencepostgres.LoadSegmentConfigs(ctx, db, segments)
}
