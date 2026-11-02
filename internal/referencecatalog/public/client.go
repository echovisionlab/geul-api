package public

import (
	"context"
	"errors"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
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

// GetClient returns the public summary of a Client.
func (s *Service) GetClient(ctx context.Context, query GetClientQuery) (_ referencecatalog.ClientView, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("client_id", query.ID))

	var view referencecatalog.ClientView
	err := s.units.Read(ctx, func(store Store) error {
		client, err := store.Client(ctx, query.ID)
		if errors.Is(err, referencecatalog.ErrClientNotFound) {
			return domainerr.ResourceNotFound("client", query.ID)
		}
		if err != nil {
			return err
		}
		view = clientSummary(ctx, store, client)
		return nil
	})
	return view, err
}

// ListClients pages the public Client summaries.
func (s *Service) ListClients(ctx context.Context, query ListClientsQuery) (referencecatalog.Page[referencecatalog.ClientView], error) {
	page := listPage(query.Filters, query.Sorts, query.Limit, query.Offset, 20)
	var listed referencecatalog.Page[referencecatalog.ClientView]
	err := s.units.Read(ctx, func(store Store) error {
		clients, total, err := store.Clients(ctx, page)
		if err != nil {
			return err
		}
		summaries := make([]referencecatalog.ClientView, 0, len(clients))
		for i := range clients {
			summaries = append(summaries, clientSummary(ctx, store, clients[i]))
		}
		listed = referencecatalog.Page[referencecatalog.ClientView]{Items: summaries, Pagination: paginationOf(page, len(clients), total)}
		return nil
	})
	return listed, err
}

func clientSummary(ctx context.Context, store Store, client referencecatalog.Client) referencecatalog.ClientView {
	return referencecatalog.ClientView{
		Client:    client,
		LightLogo: optionalClientLogo(ctx, store, client.LogoLightFileID),
		DarkLogo:  optionalClientLogo(ctx, store, client.LogoDarkFileID),
	}
}

func optionalClientLogo(ctx context.Context, store Store, fileID *string) *mediaasset.Reference {
	if fileID == nil {
		return nil
	}
	return store.ReadyAsset(ctx, referencecatalog.AssetSource{FileID: *fileID, Kind: "logo"})
}
