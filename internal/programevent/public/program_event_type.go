package public

import (
	"context"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

type ProgramEventTypeService struct {
	openv1connect.UnimplementedProgramEventTypeServiceHandler
	db *gorm.DB
}

func NewProgramEventTypeService(db *gorm.DB) *ProgramEventTypeService {
	return &ProgramEventTypeService{db: db}
}
func (s *ProgramEventTypeService) List(
	ctx context.Context,
	req *connect.Request[openv1.ListProgramEventTypesRequest],
) (*connect.Response[openv1.ListProgramEventTypesResponse], error) {
	query := s.db.WithContext(ctx).
		Model(&model.ProgramEventType{}).
		Where("status = ?", openv1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_ACTIVE.String())
	var err error
	query, err = applySimpleProgramEventTypeFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}
	query = query.Order("sort_order ASC, slug ASC")
	pagination := queryutil.ExtractPagination(req.Msg.Pagination, queryutil.DefaultPaginationConfig)
	query = pagination.Apply(query)

	var rows []model.ProgramEventType
	if err := query.Find(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	typeIDs := make([]string, 0, len(rows))
	for i := range rows {
		typeIDs = append(typeIDs, rows[i].ID)
	}
	localesByType, err := loadPublicProgramEventTypeLocales(ctx, s.db, typeIDs)
	if err != nil {
		return nil, err
	}
	result := make([]*openv1.ProgramEventType, 0, len(rows))
	for i := range rows {
		item, err := publicProgramEventTypeProto(rows[i], localesByType[rows[i].ID], req.Header().Get("Accept-Language"))
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return connect.NewResponse(&openv1.ListProgramEventTypesResponse{
		Types:      result,
		Pagination: pagination.BuildResponse(total),
	}), nil
}
