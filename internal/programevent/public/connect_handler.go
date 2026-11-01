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

func (h *ConnectHandler) Get(ctx context.Context, req *connect.Request[openv1.GetProgramEventRequest]) (*connect.Response[openv1.GetProgramEventResponse], error) {
	event, err := h.events.GetProgramEvent(ctx, GetProgramEventQuery{
		SlugOrID: req.Msg.Slug, AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&openv1.GetProgramEventResponse{Event: eventMessage(event.Event), BlockMedia: event.BlockMedia}), nil
}

func (h *ConnectHandler) List(ctx context.Context, req *connect.Request[openv1.ListProgramEventsRequest]) (*connect.Response[openv1.ListProgramEventsResponse], error) {
	list, err := h.events.ListProgramEvents(ctx, ListProgramEventsQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts, AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	events := make([]*openv1.ProgramEventSummary, 0, len(list.Events))
	for _, summary := range list.Events {
		events = append(events, summaryMessage(summary))
	}
	return connect.NewResponse(&openv1.ListProgramEventsResponse{Events: events, Pagination: paginationMessage(list.Pagination)}), nil
}

func (h *SeriesConnectHandler) Get(ctx context.Context, req *connect.Request[openv1.GetProgramEventSeriesRequest]) (*connect.Response[openv1.GetProgramEventSeriesResponse], error) {
	series, err := h.events.GetProgramEventSeries(ctx, GetProgramEventSeriesQuery{SlugOrID: req.Msg.Slug})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&openv1.GetProgramEventSeriesResponse{Series: seriesMessage(&series)}), nil
}

func (h *SeriesConnectHandler) List(ctx context.Context, req *connect.Request[openv1.ListProgramEventSeriesRequest]) (*connect.Response[openv1.ListProgramEventSeriesResponse], error) {
	list, err := h.events.ListProgramEventSeries(ctx, ListProgramEventSeriesQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(), Filters: req.Msg.Filters,
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	series := make([]*openv1.ProgramEventSeries, 0, len(list.Series))
	for i := range list.Series {
		series = append(series, seriesMessage(&list.Series[i]))
	}
	return connect.NewResponse(&openv1.ListProgramEventSeriesResponse{Series: series, Pagination: paginationMessage(list.Pagination)}), nil
}

func (h *TypeConnectHandler) List(ctx context.Context, req *connect.Request[openv1.ListProgramEventTypesRequest]) (*connect.Response[openv1.ListProgramEventTypesResponse], error) {
	list, err := h.events.ListProgramEventTypes(ctx, ListProgramEventTypesQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	types := make([]*openv1.ProgramEventType, 0, len(list.Types))
	for i := range list.Types {
		types = append(types, typeMessage(&list.Types[i]))
	}
	return connect.NewResponse(&openv1.ListProgramEventTypesResponse{Types: types, Pagination: paginationMessage(list.Pagination)}), nil
}
