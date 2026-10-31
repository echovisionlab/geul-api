package public

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// GetProgramEventSeriesQuery names a published series by slug or ID.
type GetProgramEventSeriesQuery struct {
	SlugOrID string
}

// GetProgramEventSeries returns a published series.
func (s *Service) GetProgramEventSeries(ctx context.Context, query GetProgramEventSeriesQuery) (*openv1.ProgramEventSeries, error) {
	slugOrID := strings.TrimSpace(query.SlugOrID)
	if slugOrID == "" {
		return nil, domainerr.Required("slug")
	}
	var series model.ProgramEventSeries
	rows := s.db.WithContext(ctx).
		Model(&model.ProgramEventSeries{}).
		Where("status = ?", model.ProgramEventStatusPublished)
	if _, parseErr := uuid.Parse(slugOrID); parseErr == nil {
		rows = rows.Where("id = ?", slugOrID)
	} else {
		rows = rows.Where("slug = ?", slugOrID)
	}
	if err := rows.First(&series).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.New(domainerr.NotFound, "", "program event series not found")
		}
		return nil, err
	}
	return toProtoProgramEventSeries(ctx, s.assets, &series), nil
}

// ListProgramEventSeriesQuery is one page of published series.
type ListProgramEventSeriesQuery struct {
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
}

// SeriesList is one page of published series.
type SeriesList struct {
	Series     []*openv1.ProgramEventSeries
	Pagination *commonv1.PaginationResponse
}

// ListProgramEventSeries returns one page of published series, most
// recently updated first.
func (s *Service) ListProgramEventSeries(ctx context.Context, query ListProgramEventSeriesQuery) (SeriesList, error) {
	rows := s.db.WithContext(ctx).
		Model(&model.ProgramEventSeries{}).
		Where("status = ?", model.ProgramEventStatusPublished)
	rows, err := applySimpleProgramEventSeriesFilters(rows, query.Filters)
	if err != nil {
		return SeriesList{}, err
	}
	var total int64
	if err := rows.Count(&total).Error; err != nil {
		return SeriesList{}, err
	}
	pagination := queryutil.ExtractPaginationRaw(query.Limit, query.Offset, queryutil.DefaultPaginationConfig)
	var series []model.ProgramEventSeries
	if err := pagination.Apply(rows.Order("updated_at DESC")).Find(&series).Error; err != nil {
		return SeriesList{}, err
	}
	result, err := s.toProtoProgramEventSeriesList(ctx, series)
	if err != nil {
		return SeriesList{}, err
	}
	return SeriesList{Series: result, Pagination: pagination.BuildResponse(total)}, nil
}

func toProtoProgramEventSeries(ctx context.Context, assets Assets, series *model.ProgramEventSeries) *openv1.ProgramEventSeries {
	result := openProgramEventSeriesProto(series)
	if posterAsset := loadProgramEventFileAsset(ctx, assets, series.PosterFileID); posterAsset != nil {
		result.PosterAsset = posterAsset
	}
	return result
}

func (s *Service) toProtoProgramEventSeriesList(
	ctx context.Context,
	series []model.ProgramEventSeries,
) ([]*openv1.ProgramEventSeries, error) {
	fileIDs := make([]string, 0, len(series))
	for i := range series {
		if series[i].PosterFileID != nil {
			fileIDs = append(fileIDs, *series[i].PosterFileID)
		}
	}
	assetsByFile, err := s.assets.ResolveReadyAssetsForSourceFiles(ctx, fileIDs, "image", "poster", "map_image")
	if err != nil {
		return nil, err
	}
	result := make([]*openv1.ProgramEventSeries, 0, len(series))
	for i := range series {
		item := openProgramEventSeriesProto(&series[i])
		if series[i].PosterFileID != nil {
			item.PosterAsset = assetsByFile[*series[i].PosterFileID]
		}
		result = append(result, item)
	}
	return result, nil
}

func openProgramEventSeriesProto(series *model.ProgramEventSeries) *openv1.ProgramEventSeries {
	return &openv1.ProgramEventSeries{
		Id:          series.ID,
		Status:      openProgramEventSeriesStatus(series.Status),
		Title:       series.Title,
		Slug:        series.Slug,
		Summary:     series.Summary,
		Description: series.Description,
	}
}
