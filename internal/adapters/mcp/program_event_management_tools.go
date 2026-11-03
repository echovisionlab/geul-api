package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/programevent"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	ToolProgramEventCreate         = "program_event_create"
	ToolProgramEventSettingsGet    = "program_event_settings_get"
	ToolProgramEventSettingsUpdate = "program_event_settings_update"
	ToolProgramEventPublish        = "program_event_publish"
	ToolProgramEventArchive        = "program_event_archive"
	ToolProgramEventDelete         = "program_event_delete"
)

var programEventManagementTools = toolRegistry[ProgramEventManagementTools]{
	defineTool(oauthTool(ToolProgramEventCreate, "Create Program Event", "Create a draft event with an empty typed content document. Resolve type_id with program_event_type_list and optional relations with reference tools. Specify the source locale, RFC 3339 start instant, IANA timezone, and location mode explicitly; map_place and hybrid require map_place_id. Add body text using document_paragraph_create after reading the new document.", programEventCreateInputJSONSchema, programEventSettingsOutputJSONSchema, false, false), (*ProgramEventManagementTools).create),
	defineTool(oauthTool(ToolProgramEventSettingsGet, "Get Program Event settings", "Read authorized event settings and locale metadata without returning the body. Copy the returned artists, labels, and clients unchanged into observed_artists, observed_labels, and observed_clients before editing those relations. Read body content with document_read; use their exact revisions for document edits.", contentIDInputJSONSchema, programEventSettingsOutputJSONSchema, true, false), (*ProgramEventManagementTools).getSettings),
	defineTool(oauthTool(ToolProgramEventSettingsUpdate, "Update Program Event settings", "Update event dates, timezone, location, type, series, poster, URLs, or relations through the existing event service. Supply unchanged observed relation arrays from program_event_settings_get when editing artists, labels, or clients; empty desired arrays explicitly remove observed entries while preserving concurrent additions. Credits use whole-list replacement. Use document_metadata_update for title or summary.", programEventSettingsUpdateInputJSONSchema, programEventMutationOutputJSONSchema, false, true), (*ProgramEventManagementTools).updateSettings),
	defineTool(oauthTool(ToolProgramEventPublish, "Publish Program Event", "Publish a draft event or restore an archived event using the existing event lifecycle. Restoring an archived event preserves its original publication date.", contentIDInputJSONSchema, programEventMutationOutputJSONSchema, false, true), (*ProgramEventManagementTools).mutateLifecycle),
	defineTool(oauthTool(ToolProgramEventArchive, "Archive Program Event", "Move a published event to archived using the existing event lifecycle. This does not move the event to draft; program_event_publish restores an archived event.", contentIDInputJSONSchema, programEventMutationOutputJSONSchema, false, true), (*ProgramEventManagementTools).mutateLifecycle),
	defineTool(oauthTool(ToolProgramEventDelete, "Delete Program Event", "Permanently delete an event when permitted by the existing event service and its current lifecycle.", contentIDInputJSONSchema, programEventMutationOutputJSONSchema, false, true), (*ProgramEventManagementTools).mutateLifecycle),
}

// ProgramEventManagementApplication is the Program Event use cases the
// management tools call; authorization and audit stay in programevent.Service.
type ProgramEventManagementApplication interface {
	GetProgramEvent(context.Context, programevent.GetProgramEventQuery) (programevent.EventView, error)
	CreateProgramEvent(context.Context, programevent.CreateProgramEventCommand) (programevent.EventView, error)
	UpdateProgramEvent(context.Context, programevent.UpdateProgramEventCommand) (programevent.EventUpdate, error)
	PublishProgramEvent(context.Context, programevent.PublishProgramEventCommand) (programevent.LifecycleChange, error)
	ArchiveProgramEvent(context.Context, programevent.ArchiveProgramEventCommand) (programevent.LifecycleChange, error)
	DeleteProgramEvent(context.Context, programevent.DeleteProgramEventCommand) error
}

type ProgramEventManagementTools struct {
	application ProgramEventManagementApplication
}

func NewProgramEventManagementTools(application ProgramEventManagementApplication) (*ProgramEventManagementTools, error) {
	if interfaceValueIsNil(application) {
		return nil, errors.New("MCP Program Event management application is required")
	}
	return &ProgramEventManagementTools{application: application}, nil
}

func (*ProgramEventManagementTools) ToolNames() []string { return programEventManagementTools.names() }

func (*ProgramEventManagementTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return programEventManagementTools.list(), nil
}

func (tools *ProgramEventManagementTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	return programEventManagementTools.call(tools, ctx, name, arguments)
}

