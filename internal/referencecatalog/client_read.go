package referencecatalog

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

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

// ClientView is a Client with the ready assets of its logos; a logo that is
// unset or cannot be resolved is nil.
type ClientView struct {
	Client    Client
	LightLogo *commonv1.AssetRef
	DarkLogo  *commonv1.AssetRef
}

// ClientWorkCount is a Client with how many Works name it.
type ClientWorkCount struct {
	Client    ClientView
	WorkCount int32
}

// GetClient retrieves a client by ID
func (s *Service) GetClient(ctx context.Context, query GetClientQuery) (_ ClientView, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("client_id", query.ID))

	var view ClientView
	err := s.units.Read(ctx, func(store Store) error {
		client, err := store.Client(ctx, query.ID)
		if errors.Is(err, ErrClientNotFound) {
			return domainerr.ResourceNotFound("client", query.ID)
		}
		if err != nil {
			return err
		}
		view = clientView(ctx, store, client)
		return nil
	})
	return view, err
}

// ListClients returns a paginated list of clients
func (s *Service) ListClients(ctx context.Context, query ListClientsQuery) (Page[ClientView], error) {
	var listed Page[ClientView]
	err := s.units.Read(ctx, func(store Store) error {
		clients, pagination, err := listClients(ctx, store, query)
		if err != nil {
			return err
		}
		listed = Page[ClientView]{Items: clientViews(ctx, store, clients), Pagination: pagination}
		return nil
	})
	return listed, err
}

// SearchClients searches clients by name for quick lookup
func (s *Service) SearchClients(ctx context.Context, query SearchClientsQuery) ([]ClientView, error) {
	limit := int(query.Limit)
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	var found []ClientView
	err := s.units.Read(ctx, func(store Store) error {
		clients, err := store.SearchClients(ctx, query.Query, limit)
		if err != nil {
			return err
		}
		found = clientViews(ctx, store, clients)
		return nil
	})
	return found, err
}

// ListClientsAdmin returns a paginated list of all clients with stats
func (s *Service) ListClientsAdmin(ctx context.Context, query ListClientsAdminQuery) (Page[ClientWorkCount], error) {
	can, err := policyv1.Client.List()
	if err != nil {
		return Page[ClientWorkCount]{}, err
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return Page[ClientWorkCount]{}, err
	}
	var listed Page[ClientWorkCount]
	err = s.units.Read(ctx, func(store Store) error {
		clients, pagination, err := listClients(ctx, store, query)
		if err != nil {
			return err
		}
		workCounts := map[string]int32{}
		if len(clients) > 0 {
			clientIDs := make([]string, len(clients))
			for i := range clients {
				clientIDs[i] = clients[i].ID
			}
			if workCounts, err = store.CountClientWorks(ctx, clientIDs); err != nil {
				return err
			}
		}
		items := make([]ClientWorkCount, len(clients))
		for i := range clients {
			items[i] = ClientWorkCount{Client: clientView(ctx, store, clients[i]), WorkCount: workCounts[clients[i].ID]}
		}
		listed = Page[ClientWorkCount]{Items: items, Pagination: pagination}
		return nil
	})
	return listed, err
}

func listClients(ctx context.Context, store Store, query ListClientsQuery) ([]Client, Pagination, error) {
	page := ListPage{Filters: query.Filters, Sorts: query.Sorts, Limit: 20, Offset: query.Offset}
	if query.Limit > 0 {
		page.Limit = query.Limit
	}
	clients, total, err := store.ListClients(ctx, page)
	if err != nil {
		return nil, Pagination{}, err
	}
	return clients, paginationOf(page.Limit, page.Offset, total), nil
}

func clientViews(ctx context.Context, store Store, clients []Client) []ClientView {
	views := make([]ClientView, len(clients))
	for i := range clients {
		views[i] = clientView(ctx, store, clients[i])
	}
	return views
}

// clientView resolves the logos the client row selects. A logo that cannot
// be resolved is logged and left out rather than failing the read.
func clientView(ctx context.Context, store Store, client Client) ClientView {
	return ClientView{
		Client:    client,
		LightLogo: clientLogo(ctx, store, client.ID, client.LogoLightFileID),
		DarkLogo:  clientLogo(ctx, store, client.ID, client.LogoDarkFileID),
	}
}

func clientLogo(ctx context.Context, store Store, clientID string, fileID *string) *commonv1.AssetRef {
	if fileID == nil || strings.TrimSpace(*fileID) == "" {
		return nil
	}
	asset, err := store.ReadyAsset(ctx, AssetSource{FileID: *fileID, Kind: "logo"})
	if err != nil {
		slog.ErrorContext(ctx, "Client logo asset lookup failed; serving the client without this logo", "domain", "referencecatalog", "operation", "resolve_client_logo_asset", "outcome", "failed", failurecause.Attr(err), "client_id", clientID, "file_id", *fileID)
		return nil
	}
	return asset
}

// clientLogos reads the logos a Client selects after a mutation committed,
// in its own snapshot. A Client that cannot be read is served without logos.
func (s *Service) clientLogos(ctx context.Context, clientID string) (light, dark *commonv1.AssetRef) {
	err := s.units.Read(ctx, func(store Store) error {
		lightFileID, darkFileID, err := store.ClientLogoFiles(ctx, clientID)
		if err != nil {
			return err
		}
		light = clientLogo(ctx, store, clientID, lightFileID)
		dark = clientLogo(ctx, store, clientID, darkFileID)
		return nil
	})
	if err != nil {
		slog.ErrorContext(ctx, "Client logo lookup failed; serving the client without logos", "domain", "referencecatalog", "operation", "load_client_logo_assets", "outcome", "failed", failurecause.Attr(err), "client_id", clientID)
		return nil, nil
	}
	return light, dark
}
