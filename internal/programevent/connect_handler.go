package programevent

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

// ConnectHandler serves the administrator Program Event RPCs. Each method
// translates the request into a use-case command or query, and the result
// into the response; errs.Wrap maps use-case errors to Connect codes.
type ConnectHandler struct {
	managev1connect.UnimplementedProgramEventServiceHandler
	events *Service
}

// NewConnectHandler returns the administrator Program Event Connect adapter.
func NewConnectHandler(events *Service) *ConnectHandler {
	return &ConnectHandler{events: events}
}

// SeriesConnectHandler serves the administrator Program Event series RPCs.
type SeriesConnectHandler struct {
	managev1connect.UnimplementedProgramEventSeriesServiceHandler
	events *Service
}

// NewSeriesConnectHandler returns the administrator series Connect adapter.
func NewSeriesConnectHandler(events *Service) *SeriesConnectHandler {
	return &SeriesConnectHandler{events: events}
}

// TypeConnectHandler serves the administrator Program Event type RPCs.
type TypeConnectHandler struct {
	managev1connect.UnimplementedProgramEventTypeServiceHandler
	events *Service
}

// NewTypeConnectHandler returns the administrator type Connect adapter.
func NewTypeConnectHandler(events *Service) *TypeConnectHandler {
	return &TypeConnectHandler{events: events}
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

func (h *ConnectHandler) GetProgramEvent(ctx context.Context, req *connect.Request[managev1.GetProgramEventRequest]) (*connect.Response[managev1.ProgramEvent], error) {
	return respond(h.events.GetProgramEvent(ctx, GetProgramEventQuery{ID: req.Msg.Id}))
}

func (h *ConnectHandler) ListProgramEventsAdmin(ctx context.Context, req *connect.Request[managev1.ListProgramEventsAdminRequest]) (*connect.Response[managev1.ListProgramEventsAdminResponse], error) {
	list, err := h.events.ListProgramEventsAdmin(ctx, ListProgramEventsQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	return respond(&managev1.ListProgramEventsAdminResponse{Events: list.Events, Pagination: list.Pagination}, err)
}

func (h *ConnectHandler) CreateProgramEvent(ctx context.Context, req *connect.Request[managev1.CreateProgramEventRequest]) (*connect.Response[managev1.ProgramEvent], error) {
	command := createProgramEventCommand(req.Msg)
	command.AcceptLanguage = req.Header().Get("Accept-Language")
	return respond(h.events.CreateProgramEvent(ctx, command))
}

func (h *ConnectHandler) UpdateProgramEvent(ctx context.Context, req *connect.Request[managev1.UpdateProgramEventRequest]) (*connect.Response[managev1.UpdateProgramEventResponse], error) {
	update, err := h.events.UpdateProgramEvent(ctx, updateProgramEventCommand(req.Msg))
	return respond(&managev1.UpdateProgramEventResponse{
		Id: update.ID, Changed: update.Changed, UpdatedAt: timestamppb.New(update.UpdatedAt),
	}, err)
}

func (h *ConnectHandler) AddProgramEventMedia(ctx context.Context, req *connect.Request[managev1.AddProgramEventMediaRequest]) (*connect.Response[managev1.AddProgramEventMediaResponse], error) {
	change, err := h.events.AddProgramEventMedia(ctx, AddProgramEventMediaCommand{
		EventID: req.Msg.EventId, FileID: req.Msg.FileId, Role: req.Msg.Role,
		Alt: req.Msg.Alt, Caption: req.Msg.Caption, MakePrimary: req.Msg.MakePrimary,
	})
	return respond(&managev1.AddProgramEventMediaResponse{
		EventId: req.Msg.EventId, Changed: change.Changed, Media: change.Media, UpdatedAt: timestamppb.New(change.UpdatedAt),
	}, err)
}

func (h *ConnectHandler) DeleteProgramEventMedia(ctx context.Context, req *connect.Request[managev1.DeleteProgramEventMediaRequest]) (*connect.Response[managev1.DeleteProgramEventMediaResponse], error) {
	change, err := h.events.DeleteProgramEventMedia(ctx, DeleteProgramEventMediaCommand{EventID: req.Msg.EventId, MediaID: req.Msg.MediaId})
	return respond(&managev1.DeleteProgramEventMediaResponse{
		EventId: req.Msg.EventId, Changed: change.Changed, MediaId: req.Msg.MediaId, UpdatedAt: timestamppb.New(change.UpdatedAt),
	}, err)
}

func (h *ConnectHandler) ReorderProgramEventMedia(ctx context.Context, req *connect.Request[managev1.ReorderProgramEventMediaRequest]) (*connect.Response[managev1.ReorderProgramEventMediaResponse], error) {
	order, err := h.events.ReorderProgramEventMedia(ctx, ReorderProgramEventMediaCommand{
		EventID: req.Msg.EventId, Role: req.Msg.Role, MediaIDs: req.Msg.MediaIds,
	})
	return respond(&managev1.ReorderProgramEventMediaResponse{
		EventId: req.Msg.EventId, Changed: order.Changed, Role: order.Role, MediaIds: req.Msg.MediaIds,
		UpdatedAt: timestamppb.New(order.UpdatedAt),
	}, err)
}

func (h *ConnectHandler) AddProgramEventCredit(ctx context.Context, req *connect.Request[managev1.AddProgramEventCreditRequest]) (*connect.Response[managev1.ProgramEventCredit], error) {
	return respond(h.events.AddProgramEventCredit(ctx, AddProgramEventCreditCommand{
		EventID: req.Msg.EventId, ArtistID: req.Msg.ArtistId, MemberID: req.Msg.MemberId,
		DisplayName: req.Msg.DisplayName, CreditRole: req.Msg.CreditRole, Description: req.Msg.Description,
		SortOrder: req.Msg.SortOrder,
	}))
}

func (h *ConnectHandler) UpdateProgramEventCredit(ctx context.Context, req *connect.Request[managev1.UpdateProgramEventCreditRequest]) (*connect.Response[managev1.ProgramEventCredit], error) {
	return respond(h.events.UpdateProgramEventCredit(ctx, UpdateProgramEventCreditCommand{
		EventID: req.Msg.EventId, CreditID: req.Msg.CreditId, CreditRole: req.Msg.CreditRole,
		Description: req.Msg.Description, SortOrder: req.Msg.SortOrder,
	}))
}

func (h *ConnectHandler) DeleteProgramEventCredit(ctx context.Context, req *connect.Request[managev1.DeleteProgramEventCreditRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.events.DeleteProgramEventCredit(ctx, DeleteProgramEventCreditCommand{EventID: req.Msg.EventId, CreditID: req.Msg.CreditId}))
}

func (h *ConnectHandler) ReorderProgramEventCredits(ctx context.Context, req *connect.Request[managev1.ReorderProgramEventCreditsRequest]) (*connect.Response[managev1.ReorderProgramEventCreditsResponse], error) {
	order, err := h.events.ReorderProgramEventCredits(ctx, ReorderProgramEventCreditsCommand{EventID: req.Msg.EventId, CreditIDs: req.Msg.CreditIds})
	return respond(&managev1.ReorderProgramEventCreditsResponse{
		EventId: req.Msg.EventId, Changed: order.Changed, CreditIds: req.Msg.CreditIds, UpdatedAt: timestamppb.New(order.UpdatedAt),
	}, err)
}

func (h *ConnectHandler) DeleteProgramEvent(ctx context.Context, req *connect.Request[managev1.DeleteProgramEventRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.events.DeleteProgramEvent(ctx, DeleteProgramEventCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) PublishProgramEvent(ctx context.Context, req *connect.Request[managev1.PublishProgramEventRequest]) (*connect.Response[managev1.ProgramEventLifecycleMutationResponse], error) {
	change, err := h.events.PublishProgramEvent(ctx, PublishProgramEventCommand{ID: req.Msg.Id})
	return respond(lifecycleChangeMessage(change), err)
}

func (h *ConnectHandler) ArchiveProgramEvent(ctx context.Context, req *connect.Request[managev1.ArchiveProgramEventRequest]) (*connect.Response[managev1.ProgramEventLifecycleMutationResponse], error) {
	change, err := h.events.ArchiveProgramEvent(ctx, ArchiveProgramEventCommand{ID: req.Msg.Id})
	return respond(lifecycleChangeMessage(change), err)
}

// lifecycleChangeMessage is the API form of a lifecycle change.
func lifecycleChangeMessage(change LifecycleChange) *managev1.ProgramEventLifecycleMutationResponse {
	response := &managev1.ProgramEventLifecycleMutationResponse{
		Id: change.ID, Changed: change.Changed, Status: programEventStatusToProto(change.Status),
		UpdatedAt: timestamppb.New(change.UpdatedAt),
	}
	if change.PublishedAt != nil {
		response.PublishedAt = timestamppb.New(*change.PublishedAt)
	}
	return response
}

// createProgramEventCommand is the command of a create request;
// the caller adds the request's Accept-Language.
func createProgramEventCommand(request *managev1.CreateProgramEventRequest) CreateProgramEventCommand {
	return CreateProgramEventCommand{
		Title: request.Title, Slug: request.Slug, SourceLocale: request.SourceLocale, TypeID: request.TypeId,
		StartsAt: timestampPtr(request.StartsAt), EndsAt: timestampPtr(request.EndsAt),
		Timezone: request.Timezone, AllDay: request.AllDay, LocationMode: locationModeFromProto(request.LocationMode),
		MapPlaceID: request.MapPlaceId, Summary: request.Summary, SeriesID: request.SeriesId, SeriesOrder: request.SeriesOrder,
		PosterFileID: request.PosterFileId, TicketURL: request.TicketUrl, StreamURL: request.StreamUrl, ExternalURL: request.ExternalUrl,
		Artists: ArtistRelations(request.Artists), Labels: LabelRelations(request.Labels), Clients: ClientRelations(request.Clients),
		Credits: CreditsFromProto(request.Credits),
	}
}

// updateProgramEventCommand is the command of an update request.
func updateProgramEventCommand(request *managev1.UpdateProgramEventRequest) UpdateProgramEventCommand {
	command := UpdateProgramEventCommand{
		ID: request.Id, Slug: request.Slug, TypeID: request.TypeId,
		StartsAt: timestampPtr(request.StartsAt), EndsAt: timestampPtr(request.EndsAt), ClearEndsAt: request.ClearEndsAt,
		Timezone: request.Timezone, AllDay: request.AllDay, MapPlaceID: request.MapPlaceId,
		SeriesID: request.SeriesId, SeriesOrder: request.SeriesOrder, ClearSeriesOrder: request.ClearSeriesOrder,
		PosterFileID: request.PosterFileId, TicketURL: request.TicketUrl, StreamURL: request.StreamUrl, ExternalURL: request.ExternalUrl,
		Artists: RelationsEdit{Replace: request.ReplaceArtists, Desired: ArtistRelations(request.Artists)},
		Labels:  RelationsEdit{Replace: request.ReplaceLabels, Desired: LabelRelations(request.Labels)},
		Clients: RelationsEdit{Replace: request.ReplaceClients, Desired: ClientRelations(request.Clients)},
		Credits: CreditsEdit{Replace: request.ReplaceCredits, Desired: CreditsFromProto(request.Credits)},
	}
	if request.LocationMode != nil {
		mode := locationModeFromProto(*request.LocationMode)
		command.LocationMode = &mode
	}
	if request.ObservedArtists != nil {
		observed := ArtistRelations(request.ObservedArtists.Artists)
		command.Artists.Observed = &observed
	}
	if request.ObservedLabels != nil {
		observed := LabelRelations(request.ObservedLabels.Labels)
		command.Labels.Observed = &observed
	}
	if request.ObservedClients != nil {
		observed := ClientRelations(request.ObservedClients.Clients)
		command.Clients.Observed = &observed
	}
	return command
}

// ArtistRelations are the artist relations of rows.
func ArtistRelations(rows []*managev1.ProgramEventArtist) []Relation {
	return relations(rows, func(row *managev1.ProgramEventArtist) Relation {
		return Relation{ID: row.GetArtistId(), Role: row.Role, SortOrder: row.GetSortOrder()}
	})
}

// LabelRelations are the label relations of rows.
func LabelRelations(rows []*managev1.ProgramEventLabel) []Relation {
	return relations(rows, func(row *managev1.ProgramEventLabel) Relation {
		return Relation{ID: row.GetLabelId(), Role: row.Role, SortOrder: row.GetSortOrder()}
	})
}

// ClientRelations are the client relations of rows.
func ClientRelations(rows []*managev1.ProgramEventClient) []Relation {
	return relations(rows, func(row *managev1.ProgramEventClient) Relation {
		return Relation{ID: row.GetClientId(), Role: row.Role, SortOrder: row.GetSortOrder()}
	})
}

func relations[Row any](rows []*Row, relation func(*Row) Relation) []Relation {
	if rows == nil {
		return nil
	}
	result := make([]Relation, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			result = append(result, Relation{})
			continue
		}
		result = append(result, relation(row))
	}
	return result
}

// CreditsFromProto are the credits of rows; the hydrated Artist and Member
// projections are read-only.
func CreditsFromProto(rows []*managev1.ProgramEventCredit) []Credit {
	if rows == nil {
		return nil
	}
	credits := make([]Credit, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			credits = append(credits, Credit{})
			continue
		}
		credits = append(credits, Credit{
			ID: row.GetId(), ArtistID: row.ArtistId, MemberID: row.MemberId, DisplayName: row.DisplayName,
			CreditRole: row.CreditRole, Description: row.Description, SortOrder: row.GetSortOrder(),
		})
	}
	return credits
}

func (h *SeriesConnectHandler) GetProgramEventSeries(ctx context.Context, req *connect.Request[managev1.GetProgramEventSeriesRequest]) (*connect.Response[managev1.ProgramEventSeries], error) {
	return respond(h.events.GetProgramEventSeries(ctx, GetProgramEventSeriesQuery{IDOrSlug: req.Msg.Id}))
}

func (h *SeriesConnectHandler) ListProgramEventSeriesAdmin(ctx context.Context, req *connect.Request[managev1.ListProgramEventSeriesAdminRequest]) (*connect.Response[managev1.ListProgramEventSeriesAdminResponse], error) {
	list, err := h.events.ListProgramEventSeriesAdmin(ctx, ListProgramEventSeriesQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	return respond(&managev1.ListProgramEventSeriesAdminResponse{Series: list.Series, Pagination: list.Pagination}, err)
}

func (h *SeriesConnectHandler) CreateProgramEventSeries(ctx context.Context, req *connect.Request[managev1.CreateProgramEventSeriesRequest]) (*connect.Response[managev1.ProgramEventSeries], error) {
	return respond(h.events.CreateProgramEventSeries(ctx, CreateProgramEventSeriesCommand{
		Title: req.Msg.Title, Slug: req.Msg.Slug, Summary: req.Msg.Summary,
		Description: req.Msg.Description, PosterFileID: req.Msg.PosterFileId,
	}))
}

func (h *SeriesConnectHandler) UpdateProgramEventSeries(ctx context.Context, req *connect.Request[managev1.UpdateProgramEventSeriesRequest]) (*connect.Response[managev1.ProgramEventSeries], error) {
	command := UpdateProgramEventSeriesCommand{
		ID: req.Msg.Id, Title: req.Msg.Title, Slug: req.Msg.Slug, Summary: req.Msg.Summary,
		Description: req.Msg.Description, PosterFileID: req.Msg.PosterFileId,
	}
	// UNSPECIFIED leaves the lifecycle alone; an unknown value is rejected.
	if req.Msg.Status != nil && req.Msg.GetStatus() != managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_UNSPECIFIED {
		status, _ := programEventSeriesStatusFromProto(req.Msg.GetStatus())
		command.Status = &status
	}
	return respond(h.events.UpdateProgramEventSeries(ctx, command))
}

func (h *SeriesConnectHandler) DeleteProgramEventSeries(ctx context.Context, req *connect.Request[managev1.DeleteProgramEventSeriesRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.events.DeleteProgramEventSeries(ctx, DeleteProgramEventSeriesCommand{ID: req.Msg.Id}))
}

func (h *TypeConnectHandler) ListProgramEventTypesAdmin(ctx context.Context, req *connect.Request[managev1.ListProgramEventTypesAdminRequest]) (*connect.Response[managev1.ListProgramEventTypesAdminResponse], error) {
	list, err := h.events.ListProgramEventTypesAdmin(ctx, ListProgramEventTypesQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	return respond(&managev1.ListProgramEventTypesAdminResponse{Types: list.Types, Pagination: list.Pagination}, err)
}

func (h *TypeConnectHandler) CreateProgramEventType(ctx context.Context, req *connect.Request[managev1.CreateProgramEventTypeRequest]) (*connect.Response[managev1.ProgramEventType], error) {
	return respond(h.events.CreateProgramEventType(ctx, CreateProgramEventTypeCommand{
		Slug: req.Msg.Slug, Locale: req.Msg.Locale, AcceptLanguage: req.Header().Get("Accept-Language"),
		Name: req.Msg.Name, Description: req.Msg.Description, SortOrder: req.Msg.GetSortOrder(),
		RequiresPlace: req.Msg.GetRequiresPlace(), RequiresStreamURL: req.Msg.GetRequiresStreamUrl(),
	}))
}

func (h *TypeConnectHandler) UpdateProgramEventType(ctx context.Context, req *connect.Request[managev1.UpdateProgramEventTypeRequest]) (*connect.Response[managev1.ProgramEventType], error) {
	command := UpdateProgramEventTypeCommand{
		ID: req.Msg.Id, Slug: req.Msg.Slug, SortOrder: req.Msg.SortOrder, RequiresPlace: req.Msg.RequiresPlace,
		RequiresStreamURL: req.Msg.RequiresStreamUrl, Locale: req.Msg.Locale, Name: req.Msg.Name, Description: req.Msg.Description,
	}
	if req.Msg.Status != nil {
		// UNSPECIFIED and unknown values map to an empty status, which the
		// use case rejects.
		status, _ := programEventTypeStatusFromProto(req.Msg.GetStatus())
		command.Status = &status
	}
	return respond(h.events.UpdateProgramEventType(ctx, command))
}

func (h *TypeConnectHandler) DeleteProgramEventType(ctx context.Context, req *connect.Request[managev1.DeleteProgramEventTypeRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.events.DeleteProgramEventType(ctx, DeleteProgramEventTypeCommand{ID: req.Msg.Id}))
}
