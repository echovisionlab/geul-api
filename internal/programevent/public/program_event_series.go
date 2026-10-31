package public

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

type ProgramEventSeriesService struct {
	openv1connect.UnimplementedProgramEventSeriesServiceHandler
	db     *gorm.DB
	assets Assets
}

func NewProgramEventSeriesService(db *gorm.DB, assets Assets) *ProgramEventSeriesService {
	if assets == nil {
		panic("Program Event public assets are required")
	}
	return &ProgramEventSeriesService{db: db, assets: assets}
}
func (s *ProgramEventSeriesService) Get(
	ctx context.Context,
	req *connect.Request[openv1.GetProgramEventSeriesRequest],
) (*connect.Response[openv1.GetProgramEventSeriesResponse], error) {
	slugOrID := strings.TrimSpace(req.Msg.Slug)
	if slugOrID == "" {
		return nil, errs.Required("slug")
	}

	var series model.ProgramEventSeries
	query := s.db.WithContext(ctx).
		Model(&model.ProgramEventSeries{}).
		Where("status = ?", managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String())
	if _, parseErr := uuid.Parse(slugOrID); parseErr == nil {
		query = query.Where("id = ?", slugOrID)
	} else {
		query = query.Where("slug = ?", slugOrID)
	}
	if err := query.First(&series).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFoundMsg("program event series not found")
		}
		return nil, errs.Internal(err)
	}
	protoSeries, err := s.toProtoProgramEventSeries(ctx, &series, req.Header().Get("Accept-Language"))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&openv1.GetProgramEventSeriesResponse{Series: protoSeries}), nil
}

func (s *ProgramEventSeriesService) List(
	ctx context.Context,
	req *connect.Request[openv1.ListProgramEventSeriesRequest],
) (*connect.Response[openv1.ListProgramEventSeriesResponse], error) {
	query := s.db.WithContext(ctx).
		Model(&model.ProgramEventSeries{}).
		Where("status = ?", managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String())
	var err error
	query, err = applySimpleProgramEventSeriesFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}
	query = query.Order("updated_at DESC")
	pagination := queryutil.ExtractPagination(req.Msg.Pagination, queryutil.DefaultPaginationConfig)
	query = pagination.Apply(query)

	var rows []model.ProgramEventSeries
	if err := query.Find(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	result, err := s.toProtoProgramEventSeriesList(ctx, rows)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&openv1.ListProgramEventSeriesResponse{
		Series:     result,
		Pagination: pagination.BuildResponse(total),
	}), nil
}
func (s *ProgramEventSeriesService) toProtoProgramEventSeries(
	ctx context.Context,
	series *model.ProgramEventSeries,
	_ string,
) (*openv1.ProgramEventSeries, error) {
	result := openProgramEventSeriesProto(series)
	if posterAsset := loadProgramEventFileAsset(ctx, s.assets, series.PosterFileID); posterAsset != nil {
		result.PosterAsset = posterAsset
	}
	return result, nil
}

func (s *ProgramEventSeriesService) toProtoProgramEventSeriesList(
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
		return nil, errs.Internal(err)
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
