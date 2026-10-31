package referencecatalog

import (
	"context"
	"errors"
	"log/slog"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// clientSortConfig defines allowed sort fields for clients
var clientSortConfig = queryutil.SortConfig{
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

// ListClientsAdminQuery pages Clients with their Work counts.
type ListClientsAdminQuery = ListClientsQuery

// SearchClientsQuery finds Clients whose name contains Query.
type SearchClientsQuery struct {
	Query string
	// Limit is 10 when outside 1..50.
	Limit int32
}

// GetClient retrieves a client by ID
func (s *Service) GetClient(ctx context.Context, query GetClientQuery) (_ *managev1.Client, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("client_id", query.ID))

	var client model.Client
	if err := s.db.WithContext(ctx).First(&client, "id = ?", query.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("client", query.ID)
		}
		return nil, err
	}
	return s.toProtoClient(&client, s.getClientLogoAssets(ctx, client.ID)), nil
}

// ListClients returns a paginated list of clients
func (s *Service) ListClients(ctx context.Context, query ListClientsQuery) (Page[*managev1.Client], error) {
	clients, pagination, err := s.listClients(ctx, query)
	if err != nil {
		return Page[*managev1.Client]{}, err
	}
	items := make([]*managev1.Client, len(clients))
	for i := range clients {
		items[i] = s.toProtoClient(&clients[i], s.getClientLogoAssets(ctx, clients[i].ID))
	}
	return Page[*managev1.Client]{Items: items, Pagination: pagination}, nil
}

// SearchClients searches clients by name for quick lookup
func (s *Service) SearchClients(ctx context.Context, query SearchClientsQuery) ([]*managev1.Client, error) {
	limit := int(query.Limit)
	if limit <= 0 || limit > 50 {
		limit = 10
	}

	var clients []model.Client
	scoped := s.db.WithContext(ctx).Model(&model.Client{})
	if query.Query != "" {
		scoped = scoped.Where("name ILIKE ?", "%"+query.Query+"%")
	}
	if err := scoped.Order("name ASC").Limit(limit).Find(&clients).Error; err != nil {
		return nil, err
	}

	items := make([]*managev1.Client, len(clients))
	for i := range clients {
		items[i] = s.toProtoClient(&clients[i], s.getClientLogoAssets(ctx, clients[i].ID))
	}
	return items, nil
}

// ListClientsAdmin returns a paginated list of all clients with stats
func (s *Service) ListClientsAdmin(ctx context.Context, query ListClientsAdminQuery) (Page[*managev1.ClientWithStats], error) {
	can, err := policyv1.Client.List()
	if err != nil {
		return Page[*managev1.ClientWithStats]{}, err
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return Page[*managev1.ClientWithStats]{}, err
	}
	clients, pagination, err := s.listClients(ctx, query)
	if err != nil {
		return Page[*managev1.ClientWithStats]{}, err
	}

	workCounts := map[string]int32{}
	if len(clients) > 0 {
		clientIDs := make([]string, len(clients))
		for i := range clients {
			clientIDs[i] = clients[i].ID
		}
		if workCounts, err = s.clientWorks.CountByClient(ctx, s.db, clientIDs); err != nil {
			return Page[*managev1.ClientWithStats]{}, err
		}
	}

	items := make([]*managev1.ClientWithStats, len(clients))
	for i := range clients {
		items[i] = &managev1.ClientWithStats{
			Client:    s.toProtoClient(&clients[i], s.getClientLogoAssets(ctx, clients[i].ID)),
			WorkCount: workCounts[clients[i].ID],
		}
	}
	return Page[*managev1.ClientWithStats]{Items: items, Pagination: pagination}, nil
}

func (s *Service) listClients(ctx context.Context, query ListClientsQuery) ([]model.Client, *commonv1.PaginationResponse, error) {
	scoped, err := clientFilterConfig.ApplyFilters(s.db.WithContext(ctx).Model(&model.Client{}), query.Filters)
	if err != nil {
		return nil, nil, err
	}
	var total int64
	if err := scoped.Count(&total).Error; err != nil {
		return nil, nil, err
	}

	limit, offset := int32(20), query.Offset
	if query.Limit > 0 {
		limit = query.Limit
	}
	scoped, err = clientSortConfig.ApplySort(scoped, query.Sorts)
	if err != nil {
		return nil, nil, err
	}

	var clients []model.Client
	if err := scoped.Limit(int(limit)).Offset(int(offset)).Find(&clients).Error; err != nil {
		return nil, nil, err
	}
	return clients, &commonv1.PaginationResponse{
		Total:   int32(total),
		Limit:   limit,
		Offset:  offset,
		HasMore: offset+limit < int32(total),
	}, nil
}
