package public

import (
	"context"

	"connectrpc.com/connect"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// ConnectHandler serves the public Program Event RPCs.
type ConnectHandler struct {
	openv1connect.UnimplementedProgramEventServiceHandler
	events *Service
}

// NewConnectHandler returns the public Program Event Connect adapter.
func NewConnectHandler(events *Service) *ConnectHandler {
	return &ConnectHandler{events: events}
}

// SeriesConnectHandler serves the public Program Event series RPCs.
type SeriesConnectHandler struct {
	openv1connect.UnimplementedProgramEventSeriesServiceHandler
	events *Service
}

// NewSeriesConnectHandler returns the public series Connect adapter.
func NewSeriesConnectHandler(events *Service) *SeriesConnectHandler {
	return &SeriesConnectHandler{events: events}
}

// TypeConnectHandler serves the public Program Event type RPCs.
type TypeConnectHandler struct {
	openv1connect.UnimplementedProgramEventTypeServiceHandler
	events *Service
}

// NewTypeConnectHandler returns the public type Connect adapter.
func NewTypeConnectHandler(events *Service) *TypeConnectHandler {
	return &TypeConnectHandler{events: events}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func (h *ConnectHandler) Get(ctx context.Context, req *connect.Request[openv1.GetProgramEventRequest]) (*connect.Response[openv1.GetProgramEventResponse], error) {
	event, err := h.events.GetProgramEvent(ctx, GetProgramEventQuery{
		SlugOrID: req.Msg.Slug, AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	return respond(&openv1.GetProgramEventResponse{Event: event.Event, BlockMedia: event.BlockMedia}, err)
}

func (h *ConnectHandler) List(ctx context.Context, req *connect.Request[openv1.ListProgramEventsRequest]) (*connect.Response[openv1.ListProgramEventsResponse], error) {
	list, err := h.events.ListProgramEvents(ctx, ListProgramEventsQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts, AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	return respond(&openv1.ListProgramEventsResponse{Events: list.Events, Pagination: list.Pagination}, err)
}

func (h *SeriesConnectHandler) Get(ctx context.Context, req *connect.Request[openv1.GetProgramEventSeriesRequest]) (*connect.Response[openv1.GetProgramEventSeriesResponse], error) {
	series, err := h.events.GetProgramEventSeries(ctx, GetProgramEventSeriesQuery{SlugOrID: req.Msg.Slug})
	return respond(&openv1.GetProgramEventSeriesResponse{Series: series}, err)
}

func (h *SeriesConnectHandler) List(ctx context.Context, req *connect.Request[openv1.ListProgramEventSeriesRequest]) (*connect.Response[openv1.ListProgramEventSeriesResponse], error) {
	list, err := h.events.ListProgramEventSeries(ctx, ListProgramEventSeriesQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(), Filters: req.Msg.Filters,
	})
	return respond(&openv1.ListProgramEventSeriesResponse{Series: list.Series, Pagination: list.Pagination}, err)
}

func (h *TypeConnectHandler) List(ctx context.Context, req *connect.Request[openv1.ListProgramEventTypesRequest]) (*connect.Response[openv1.ListProgramEventTypesResponse], error) {
	list, err := h.events.ListProgramEventTypes(ctx, ListProgramEventTypesQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	return respond(&openv1.ListProgramEventTypesResponse{Types: list.Types, Pagination: list.Pagination}, err)
}
