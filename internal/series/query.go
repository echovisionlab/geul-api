package series

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
)

func (s *SeriesService) loadSeriesListDetails(
	ctx context.Context,
	seriesList []model.Series,
) (map[string]SeriesListDetails, error) {
	details := make(map[string]SeriesListDetails, len(seriesList))
	seriesIDs := make([]string, 0, len(seriesList))
	fileIDs := make([]string, 0, len(seriesList))
	for i := range seriesList {
		seriesIDs = append(seriesIDs, seriesList[i].ID)
		details[seriesList[i].ID] = SeriesListDetails{}
		if seriesList[i].FeaturedImageFileID != nil && *seriesList[i].FeaturedImageFileID != "" {
			fileIDs = append(fileIDs, *seriesList[i].FeaturedImageFileID)
		}
	}
	postCounts, err := s.reads.LoadPostCounts(ctx, seriesIDs)
	if err != nil {
		return nil, err
	}
	managerCounts, err := s.reads.LoadManagerCounts(ctx, seriesIDs)
	if err != nil {
		return nil, err
	}
	featuredAssets, err := s.media.ReadyAssetsForSourceFiles(ctx, "image", fileIDs)
	if err != nil {
		return nil, err
	}
	for i := range seriesList {
		item := details[seriesList[i].ID]
		item.PostCount = postCounts[seriesList[i].ID]
		item.ManagerCount = managerCounts[seriesList[i].ID]
		if seriesList[i].FeaturedImageFileID != nil {
			item.FeaturedImageAsset = featuredAssets[*seriesList[i].FeaturedImageFileID]
		}
		details[seriesList[i].ID] = item
	}
	return details, nil
}

func loadSeriesManagerMemberIDs(ctx context.Context, db *gorm.DB, seriesID string) ([]string, error) {
	var memberIDs []string
	if err := db.WithContext(ctx).Table("series_manager").
		Where("series_id = ?", seriesID).
		Order("created_at ASC, member_id ASC").
		Pluck("member_id", &memberIDs).Error; err != nil {
		return nil, err
	}
	return memberIDs, nil
}
