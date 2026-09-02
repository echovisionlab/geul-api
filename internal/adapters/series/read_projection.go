package series

import (
	"context"

	"gorm.io/gorm"
)

type ReadProjection struct {
	db *gorm.DB
}

func NewReadProjection(db *gorm.DB) *ReadProjection {
	if db == nil {
		panic("series read projection: database is required")
	}
	return &ReadProjection{db: db}
}

func (p *ReadProjection) LoadPostCounts(ctx context.Context, seriesIDs []string) (map[string]int32, error) {
	counts := make(map[string]int32, len(seriesIDs))
	if len(seriesIDs) == 0 {
		return counts, nil
	}
	type countRow struct {
		SeriesID string `gorm:"column:series_id"`
		Count    int32  `gorm:"column:count"`
	}
	var postCounts []countRow
	if err := p.db.WithContext(ctx).Table("post").Select("series_id, COUNT(*)::integer AS count").
		Where("series_id IN ?", seriesIDs).Group("series_id").Scan(&postCounts).Error; err != nil {
		return nil, err
	}
	for _, row := range postCounts {
		counts[row.SeriesID] = row.Count
	}
	return counts, nil
}

func (p *ReadProjection) LoadManagerCounts(ctx context.Context, seriesIDs []string) (map[string]int32, error) {
	counts := make(map[string]int32, len(seriesIDs))
	if len(seriesIDs) == 0 {
		return counts, nil
	}
	type countRow struct {
		SeriesID string `gorm:"column:series_id"`
		Count    int32  `gorm:"column:count"`
	}
	var managerCounts []countRow
	if err := p.db.WithContext(ctx).Table("series_manager").Select("series_id, COUNT(*)::integer AS count").
		Where("series_id IN ?", seriesIDs).Group("series_id").Scan(&managerCounts).Error; err != nil {
		return nil, err
	}
	for _, row := range managerCounts {
		counts[row.SeriesID] = row.Count
	}
	return counts, nil
}
