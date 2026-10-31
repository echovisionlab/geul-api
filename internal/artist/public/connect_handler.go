package public

import (
	"context"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// ConnectHandler serves the public Artist RPCs by translating each request
// into a query of the public Artist reads.
type ConnectHandler struct {
	openv1connect.UnimplementedArtistServiceHandler
	artists *Service
}

// NewConnectHandler returns the public Connect adapter of artists.
func NewConnectHandler(artists *Service) *ConnectHandler {
	return &ConnectHandler{artists: artists}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func (h *ConnectHandler) List(ctx context.Context, req *connect.Request[openv1.ListArtistsRequest]) (*connect.Response[openv1.ListArtistsResponse], error) {
	page, err := h.artists.List(ctx, ListArtistsQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(), Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	return respond(&openv1.ListArtistsResponse{Artists: page.Artists, Pagination: page.Pagination}, err)
}

func (h *ConnectHandler) Get(ctx context.Context, req *connect.Request[openv1.GetArtistRequest]) (*connect.Response[openv1.GetArtistResponse], error) {
	artist, err := h.artists.Get(ctx, GetArtistQuery{
		SlugOrID: req.Msg.Slug, ShareToken: req.Msg.GetShareToken(), SharePassword: req.Msg.GetSharePassword(),
		AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	return respond(&openv1.GetArtistResponse{Artist: artist}, err)
}

func (h *ConnectHandler) GetReleases(ctx context.Context, req *connect.Request[openv1.GetArtistReleasesRequest]) (*connect.Response[openv1.GetArtistReleasesResponse], error) {
	return respond(h.artists.GetReleases(ctx, ArtistWorksQuery{
		ArtistID: req.Msg.ArtistId, Limit: req.Msg.Limit, Offset: req.Msg.Offset, Sorts: req.Msg.Sorts,
	}))
}

func (h *ConnectHandler) GetWorks(ctx context.Context, req *connect.Request[openv1.GetArtistWorksRequest]) (*connect.Response[openv1.GetArtistWorksResponse], error) {
	return respond(h.artists.GetWorks(ctx, ArtistWorksQuery{
		ArtistID: req.Msg.ArtistId, Limit: req.Msg.Limit, Offset: req.Msg.Offset, Sorts: req.Msg.Sorts,
		AcceptLanguage: req.Header().Get("Accept-Language"),
	}))
}
