package public

import (
	"context"

	"connectrpc.com/connect"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// ConnectHandler serves the public File RPCs. It translates the request into
// the use-case query and the result into the response; errs.Wrap maps
// use-case errors to Connect codes.
type ConnectHandler struct {
	files *Service
}

// NewConnectHandler returns the public File Connect adapter.
func NewConnectHandler(files *Service) *ConnectHandler {
	return &ConnectHandler{files: files}
}

func (h *ConnectHandler) AuthorizeDownload(ctx context.Context, req *connect.Request[openv1.AuthorizeDownloadRequest]) (*connect.Response[openv1.AuthorizeDownloadResponse], error) {
	query := AuthorizeDownloadQuery{EntityType: MediaEntityType(req.Msg.EntityType), EntityID: req.Msg.EntityId, TrackID: req.Msg.GetTrackId()}
	if selector := req.Msg.GetContentBlock(); selector != nil {
		query.ContentBlock = &ContentBlockFileSelector{BlockID: selector.GetBlockId(), ReferencePath: selector.GetReferencePath()}
	}
	response, err := h.files.AuthorizeDownload(ctx, query)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(downloadResultProto(response)), nil
}
