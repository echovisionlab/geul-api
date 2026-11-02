package series

import (
	"context"
	"errors"
)

func (s *Service) loadSeriesListDetails(
	ctx context.Context,
	seriesList []Series,
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

// seriesManagers projects the Member summary of each attributed manager,
// in attribution order.
func (s *Service) seriesManagers(ctx context.Context, attributions []ManagerAttribution) ([]Manager, error) {
	result := make([]Manager, 0, len(attributions))
	if len(attributions) == 0 {
		return result, nil
	}
	memberIDs := make([]string, len(attributions))
	for i := range attributions {
		memberIDs[i] = attributions[i].MemberID
	}
	summaries, err := s.members.LoadSeriesManagers(ctx, memberIDs)
	if err != nil {
		return nil, err
	}
	for _, attribution := range attributions {
		summary, ok := summaries[attribution.MemberID]
		if !ok {
			return nil, errors.New("series manager member was not found")
		}
		result = append(result, Manager{
			MemberID: summary.MemberID, Nickname: summary.Nickname, Avatar: summary.Avatar, CreatedAt: attribution.CreatedAt,
		})
	}
	return result, nil
}
