package admin

import (
	"context"

	"connectrpc.com/connect"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// OG is the OG-owned portion of the manage Admin RPC surface. Embedding this
// consumer-owned port lets the Admin endpoint aggregate independently owned
// operations without importing OG implementation details.
type OG interface {
	RegenerateOgImage(context.Context, *connect.Request[managev1.RegenerateOgImageRequest]) (*connect.Response[managev1.RegenerateOgImageResponse], error)
	RegenerateAllOgImages(context.Context, *connect.Request[managev1.RegenerateAllOgImagesRequest]) (*connect.Response[managev1.RegenerateAllOgImagesResponse], error)
	GetOgGeneration(context.Context, *connect.Request[managev1.GetOgGenerationRequest]) (*connect.Response[managev1.GetOgGenerationResponse], error)
	GetLatestOgGeneration(context.Context, *connect.Request[managev1.GetLatestOgGenerationRequest]) (*connect.Response[managev1.GetLatestOgGenerationResponse], error)
	GetOgGenerationRun(context.Context, *connect.Request[managev1.GetOgGenerationRunRequest]) (*connect.Response[managev1.GetOgGenerationRunResponse], error)
}

// ConnectHandler serves the manage Admin RPCs: the dashboard through its use
// case, and the OG operations through the OG handler that owns them.
type ConnectHandler struct {
	OG
	admin *Service
}

// NewConnectHandler returns the Connect adapter of admin and og.
func NewConnectHandler(admin *Service, og OG) *ConnectHandler {
	return &ConnectHandler{OG: og, admin: admin}
}

func (h *ConnectHandler) GetDashboardStats(ctx context.Context, _ *connect.Request[managev1.GetDashboardStatsRequest]) (*connect.Response[managev1.GetDashboardStatsResponse], error) {
	stats, err := h.admin.DashboardStats(ctx)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.GetDashboardStatsResponse{Stats: stats}), nil
}