type programEventCreateArguments struct {
	Title        string                         `json:"title"`
	Slug         string                         `json:"slug"`
	SourceLocale string                         `json:"source_locale"`
	TypeID       string                         `json:"type_id"`
	StartsAt     string                         `json:"starts_at"`
	Timezone     string                         `json:"timezone"`
	LocationMode string                         `json:"location_mode"`
	Summary      *string                        `json:"summary,omitempty"`
	EndsAt       *string                        `json:"ends_at,omitempty"`
	AllDay       bool                           `json:"all_day,omitempty"`
	MapPlaceID   *string                        `json:"map_place_id,omitempty"`
	SeriesID     *string                        `json:"series_id,omitempty"`
	SeriesOrder  *int32                         `json:"series_order,omitempty"`
	PosterFileID *string                        `json:"poster_file_id,omitempty"`
	TicketURL    *string                        `json:"ticket_url,omitempty"`
	StreamURL    *string                        `json:"stream_url,omitempty"`
	ExternalURL  *string                        `json:"external_url,omitempty"`
	Artists      []*managev1.ProgramEventArtist `json:"artists,omitempty"`
	Labels       []*managev1.ProgramEventLabel  `json:"labels,omitempty"`
	Clients      []*managev1.ProgramEventClient `json:"clients,omitempty"`
	Credits      []programEventCreditArguments  `json:"credits,omitempty"`
}

func (tools *ProgramEventManagementTools) create(ctx context.Context, _ string, call toolCall[programEventCreateArguments]) (mcpserver.ToolResult, error) {
	var input programEventCreateArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	for _, field := range []struct{ name, value string }{{"title", input.Title}, {"slug", input.Slug}, {"source_locale", input.SourceLocale}} {
		if strings.TrimSpace(field.value) == "" {
			return executionError(fmt.Errorf("%s is required", field.name))
		}
	}
	if err := validateUUID("type_id", input.TypeID); err != nil {
		return executionError(err)
	}
	startsAt, err := parseProgramEventTimestamp("starts_at", input.StartsAt)
	if err != nil {
		return executionError(err)
	}
	var endsAt *time.Time
	if input.EndsAt != nil {
		endsAt, err = parseProgramEventTimestamp("ends_at", *input.EndsAt)
		if err != nil {
			return executionError(err)
		}
	}
	if err := validateProgramEventTimezone(input.Timezone); err != nil {
		return executionError(err)
	}
	mode, err := parseProgramEventLocationMode(input.LocationMode)
	if err != nil {
		return executionError(err)
	}
	if mode.UsesMapPlace() && (input.MapPlaceID == nil || strings.TrimSpace(*input.MapPlaceID) == "") {
		return executionError(fmt.Errorf("map_place_id is required when location_mode is %s", input.LocationMode))
	}
	created, err := tools.application.CreateProgramEvent(ctx, programevent.CreateProgramEventCommand{
		Title: input.Title, Slug: input.Slug, SourceLocale: input.SourceLocale, AcceptLanguage: input.SourceLocale, TypeID: input.TypeID,
		StartsAt: startsAt, EndsAt: endsAt, Timezone: input.Timezone, AllDay: input.AllDay,
		LocationMode: mode, MapPlaceID: input.MapPlaceID, Summary: input.Summary,
		SeriesID: input.SeriesID, SeriesOrder: input.SeriesOrder, PosterFileID: input.PosterFileID,
		TicketURL: input.TicketURL, StreamURL: input.StreamURL, ExternalURL: input.ExternalURL,
		Artists: programevent.ArtistRelations(input.Artists), Labels: programevent.LabelRelations(input.Labels),
		Clients: programevent.ClientRelations(input.Clients), Credits: toProgramEventCredits(input.Credits),
	})
	if err != nil {
		return expectedToolError(err)
	}
	output := programEventSettingsOutput(programevent.EventMessage(created))
	output["changed"] = true
	return contentResult(output)
}

func (tools *ProgramEventManagementTools) getSettings(ctx context.Context, _ string, call toolCall[contentIDArguments]) (mcpserver.ToolResult, error) {
	var input contentIDArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if err := validateUUID("document_id", input.DocumentID); err != nil {
		return executionError(err)
	}
	event, err := tools.application.GetProgramEvent(ctx, programevent.GetProgramEventQuery{ID: input.DocumentID})
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(programEventSettingsOutput(programevent.EventMessage(event)))
}

