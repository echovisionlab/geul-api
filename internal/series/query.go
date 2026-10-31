package series

import (
	"context"
	"errors"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
)

func (s *Service) loadSeriesListDetails(
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

type seriesManagerAttribution struct {
	MemberID  string
	CreatedAt time.Time
}

func loadSeriesManagerAttributions(ctx context.Context, db *gorm.DB, seriesID string) ([]seriesManagerAttribution, error) {
	var rows []seriesManagerAttribution
	err := db.WithContext(ctx).Table("series_manager").Select("member_id", "created_at").
		Where("series_id = ?", seriesID).Order("created_at ASC, member_id ASC").Scan(&rows).Error
	return rows, err
}

func (s *Service) loadSeriesManagerProjections(ctx context.Context, seriesID string) ([]*managev1.SeriesManager, error) {
	rows, err := loadSeriesManagerAttributions(ctx, s.db, seriesID)
	if err != nil {
		return nil, err
	}
	result := make([]*managev1.SeriesManager, 0, len(rows))
	if len(rows) == 0 {
		return result, nil
	}
	memberIDs := make([]string, len(rows))
	for i := range rows {
		memberIDs[i] = rows[i].MemberID
	}
	summaries, err := s.members.LoadSeriesManagers(ctx, memberIDs)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		summary := summaries[row.MemberID]
		if summary == nil {
			return nil, errors.New("series manager member was not found")
		}
		manager := proto.Clone(summary).(*managev1.SeriesManager)
		manager.CreatedAt = timestamppb.New(row.CreatedAt)
		result = append(result, manager)
	}
	return result, nil
}
