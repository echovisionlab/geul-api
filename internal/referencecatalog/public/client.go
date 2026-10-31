package public

import (
	"context"
	"errors"
	"log/slog"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
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

// GetClientQuery names a Client.
type GetClientQuery struct {
	ID string
}

// ListClientsQuery pages Clients.
type ListClientsQuery struct {
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
	// Limit is 20 when not positive.
	Limit  int32
	Offset int32
}

// GetClient returns the public summary of a Client.
func (s *Service) GetClient(ctx context.Context, query GetClientQuery) (_ *openv1.ClientSummary, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("client_id", query.ID))

	var client model.Client
	if err := s.db.WithContext(ctx).First(&client, "id = ?", query.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("client", query.ID)
		}
		return nil, err
	}
	return s.toProtoClientSummary(ctx, &client), nil
}

// ListClients pages the public Client summaries.
func (s *Service) ListClients(ctx context.Context, query ListClientsQuery) (referencecatalog.Page[*openv1.ClientSummary], error) {
	var clients []model.Client
	var total int64

	scoped, err := clientFilterConfig.ApplyFilters(s.db.WithContext(ctx).Model(&model.Client{}), query.Filters)
	if err != nil {
		return referencecatalog.Page[*openv1.ClientSummary]{}, err
	}
	if err := scoped.Count(&total).Error; err != nil {
		return referencecatalog.Page[*openv1.ClientSummary]{}, err
	}
	scoped, err = publicClientSortConfig.ApplySort(scoped, query.Sorts)
	if err != nil {
		return referencecatalog.Page[*openv1.ClientSummary]{}, err
	}
	pagination := queryutil.GetPaginationParams(query.Limit, query.Offset, 20)
	if err := queryutil.ApplyPagination(scoped, pagination).Find(&clients).Error; err != nil {
		return referencecatalog.Page[*openv1.ClientSummary]{}, err
	}

	summaries := make([]*openv1.ClientSummary, 0, len(clients))
	for i := range clients {
		summaries = append(summaries, s.toProtoClientSummary(ctx, &clients[i]))
	}
	return referencecatalog.Page[*openv1.ClientSummary]{
		Items: summaries,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   pagination.Limit,
			Offset:  pagination.Offset,
			HasMore: pagination.Offset+int32(len(clients)) < int32(total),
		},
	}, nil
}

func (s *Service) toProtoClientSummary(ctx context.Context, client *model.Client) *openv1.ClientSummary {
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

func (s *Service) getOptionalClientLogoAsset(ctx context.Context, fileID *string) *commonv1.AssetRef {
	if fileID == nil {
		return nil
	}
	return s.assets.ReadyRef(ctx, s.db, referencecatalog.AssetSource{FileID: *fileID, Kind: "logo"})
}