type programEventSettingsArguments struct {
	DocumentID       string                          `json:"document_id"`
	Slug             *string                         `json:"slug,omitempty"`
	TypeID           *string                         `json:"type_id,omitempty"`
	StartsAt         *string                         `json:"starts_at,omitempty"`
	EndsAt           *string                         `json:"ends_at,omitempty"`
	ClearEndsAt      bool                            `json:"clear_ends_at,omitempty"`
	Timezone         *string                         `json:"timezone,omitempty"`
	AllDay           *bool                           `json:"all_day,omitempty"`
	LocationMode     *string                         `json:"location_mode,omitempty"`
	MapPlaceID       *string                         `json:"map_place_id,omitempty"`
	SeriesID         *string                         `json:"series_id,omitempty"`
	SeriesOrder      *int32                          `json:"series_order,omitempty"`
	ClearSeriesOrder bool                            `json:"clear_series_order,omitempty"`
	PosterFileID     *string                         `json:"poster_file_id,omitempty"`
	TicketURL        *string                         `json:"ticket_url,omitempty"`
	StreamURL        *string                         `json:"stream_url,omitempty"`
	ExternalURL      *string                         `json:"external_url,omitempty"`
	Artists          *[]*managev1.ProgramEventArtist `json:"artists,omitempty"`
	ObservedArtists  *[]*managev1.ProgramEventArtist `json:"observed_artists,omitempty"`
	Labels           *[]*managev1.ProgramEventLabel  `json:"labels,omitempty"`
	ObservedLabels   *[]*managev1.ProgramEventLabel  `json:"observed_labels,omitempty"`
	Clients          *[]*managev1.ProgramEventClient `json:"clients,omitempty"`
	ObservedClients  *[]*managev1.ProgramEventClient `json:"observed_clients,omitempty"`
	Credits          *[]programEventCreditArguments  `json:"credits,omitempty"`
}

func (tools *ProgramEventManagementTools) updateSettings(ctx context.Context, _ string, call toolCall[programEventSettingsArguments]) (mcpserver.ToolResult, error) {
	arguments := call.Arguments

	var input programEventSettingsArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if err := rejectNullArguments(arguments, "artists", "observed_artists", "labels", "observed_labels", "clients", "observed_clients", "credits"); err != nil {
		return executionError(err)
	}
	if err := validateUUID("document_id", input.DocumentID); err != nil {
		return executionError(err)
	}
	if input.Slug == nil && input.TypeID == nil && input.StartsAt == nil && input.EndsAt == nil && !input.ClearEndsAt && input.Timezone == nil && input.AllDay == nil && input.LocationMode == nil && input.MapPlaceID == nil && input.SeriesID == nil && input.SeriesOrder == nil && !input.ClearSeriesOrder && input.PosterFileID == nil && input.TicketURL == nil && input.StreamURL == nil && input.ExternalURL == nil && input.Artists == nil && input.Labels == nil && input.Clients == nil && input.Credits == nil {
		return executionError(errors.New("at least one Program Event setting is required"))
	}
	if input.Artists != nil && input.ObservedArtists == nil {
		return executionError(errors.New("observed_artists is required when updating artists"))
	}
	if input.Labels != nil && input.ObservedLabels == nil {
		return executionError(errors.New("observed_labels is required when updating labels"))
	}
	if input.Clients != nil && input.ObservedClients == nil {
		return executionError(errors.New("observed_clients is required when updating clients"))
	}
	command := programevent.UpdateProgramEventCommand{
		ID: input.DocumentID, Slug: input.Slug, TypeID: input.TypeID, ClearEndsAt: input.ClearEndsAt,
		Timezone: input.Timezone, AllDay: input.AllDay, MapPlaceID: input.MapPlaceID,
		SeriesID: input.SeriesID, SeriesOrder: input.SeriesOrder, ClearSeriesOrder: input.ClearSeriesOrder,
		PosterFileID: input.PosterFileID, TicketURL: input.TicketURL, StreamURL: input.StreamURL, ExternalURL: input.ExternalURL,
	}
	var err error
	if input.StartsAt != nil {
		command.StartsAt, err = parseProgramEventTimestamp("starts_at", *input.StartsAt)
		if err != nil {
			return executionError(err)
		}
	}
	if input.EndsAt != nil {
		command.EndsAt, err = parseProgramEventTimestamp("ends_at", *input.EndsAt)
		if err != nil {
			return executionError(err)
		}
	}
	if input.Timezone != nil {
		if err := validateProgramEventTimezone(*input.Timezone); err != nil {
			return executionError(err)
		}
	}
	if input.LocationMode != nil {
		mode, err := parseProgramEventLocationMode(*input.LocationMode)
		if err != nil {
			return executionError(err)
		}
		command.LocationMode = &mode
	}
	if err := programEventRelationsEdits(&command, input); err != nil {
		return expectedToolError(err)
	}
	if input.Credits != nil {
		command.Credits = programevent.CreditsEdit{Replace: true, Desired: toProgramEventCredits(*input.Credits)}
	}
	updated, err := tools.application.UpdateProgramEvent(ctx, command)
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"document_type": "program_event", "document_id": updated.ID, "changed": updated.Changed, "updated_at": timestampString(timestamppb.New(updated.UpdatedAt))})
}

