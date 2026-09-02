package public

import (
	"context"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var publicClientSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"id":         "id",
		"name":       "name",
		"website":    "website",
		"created_at": "created_at",
	},
	DefaultSort: "name ASC, id ASC",
}

type ClientService struct {
	openv1connect.UnimplementedClientServiceHandler
	db     *gorm.DB
	assets AssetReader
}

func NewClientService(db *gorm.DB, assets AssetReader) *ClientService {
	if db == nil {
		panic("db is required")
	}
	if assets == nil {
		panic("assets are required")
	}
	return &ClientService{db: db, assets: assets}
}

func (s *ClientService) Get(
	ctx context.Context,
	req *connect.Request[openv1.GetClientRequest],
) (*connect.Response[openv1.GetClientResponse], error) {
	var client model.Client
	if err := s.db.WithContext(ctx).First(&client, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("client", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}

	return connect.NewResponse(&openv1.GetClientResponse{
		Client: s.toProtoClientSummary(ctx, &client),
	}), nil
}

func (s *ClientService) List(
	ctx context.Context,
	req *connect.Request[openv1.ListClientsRequest],
) (*connect.Response[openv1.ListClientsResponse], error) {
	var clients []model.Client
	var total int64

	query := s.db.WithContext(ctx).Model(&model.Client{})
	var err error
	query, err = clientFilterConfig.ApplyFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	query, err = publicClientSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	pagination := queryutil.GetPaginationParams(
		req.Msg.Pagination.GetLimit(),
		req.Msg.Pagination.GetOffset(),
		20,
	)
	query = queryutil.ApplyPagination(query, pagination)

	if err := query.Find(&clients).Error; err != nil {
		return nil, errs.Internal(err)
	}

	summaries := make([]*openv1.ClientSummary, 0, len(clients))
	for i := range clients {
		summaries = append(summaries, s.toProtoClientSummary(ctx, &clients[i]))
	}

	return connect.NewResponse(&openv1.ListClientsResponse{
		Clients: summaries,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   pagination.Limit,
			Offset:  pagination.Offset,
			HasMore: pagination.Offset+int32(len(clients)) < int32(total),
		},
	}), nil
}

func (s *ClientService) toProtoClientSummary(ctx context.Context, client *model.Client) *openv1.ClientSummary {
	summary := &openv1.ClientSummary{
		Id:        client.ID,
		Name:      client.Name,
		CreatedAt: timestamppb.New(client.CreatedAt),
	}
	if client.Website != nil {
		summary.Website = client.Website
	}

	summary.LogoLightAsset = s.getOptionalClientLogoAsset(ctx, client.LogoLightFileID)
	summary.LogoDarkAsset = s.getOptionalClientLogoAsset(ctx, client.LogoDarkFileID)

	return summary
}

func (s *ClientService) getOptionalClientLogoAsset(ctx context.Context, fileID *string) *commonv1.AssetRef {
	if fileID == nil {
		return nil
	}
	return s.assets.ReadyRef(ctx, s.db, referencecatalog.AssetSource{FileID: *fileID, Kind: "logo"})
}
