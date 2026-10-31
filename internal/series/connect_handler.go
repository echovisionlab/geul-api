package series

import (
	"context"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

// ConnectHandler serves the Series management RPCs.
type ConnectHandler struct {
	managev1connect.UnimplementedSeriesServiceHandler
	series *Service
}

// NewConnectHandler returns the Connect adapter of series.
func NewConnectHandler(series *Service) *ConnectHandler {
	return &ConnectHandler{series: series}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func deleted(err error) (*connect.Response[managev1.DeleteResponse], error) {
	return respond(&managev1.DeleteResponse{Success: true}, err)
}

func page(request interface {
	GetLimit() int32
	GetOffset() int32
}) Page {
	return Page{Limit: request.GetLimit(), Offset: request.GetOffset()}
}

func (h *ConnectHandler) ListSeriesPosts(ctx context.Context, req *connect.Request[managev1.ListSeriesPostsRequest]) (*connect.Response[managev1.ListSeriesPostsResponse], error) {
	return respond(h.series.ListSeriesPosts(ctx, ListSeriesPostsQuery{SeriesID: req.Msg.SeriesId}))
}

func (h *ConnectHandler) ListMySeries(ctx context.Context, req *connect.Request[managev1.ListMySeriesRequest]) (*connect.Response[managev1.ListMySeriesResponse], error) {
	return respond(h.series.ListMySeries(ctx, ListMySeriesQuery{Page: page(req.Msg.GetPagination())}))
}

func (h *ConnectHandler) ListSeriesAdmin(ctx context.Context, req *connect.Request[managev1.ListSeriesAdminRequest]) (*connect.Response[managev1.ListSeriesAdminResponse], error) {
	return respond(h.series.ListSeriesAdmin(ctx, ListSeriesAdminQuery{Page: page(req.Msg.GetPagination()), Filters: req.Msg.Filters, Sorts: req.Msg.Sorts}))
}

func (h *ConnectHandler) ListSeriesSimple(ctx context.Context, _ *connect.Request[managev1.ListSeriesSimpleRequest]) (*connect.Response[managev1.ListSeriesSimpleResponse], error) {
	return respond(h.series.ListSeriesSimple(ctx, ListSeriesSimpleQuery{}))
}

func (h *ConnectHandler) CreateSeries(ctx context.Context, req *connect.Request[managev1.CreateSeriesRequest]) (*connect.Response[managev1.Series], error) {
	return respond(h.series.CreateSeries(ctx, CreateSeriesCommand{
		Title: req.Msg.Title, Slug: req.Msg.Slug, Description: req.Msg.Description, AcceptLanguage: req.Header().Get("Accept-Language"),
	}))
}

func (h *ConnectHandler) CheckSeriesSlugAvailable(ctx context.Context, req *connect.Request[managev1.CheckSeriesSlugAvailableRequest]) (*connect.Response[managev1.CheckSeriesSlugAvailableResponse], error) {
	return respond(h.series.CheckSeriesSlugAvailable(ctx, CheckSeriesSlugAvailableQuery{Slug: req.Msg.Slug, ExcludeID: req.Msg.ExcludeId}))
}

func (h *ConnectHandler) UpdateSeries(ctx context.Context, req *connect.Request[managev1.UpdateSeriesRequest]) (*connect.Response[managev1.Series], error) {
	return respond(h.series.UpdateSeries(ctx, UpdateSeriesCommand{
		ID: req.Msg.Id, Slug: req.Msg.Slug, Status: req.Msg.Status, Title: req.Msg.Title, Description: req.Msg.Description,
	}))
}

func (h *ConnectHandler) DeleteSeries(ctx context.Context, req *connect.Request[managev1.DeleteSeriesRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.series.DeleteSeries(ctx, DeleteSeriesCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) SetSeriesFeaturedImage(ctx context.Context, req *connect.Request[managev1.SetSeriesFeaturedImageRequest]) (*connect.Response[managev1.SetSeriesFeaturedImageResponse], error) {
	return respond(h.series.SetSeriesFeaturedImage(ctx, SetSeriesFeaturedImageCommand{SeriesID: req.Msg.SeriesId, FileID: req.Msg.FileId}))
}

func (h *ConnectHandler) DeleteSeriesFeaturedImage(ctx context.Context, req *connect.Request[managev1.DeleteSeriesFeaturedImageRequest]) (*connect.Response[managev1.OgAssetDeleteResponse], error) {
	return respond(h.series.DeleteSeriesFeaturedImage(ctx, DeleteSeriesFeaturedImageCommand{SeriesID: req.Msg.SeriesId}))
}

func (h *ConnectHandler) GetSeriesWithManagers(ctx context.Context, req *connect.Request[managev1.GetSeriesWithManagersRequest]) (*connect.Response[managev1.GetSeriesWithManagersResponse], error) {
	return respond(h.series.GetSeriesWithManagers(ctx, GetSeriesWithManagersQuery{ID: req.Msg.Id}))
}

func (h *ConnectHandler) ListSeriesManagers(ctx context.Context, req *connect.Request[managev1.ListSeriesManagersRequest]) (*connect.Response[managev1.ListSeriesManagersResponse], error) {
	return respond(h.series.ListSeriesManagers(ctx, ListSeriesManagersQuery{SeriesID: req.Msg.SeriesId}))
}

func (h *ConnectHandler) AddSeriesManager(ctx context.Context, req *connect.Request[managev1.AddSeriesManagerRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.series.AddSeriesManager(ctx, AddSeriesManagerCommand{SeriesID: req.Msg.SeriesId, MemberID: req.Msg.MemberId}))
}

func (h *ConnectHandler) RemoveSeriesManager(ctx context.Context, req *connect.Request[managev1.RemoveSeriesManagerRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.series.RemoveSeriesManager(ctx, RemoveSeriesManagerCommand{SeriesID: req.Msg.SeriesId, MemberID: req.Msg.MemberId}))
}

func (h *ConnectHandler) AssignPostToSeries(ctx context.Context, req *connect.Request[managev1.AssignPostToSeriesRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.series.AssignPostToSeries(ctx, AssignPostToSeriesCommand{PostID: req.Msg.PostId, SeriesID: req.Msg.SeriesId}))
}

func (h *ConnectHandler) UnassignPostFromSeries(ctx context.Context, req *connect.Request[managev1.UnassignPostFromSeriesRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.series.UnassignPostFromSeries(ctx, UnassignPostFromSeriesCommand{PostID: req.Msg.PostId, SeriesID: req.Msg.SeriesId}))
}

func (h *ConnectHandler) ReorderSeriesPosts(ctx context.Context, req *connect.Request[managev1.ReorderSeriesPostsRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.series.ReorderSeriesPosts(ctx, ReorderSeriesPostsCommand{SeriesID: req.Msg.SeriesId, PostIDs: req.Msg.PostIds}))
}

// InternalConnectHandler serves the Post Series collaboration RPCs
// editor-collab calls.
type InternalConnectHandler struct {
	intrav1connect.UnimplementedInternalPostSeriesServiceHandler
	series *Service
}

// NewInternalConnectHandler returns the internal Connect adapter of series.
func NewInternalConnectHandler(series *Service) *InternalConnectHandler {
	return &InternalConnectHandler{series: series}
}

func (h *InternalConnectHandler) LoadDocument(ctx context.Context, req *connect.Request[intrav1.LoadPostSeriesDocumentRequest]) (*connect.Response[intrav1.LoadPostSeriesDocumentResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.InvalidArgument("request", "request is required")
	}
	return respond(h.series.LoadPostSeriesDocument(ctx, LoadPostSeriesDocumentQuery{SeriesID: req.Msg.SeriesId, Locale: req.Msg.Locale}))
}

func (h *InternalConnectHandler) SaveDocument(ctx context.Context, req *connect.Request[intrav1.SavePostSeriesDocumentRequest]) (*connect.Response[intrav1.SavePostSeriesDocumentResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.InvalidArgument("request", "request is required")
	}
	return respond(h.series.SavePostSeriesDocument(ctx, SavePostSeriesDocumentCommand{
		SeriesID: req.Msg.SeriesId, Locale: req.Msg.Locale,
		ExpectedDocumentRevision: req.Msg.ExpectedDocumentRevision, ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
		Requested: req.Msg.Requested, ContributorMemberIDs: req.Msg.ContributorMemberIds,
	}))
}