// programEventRelationsEdits sets the artist, label and client edits of
// input on command.
func programEventRelationsEdits(command *programevent.UpdateProgramEventCommand, input programEventSettingsArguments) error {
	if err := setRelationsEdit(&command.Artists, "artists", input.Artists, input.ObservedArtists, programevent.ArtistRelations); err != nil {
		return err
	}
	if err := setRelationsEdit(&command.Labels, "labels", input.Labels, input.ObservedLabels, programevent.LabelRelations); err != nil {
		return err
	}
	return setRelationsEdit(&command.Clients, "clients", input.Clients, input.ObservedClients, programevent.ClientRelations)
}

// setRelationsEdit replaces the relations of collection with desired, merged
// into observed. A null row is rejected as the service rejected it when it
// received the rows.
func setRelationsEdit[Row any](
	edit *programevent.RelationsEdit,
	collection string,
	desired, observed *[]*Row,
	convert func([]*Row) []programevent.Relation,
) error {
	if observed != nil {
		rows := convert(*observed)
		edit.Observed = &rows
	}
	if desired == nil {
		return nil
	}
	if containsNil(desired) || containsNil(observed) {
		return domainerr.InvalidField(collection, "relation rows must not be null")
	}
	edit.Replace, edit.Desired = true, convert(*desired)
	return nil
}

func containsNil[Row any](rows *[]*Row) bool {
	if rows == nil {
		return false
	}
	for _, row := range *rows {
		if row == nil {
			return true
		}
	}
	return false
}

func (tools *ProgramEventManagementTools) mutateLifecycle(ctx context.Context, name string, call toolCall[contentIDArguments]) (mcpserver.ToolResult, error) {
	var input contentIDArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if err := validateUUID("document_id", input.DocumentID); err != nil {
		return executionError(err)
	}
	if name == ToolProgramEventDelete {
		if err := tools.application.DeleteProgramEvent(ctx, programevent.DeleteProgramEventCommand{ID: input.DocumentID}); err != nil {
			return expectedToolError(err)
		}
		return contentResult(map[string]any{"document_type": "program_event", "document_id": input.DocumentID, "changed": true, "deleted": true})
	}
	var change programevent.LifecycleChange
	var err error
	if name == ToolProgramEventPublish {
		change, err = tools.application.PublishProgramEvent(ctx, programevent.PublishProgramEventCommand{ID: input.DocumentID})
	} else {
		change, err = tools.application.ArchiveProgramEvent(ctx, programevent.ArchiveProgramEventCommand{ID: input.DocumentID})
	}
	if err != nil {
		return expectedToolError(err)
	}
	var publishedAt *timestamppb.Timestamp
	if change.PublishedAt != nil {
		publishedAt = timestamppb.New(*change.PublishedAt)
	}
	return contentResult(map[string]any{
		"document_type": "program_event", "document_id": change.ID, "changed": change.Changed,
		"status":       contentStatus(string(change.Status), "PROGRAM_EVENT_STATUS_"),
		"published_at": optionalProgramEventTimestamp(publishedAt), "updated_at": timestampString(timestamppb.New(change.UpdatedAt)),
	})
}

func parseProgramEventTimestamp(field, value string) (*time.Time, error) {
	instant, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, fmt.Errorf("%s must be an RFC 3339 instant with an explicit offset: %w", field, err)
	}
	instant = timestamppb.New(instant).AsTime()
	return &instant, nil
}

func validateProgramEventTimezone(value string) error {
	if value == "" || value == "Local" {
		return errors.New("timezone must be an explicit IANA time zone, such as Asia/Seoul or UTC")
	}
	if _, err := time.LoadLocation(value); err != nil {
		return fmt.Errorf("timezone must be an IANA time zone: %w", err)
	}
	return nil
}

