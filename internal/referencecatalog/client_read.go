package referencecatalog

import (
	"context"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/authz"
	errs "github.com/echovisionlab/geul-api/internal/errors"
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

// =============================================================================
// Public Read Methods
// =============================================================================

// GetClient retrieves a client by ID
func (s *ClientService) GetClient(
	ctx context.Context,
	req *connect.Request[managev1.GetClientRequest],
) (*connect.Response[managev1.Client], error) {
	var client model.Client
	if err := s.db.WithContext(ctx).First(&client, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("client", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}

	logoAssets := s.getClientLogoAssets(ctx, client.ID)
	return connect.NewResponse(s.toProtoClient(&client, logoAssets)), nil
}

// ListClients returns a paginated list of clients
func (s *ClientService) ListClients(
	ctx context.Context,
	req *connect.Request[managev1.ListClientsRequest],
) (*connect.Response[managev1.ListClientsResponse], error) {
	var clients []model.Client
	var total int64

	query := s.db.WithContext(ctx).Model(&model.Client{})
	var err error
	query, err = clientFilterConfig.ApplyFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}

	// Count total
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// Apply pagination
	limit := int32(20)
	offset := int32(0)
	if req.Msg.Pagination != nil {
		if req.Msg.Pagination.Limit > 0 {
			limit = req.Msg.Pagination.Limit
		}
		offset = req.Msg.Pagination.Offset
	}

	// Apply sorting
	query, err = clientSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	if err := query.Limit(int(limit)).Offset(int(offset)).Find(&clients).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// Convert to proto
	protoClients := make([]*managev1.Client, len(clients))
	for i, client := range clients {
		logoAssets := s.getClientLogoAssets(ctx, client.ID)
		protoClients[i] = s.toProtoClient(&client, logoAssets)
	}

	return connect.NewResponse(&managev1.ListClientsResponse{
		Clients: protoClients,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}), nil
}

// SearchClients searches clients by name for quick lookup
func (s *ClientService) SearchClients(
	ctx context.Context,
	req *connect.Request[managev1.SearchClientsRequest],
) (*connect.Response[managev1.SearchClientsResponse], error) {
	limit := int(req.Msg.Limit)
	if limit <= 0 || limit > 50 {
		limit = 10
	}

	var clients []model.Client
	query := s.db.WithContext(ctx).Model(&model.Client{})

	if req.Msg.Query != "" {
		pattern := "%" + req.Msg.Query + "%"
		query = query.Where("name ILIKE ?", pattern)
	}

	if err := query.Order("name ASC").Limit(limit).Find(&clients).Error; err != nil {
		return nil, errs.Internal(err)
	}

	protoClients := make([]*managev1.Client, len(clients))
	for i, client := range clients {
		logoAssets := s.getClientLogoAssets(ctx, client.ID)
		protoClients[i] = s.toProtoClient(&client, logoAssets)
	}

	return connect.NewResponse(&managev1.SearchClientsResponse{
		Clients: protoClients,
	}), nil
}

// =============================================================================
// Admin Methods
// =============================================================================

// ListClientsAdmin returns a paginated list of all clients with stats
func (s *ClientService) ListClientsAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListClientsAdminRequest],
) (*connect.Response[managev1.ListClientsAdminResponse], error) {
	can, err := policyv1.Client.List()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return nil, err
	}

	var clients []model.Client
	var total int64

	query := s.db.WithContext(ctx).Model(&model.Client{})
	query, err = clientFilterConfig.ApplyFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}

	// Count total
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// Apply pagination
	limit := int32(20)
	offset := int32(0)
	if req.Msg.Pagination != nil {
		if req.Msg.Pagination.Limit > 0 {
			limit = req.Msg.Pagination.Limit
		}
		offset = req.Msg.Pagination.Offset
	}

	// Apply sorting
	query, err = clientSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	if err := query.Limit(int(limit)).Offset(int(offset)).Find(&clients).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// Get work counts for each client
	clientIDs := make([]string, len(clients))
	for i, c := range clients {
		clientIDs[i] = c.ID
	}

	workCounts := make(map[string]int32)
	if len(clientIDs) > 0 {
		var workStats []struct {
			ClientID string
			Count    int32
		}
		if err := s.db.WithContext(ctx).
			Table("work_client").
			Select("client_id, COUNT(*) as count").
			Where("client_id IN ?", clientIDs).
			Group("client_id").
			Scan(&workStats).Error; err != nil {
			return nil, errs.Internal(err)
		}
		for _, stat := range workStats {
			workCounts[stat.ClientID] = stat.Count
		}
	}

	// Convert to proto with stats
	protoClients := make([]*managev1.ClientWithStats, len(clients))
	for i, client := range clients {
		logoAssets := s.getClientLogoAssets(ctx, client.ID)
		protoClients[i] = &managev1.ClientWithStats{
			Client:    s.toProtoClient(&client, logoAssets),
			WorkCount: workCounts[client.ID],
		}
	}

	return connect.NewResponse(&managev1.ListClientsAdminResponse{
		Clients: protoClients,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}), nil
}
