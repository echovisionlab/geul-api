package menu

import (
	"context"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

// ConnectHandler serves the Menu management RPCs.
type ConnectHandler struct {
	managev1connect.UnimplementedMenuServiceHandler
	menus *Service
}

// NewConnectHandler returns the Connect adapter of menus.
func NewConnectHandler(menus *Service) *ConnectHandler {
	return &ConnectHandler{menus: menus}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func (h *ConnectHandler) GetMenuById(ctx context.Context, req *connect.Request[managev1.GetMenuByIdRequest]) (*connect.Response[managev1.Menu], error) {
	return respond(h.menus.GetMenu(ctx, GetMenuQuery{ID: req.Msg.Id}))
}

func (h *ConnectHandler) ListMenus(ctx context.Context, req *connect.Request[managev1.ListMenusRequest]) (*connect.Response[managev1.ListMenusResponse], error) {
	return respond(h.menus.ListMenus(ctx, ListMenusQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
	}))
}

func (h *ConnectHandler) CreateMenu(ctx context.Context, req *connect.Request[managev1.CreateMenuRequest]) (*connect.Response[managev1.Menu], error) {
	return respond(h.menus.CreateMenu(ctx, CreateMenuCommand{Name: req.Msg.Name, Items: req.Msg.Items}))
}

func (h *ConnectHandler) UpdateMenu(ctx context.Context, req *connect.Request[managev1.UpdateMenuRequest]) (*connect.Response[managev1.Menu], error) {
	command := UpdateMenuCommand{ID: req.Msg.Id, Name: req.Msg.Name}
	if req.Msg.Items != nil {
		command.Items = &req.Msg.Items.Items
	}
	return respond(h.menus.UpdateMenu(ctx, command))
}

func (h *ConnectHandler) DeleteMenu(ctx context.Context, req *connect.Request[managev1.DeleteMenuRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	err := h.menus.DeleteMenu(ctx, DeleteMenuCommand{ID: req.Msg.Id})
	return respond(&managev1.DeleteResponse{Success: true}, err)
}

// InternalConnectHandler serves the Menu collaboration RPCs editor-collab
// calls.
type InternalConnectHandler struct {
	intrav1connect.UnimplementedInternalMenuServiceHandler
	menus *Service
}

// NewInternalConnectHandler returns the internal Connect adapter of menus.
func NewInternalConnectHandler(menus *Service) *InternalConnectHandler {
	return &InternalConnectHandler{menus: menus}
}

func (h *InternalConnectHandler) LoadDocument(ctx context.Context, req *connect.Request[intrav1.LoadMenuDocumentRequest]) (*connect.Response[intrav1.LoadMenuDocumentResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.InvalidArgument("request", "request is required")
	}
	return respond(h.menus.LoadMenuDocument(ctx, LoadMenuDocumentQuery{MenuID: req.Msg.MenuId, Locale: req.Msg.Locale}))
}

func (h *InternalConnectHandler) SaveDocument(ctx context.Context, req *connect.Request[intrav1.SaveMenuDocumentRequest]) (*connect.Response[intrav1.SaveMenuDocumentResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.InvalidArgument("request", "request is required")
	}
	return respond(h.menus.SaveMenuDocument(ctx, SaveMenuDocumentCommand{
		MenuID: req.Msg.MenuId, Locale: req.Msg.Locale,
		ExpectedDocumentRevision: req.Msg.ExpectedDocumentRevision, ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
		Name: req.Msg.Name, Items: req.Msg.Items, RequestedLabels: req.Msg.RequestedLabels,
		ContributorMemberIDs: req.Msg.ContributorMemberIds,
	}))
}