func parseProgramEventLocationMode(value string) (programevent.LocationMode, error) {
	switch value {
	case "map_place":
		return programevent.LocationModeMapPlace, nil
	case "online":
		return programevent.LocationModeOnline, nil
	case "hybrid":
		return programevent.LocationModeHybrid, nil
	case "tba":
		return programevent.LocationModeTBA, nil
	default:
		return "", errors.New("location_mode must be map_place, online, hybrid, or tba")
	}
}

func optionalProgramEventTimestamp(value *timestamppb.Timestamp) any {
	if value == nil {
		return nil
	}
	return timestampString(value)
}

// Credit input excludes the hydrated Artist and Member projections returned
// by the owning read service; only its native writable fields cross this tool.
type programEventCreditArguments struct {
	ID          string  `json:"id,omitempty"`
	ArtistID    *string `json:"artist_id,omitempty"`
	MemberID    *string `json:"member_id,omitempty"`
	DisplayName *string `json:"display_name,omitempty"`
	CreditRole  *string `json:"credit_role,omitempty"`
	Description *string `json:"description,omitempty"`
	SortOrder   int32   `json:"sort_order,omitempty"`
}

func toProgramEventCredits(input []programEventCreditArguments) []programevent.Credit {
	credits := make([]programevent.Credit, 0, len(input))
	for _, row := range input {
		credits = append(credits, programevent.Credit{
			ID: row.ID, ArtistID: row.ArtistID, MemberID: row.MemberID, DisplayName: row.DisplayName,
			CreditRole: row.CreditRole, Description: row.Description, SortOrder: row.SortOrder,
		})
	}
	return credits
}

func programEventSettingsOutput(event *managev1.ProgramEvent) map[string]any {
	artists := make([]map[string]any, 0, len(event.Artists))
	for _, row := range event.Artists {
		item := map[string]any{"artist_id": row.ArtistId, "sort_order": row.SortOrder}
		if row.Role != nil {
			item["role"] = *row.Role
		}
		artists = append(artists, item)
	}
	labels := make([]map[string]any, 0, len(event.Labels))
	for _, row := range event.Labels {
		item := map[string]any{"label_id": row.LabelId, "sort_order": row.SortOrder}
		if row.Role != nil {
			item["role"] = *row.Role
		}
		labels = append(labels, item)
	}
	clients := make([]map[string]any, 0, len(event.Clients))
	for _, row := range event.Clients {
		item := map[string]any{"client_id": row.ClientId, "sort_order": row.SortOrder}
		if row.Role != nil {
			item["role"] = *row.Role
		}
		clients = append(clients, item)
	}
	credits := make([]map[string]any, 0, len(event.Credits))
	for _, row := range event.Credits {
		item := map[string]any{"id": row.Id, "sort_order": row.SortOrder}
		for name, value := range map[string]*string{"artist_id": row.ArtistId, "member_id": row.MemberId, "display_name": row.DisplayName, "credit_role": row.CreditRole, "description": row.Description} {
			if value != nil {
				item[name] = *value
			}
		}
		credits = append(credits, item)
	}
	locales := make([]map[string]any, 0, len(event.Locales))
	for _, row := range event.Locales {
		item := map[string]any{"locale": row.Locale}
		if row.Summary != nil {
			item["summary"] = *row.Summary
		}
		locales = append(locales, item)
	}
	output := map[string]any{
		"document_type": "program_event", "document_id": event.Id, "title": event.Title, "slug": event.Slug,
		"status": contentStatus(event.Status.String(), "PROGRAM_EVENT_STATUS_"), "source_locale": event.SourceLocale,
		"type_id": event.TypeId, "series_id": optionalStringValue(event.SeriesId), "starts_at": timestampString(event.StartsAt),
		"ends_at": optionalProgramEventTimestamp(event.EndsAt), "timezone": event.Timezone, "all_day": event.AllDay,
		"location_mode": contentStatus(event.LocationMode.String(), "PROGRAM_EVENT_LOCATION_MODE_"),
		"map_place_id":  optionalStringValue(event.MapPlaceId), "poster_file_id": optionalStringValue(event.PosterFileId),
		"ticket_url": optionalStringValue(event.TicketUrl), "stream_url": optionalStringValue(event.StreamUrl), "external_url": optionalStringValue(event.ExternalUrl),
		"document_revision": event.DocumentRevision, "updated_at": timestampString(event.UpdatedAt), "published_at": optionalProgramEventTimestamp(event.PublishedAt),
		"artists": artists, "labels": labels, "clients": clients, "credits": credits, "locales": locales,
	}
	if event.SeriesOrder != nil {
		output["series_order"] = *event.SeriesOrder
	}
	return output
}
