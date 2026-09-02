package admin

import (
	"context"

	"connectrpc.com/connect"

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
