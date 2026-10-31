package public

import (
	"context"

	"connectrpc.com/connect"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// ConnectHandler serves the public manifest RPC.
type ConnectHandler struct {
	openv1connect.UnimplementedManifestServiceHandler
	manifest *Service
}

// NewConnectHandler returns the Connect adapter of manifest.
func NewConnectHandler(manifest *Service) *ConnectHandler {
	return &ConnectHandler{manifest: manifest}
}

func (h *ConnectHandler) Get(ctx context.Context, req *connect.Request[openv1.GetRequest]) (*connect.Response[openv1.GetResponse], error) {
	response, err := h.manifest.GetManifest(ctx, GetManifestQuery{AcceptLanguage: req.Header().Get("Accept-Language")})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(response), nil
}
