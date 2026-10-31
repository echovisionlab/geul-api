package public

import (
	"context"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// ConnectHandler serves the public Map Theme RPCs.
type ConnectHandler struct {
	openv1connect.UnimplementedMapThemeServiceHandler
	themes *Service
}

// NewConnectHandler returns the Connect adapter of themes.
func NewConnectHandler(themes *Service) *ConnectHandler {
	return &ConnectHandler{themes: themes}
}

func (h *ConnectHandler) ResolveByIds(ctx context.Context, req *connect.Request[openv1.ResolveMapThemesByIdsRequest]) (*connect.Response[openv1.ResolveMapThemesByIdsResponse], error) {
	results, err := h.themes.ResolveByIDs(ctx, ResolveByIDsQuery{ThemeIDs: req.Msg.RequestedThemeIds})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&openv1.ResolveMapThemesByIdsResponse{Results: results}), nil
}

func (h *ConnectHandler) Resolve(ctx context.Context, req *connect.Request[openv1.ResolveMapThemeRequest]) (*connect.Response[openv1.ResolvedMapTheme], error) {
	resolved, err := h.themes.Resolve(ctx, ResolveQuery{ThemeID: req.Msg.ThemeId, Scheme: req.Msg.Scheme})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(resolved), nil
}
