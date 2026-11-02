package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/programevent"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"google.golang.org/protobuf/proto"
)

const programEventManagementID = "11111111-1111-4111-8111-111111111111"
const programEventManagementTypeID = "22222222-2222-4222-8222-222222222222"
const programEventManagementRelationID = "33333333-3333-4333-8333-333333333333"
const programEventManagementRevision = "44444444-4444-4444-8444-444444444444"

func programEventManagementCreateArguments(t *testing.T) mcpserver.ToolArguments {
	t.Helper()
	return toolArguments(t, `{"title":"Concert","slug":"concert","source_locale":"ko","type_id":"`+programEventManagementTypeID+`","starts_at":"2026-10-10T19:00:00+09:00","timezone":"Asia/Seoul","location_mode":"online"}`)
}

func TestProgramEventManagementDescriptors(t *testing.T) {
	if _, err := NewProgramEventManagementTools(nil); err == nil {
		t.Fatal("missing owner accepted")
	}
	var nilOwner *recordingProgramEventManagement
	if _, err := NewProgramEventManagementTools(nilOwner); err == nil {
		t.Fatal("typed nil owner accepted")
	}
	tools, err := NewProgramEventManagementTools(newRecordingProgramEventManagement())
	if err != nil {
		t.Fatal(err)
	}
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil || len(listed) != 6 {
		t.Fatalf("descriptors = %v, err=%v", listed, err)
	}
	wantNames := []string{ToolProgramEventCreate, ToolProgramEventSettingsGet, ToolProgramEventSettingsUpdate, ToolProgramEventPublish, ToolProgramEventArchive, ToolProgramEventDelete}
	if !reflect.DeepEqual(tools.ToolNames(), wantNames) {
		t.Fatalf("names = %v", tools.ToolNames())
	}
	for _, tool := range listed {
		assertMCPToolOAuthSecurity(t, tool)
		for _, raw := range []json.RawMessage{tool.InputSchema, tool.OutputSchema} {
			var schema map[string]any
			if err := json.Unmarshal(raw, &schema); err != nil || schema["type"] != "object" || schema["additionalProperties"] != false {
				t.Fatalf("%s schema = %s, err=%v", tool.Name, raw, err)
			}
		}
	}
	var createSchema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(listed[0].InputSchema, &createSchema); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(createSchema.Required, []string{"title", "slug", "source_locale", "type_id", "starts_at", "timezone", "location_mode"}) {
		t.Fatalf("required create fields = %v", createSchema.Required)
	}
	if !strings.Contains(listed[4].Description, "does not move the event to draft") {
		t.Fatal("archive advertises incorrect lifecycle")
	}
}

func TestProgramEventManagementCreateForwardsCompleteNativeRequest(t *testing.T) {
	owner := newRecordingProgramEventManagement()
	tools, _ := NewProgramEventManagementTools(owner)
	arguments := programEventManagementCreateArguments(t)
	for name, value := range map[string]any{
		"ends_at": "2026-10-10T22:00:00+09:00", "all_day": true, "location_mode": "hybrid",
		"map_place_id": programEventManagementRelationID, "summary": "Summary", "series_id": programEventManagementRelationID,
		"series_order": 0, "poster_file_id": programEventManagementRelationID,
		"ticket_url": "https://example.test/ticket", "stream_url": "https://example.test/live", "external_url": "https://example.test/event",
		"artists": []map[string]any{{"artist_id": programEventManagementRelationID, "role": "performer", "sort_order": 2}},
		"labels":  []map[string]any{{"label_id": programEventManagementRelationID, "role": "organizer", "sort_order": 3}},
		"clients": []map[string]any{{"client_id": programEventManagementRelationID, "role": "host", "sort_order": 4}},
		"credits": []map[string]any{{"member_id": programEventManagementRelationID, "credit_role": "producer", "description": "Credit", "sort_order": 5}},
	} {
		arguments[name], _ = json.Marshal(value)
	}
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolProgramEventCreate, arguments)
	if err != nil {
		t.Fatal(err)
	}
	startsAt, endsAt := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC), time.Date(2026, 10, 10, 13, 0, 0, 0, time.UTC)
	want := programevent.CreateProgramEventCommand{
		Title: "Concert", Slug: "concert", SourceLocale: "ko", AcceptLanguage: "ko", TypeID: programEventManagementTypeID,
		StartsAt: &startsAt, EndsAt: &endsAt, Timezone: "Asia/Seoul", AllDay: true,
		LocationMode: programevent.LocationModeHybrid,
		MapPlaceID:   proto.String(programEventManagementRelationID), Summary: proto.String("Summary"),
		SeriesID: proto.String(programEventManagementRelationID), SeriesOrder: proto.Int32(0), PosterFileID: proto.String(programEventManagementRelationID),
		TicketURL: proto.String("https://example.test/ticket"), StreamURL: proto.String("https://example.test/live"), ExternalURL: proto.String("https://example.test/event"),
		Artists: []programevent.Relation{{ID: programEventManagementRelationID, Role: proto.String("performer"), SortOrder: 2}},
		Labels:  []programevent.Relation{{ID: programEventManagementRelationID, Role: proto.String("organizer"), SortOrder: 3}},
		Clients: []programevent.Relation{{ID: programEventManagementRelationID, Role: proto.String("host"), SortOrder: 4}},
		Credits: []programevent.Credit{{MemberID: proto.String(programEventManagementRelationID), CreditRole: proto.String("producer"), Description: proto.String("Credit"), SortOrder: 5}},
	}
	if !reflect.DeepEqual(owner.create, want) {
		t.Fatalf("create command = %+v; want %+v", owner.create, want)
	}
	if result.StructuredContent["status"] != "draft" || result.StructuredContent["document_revision"] != programEventManagementRevision || result.StructuredContent["changed"] != true {
		t.Fatalf("created result = %#v", result.StructuredContent)
	}
}

func TestProgramEventManagementSettingsReadAndObservedRelationRoundTrip(t *testing.T) {
	owner := newRecordingProgramEventManagement()
	owner.event.Artists = []programevent.Relation{{ID: programEventManagementRelationID, Role: proto.String(""), SortOrder: 2}}
	owner.event.Labels = []programevent.Relation{{ID: programEventManagementRelationID, SortOrder: 3}}
	owner.event.Clients = []programevent.Relation{{ID: programEventManagementRelationID, Role: proto.String("host"), SortOrder: 4}}
	owner.event.Credits = []programevent.CreditView{{CreditRecord: programevent.CreditRecord{ID: programEventManagementRelationID, MemberID: proto.String(programEventManagementTypeID), DisplayName: proto.String("Producer"), SortOrder: 5}, Artist: &programevent.CreditArtist{Name: "hydrated private data"}}}
	owner.event.Locales = []programevent.LocaleView{{Locale: "ko", Summary: proto.String(""), ContentHTML: "body must be excluded", ContentText: "body must be excluded"}}
	owner.event.Document = &contentv1.RichTextDocument{}
	tools, _ := NewProgramEventManagementTools(owner)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolProgramEventSettingsGet, toolArguments(t, `{"document_id":"`+programEventManagementID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result.StructuredContent)
	if strings.Contains(string(encoded), "body must be excluded") || strings.Contains(string(encoded), "hydrated private data") || result.StructuredContent["document"] != nil || result.StructuredContent["block_media"] != nil {
		t.Fatalf("settings leaked body or hydration: %s", encoded)
	}
	arguments := toolArguments(t, `{"document_id":"`+programEventManagementID+`","artists":[],"labels":[],"clients":[],"credits":[]}`)
	for _, relation := range []string{"artists", "labels", "clients"} {
		arguments["observed_"+relation], _ = json.Marshal(result.StructuredContent[relation])
	}
	if _, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolProgramEventSettingsUpdate, arguments); err != nil {
		t.Fatal(err)
	}
	r := owner.update
	if !r.Artists.Replace || !r.Labels.Replace || !r.Clients.Replace || !r.Credits.Replace || len(r.Artists.Desired)+len(r.Labels.Desired)+len(r.Clients.Desired)+len(r.Credits.Desired) != 0 {
		t.Fatalf("empty replacement flags lost: %+v", r)
	}
	observed := func(rows []programevent.Relation) *[]programevent.Relation { return &rows }
	if !reflect.DeepEqual(r.Artists.Observed, observed(owner.event.Artists)) || !reflect.DeepEqual(r.Labels.Observed, observed(owner.event.Labels)) || !reflect.DeepEqual(r.Clients.Observed, observed(owner.event.Clients)) {
		t.Fatalf("observed snapshots changed: %+v", r)
	}
}

func TestProgramEventManagementSettingsForwardsNativeOptionalFields(t *testing.T) {
	owner := newRecordingProgramEventManagement()
	tools, _ := NewProgramEventManagementTools(owner)
	_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolProgramEventSettingsUpdate, toolArguments(t, `{
  "document_id":"`+programEventManagementID+`","slug":"renamed","type_id":"`+programEventManagementTypeID+`",
  "starts_at":"2026-10-10T20:00:00+09:00","ends_at":"2026-10-10T21:00:00+09:00","clear_ends_at":true,
  "timezone":"UTC","all_day":false,"location_mode":"tba","map_place_id":"","series_id":"","series_order":0,"clear_series_order":true,
  "poster_file_id":"","ticket_url":"","stream_url":"","external_url":"",
  "artists":[{"artist_id":"`+programEventManagementRelationID+`","role":"performer","sort_order":7}],"observed_artists":[],
  "labels":[{"label_id":"`+programEventManagementRelationID+`","sort_order":8}],"observed_labels":[],
  "clients":[{"client_id":"`+programEventManagementRelationID+`","sort_order":9}],"observed_clients":[],
  "credits":[{"id":"`+programEventManagementRelationID+`","display_name":"Guest","credit_role":"speaker","sort_order":10}]
}`))
	if err != nil {
		t.Fatal(err)
	}
	startsAt, endsAt := time.Date(2026, 10, 10, 11, 0, 0, 0, time.UTC), time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	tba := programevent.LocationModeTBA
	none := []programevent.Relation{}
	want := programevent.UpdateProgramEventCommand{
		ID: programEventManagementID, Slug: proto.String("renamed"), TypeID: proto.String(programEventManagementTypeID),
		StartsAt: &startsAt, EndsAt: &endsAt, ClearEndsAt: true,
		Timezone: proto.String("UTC"), AllDay: proto.Bool(false), LocationMode: &tba,
		MapPlaceID: proto.String(""), SeriesID: proto.String(""), SeriesOrder: proto.Int32(0), ClearSeriesOrder: true,
		PosterFileID: proto.String(""), TicketURL: proto.String(""), StreamURL: proto.String(""), ExternalURL: proto.String(""),
		Artists: programevent.RelationsEdit{Replace: true, Desired: []programevent.Relation{{ID: programEventManagementRelationID, Role: proto.String("performer"), SortOrder: 7}}, Observed: &none},
		Labels:  programevent.RelationsEdit{Replace: true, Desired: []programevent.Relation{{ID: programEventManagementRelationID, SortOrder: 8}}, Observed: &none},
		Clients: programevent.RelationsEdit{Replace: true, Desired: []programevent.Relation{{ID: programEventManagementRelationID, SortOrder: 9}}, Observed: &none},
		Credits: programevent.CreditsEdit{Replace: true, Desired: []programevent.Credit{{ID: programEventManagementRelationID, DisplayName: proto.String("Guest"), CreditRole: proto.String("speaker"), SortOrder: 10}}},
	}
	if !reflect.DeepEqual(owner.update, want) {
		t.Fatalf("settings command = %+v; want %+v", owner.update, want)
	}
}

func TestProgramEventManagementRejectsInvalidArgumentsBeforeOwnerCall(t *testing.T) {
	for _, test := range []struct {
		name, tool, field string
		value             any
	}{
		{"missing title", ToolProgramEventCreate, "title", ""},
		{"missing source locale", ToolProgramEventCreate, "source_locale", ""},
		{"invalid type UUID", ToolProgramEventCreate, "type_id", "concert"},
		{"local start time without offset", ToolProgramEventCreate, "starts_at", "2026-10-10T19:00:00"},
		{"invalid end", ToolProgramEventCreate, "ends_at", "tomorrow"},
		{"invalid timezone", ToolProgramEventCreate, "timezone", "not/a/timezone"},
		{"implicit machine timezone", ToolProgramEventCreate, "timezone", "Local"},
		{"missing location mode", ToolProgramEventCreate, "location_mode", ""},
		{"map place mode without place", ToolProgramEventCreate, "location_mode", "map_place"},
		{"hybrid mode without place", ToolProgramEventCreate, "location_mode", "hybrid"},
		{"missing artists baseline", ToolProgramEventSettingsUpdate, "artists", []any{}},
		{"missing labels baseline", ToolProgramEventSettingsUpdate, "labels", []any{}},
		{"missing clients baseline", ToolProgramEventSettingsUpdate, "clients", []any{}},
		{"null credits", ToolProgramEventSettingsUpdate, "credits", nil},
		{"hydrated credit field is not writable", ToolProgramEventSettingsUpdate, "credits", []map[string]any{{"display_name": "Guest", "artist": map[string]any{"name": "not an input field"}}}},
		{"no update", ToolProgramEventSettingsUpdate, "document_id", programEventManagementID},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner := newRecordingProgramEventManagement()
			tools, _ := NewProgramEventManagementTools(owner)
			arguments := programEventManagementCreateArguments(t)
			if test.tool == ToolProgramEventSettingsUpdate {
				arguments = toolArguments(t, `{"document_id":"`+programEventManagementID+`"}`)
			}
			arguments[test.field], _ = json.Marshal(test.value)
			_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, test.tool, arguments)
			var rejected *mcpserver.ToolExecutionError
			if !errors.As(err, &rejected) || len(owner.calls) != 0 {
				t.Fatalf("invalid arguments reached owner: calls=%v, err=%v", owner.calls, err)
			}
		})
	}
}

func TestProgramEventManagementCreateReadUpdateLifecycleFlow(t *testing.T) {
	owner := newRecordingProgramEventManagement()
	tools, _ := NewProgramEventManagementTools(owner)
	idArguments := toolArguments(t, `{"document_id":"`+programEventManagementID+`"}`)
	for _, name := range []string{ToolProgramEventCreate, ToolProgramEventSettingsGet, ToolProgramEventSettingsUpdate, ToolProgramEventPublish, ToolProgramEventArchive, ToolProgramEventPublish, ToolProgramEventDelete} {
		arguments := idArguments
		switch name {
		case ToolProgramEventCreate:
			arguments = programEventManagementCreateArguments(t)
		case ToolProgramEventSettingsUpdate:
			arguments = toolArguments(t, `{"document_id":"`+programEventManagementID+`","all_day":true}`)
		}
		result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, name, arguments)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if name == ToolProgramEventArchive && result.StructuredContent["status"] != "archived" {
			t.Fatalf("archive result = %#v", result.StructuredContent)
		}
		if name == ToolProgramEventPublish && result.StructuredContent["published_at"] != "2026-10-05T00:00:00Z" {
			t.Fatalf("publication date changed: %#v", result.StructuredContent)
		}
	}
	if !reflect.DeepEqual(owner.calls, []string{"create", "get", "update", "publish", "archive", "publish", "delete"}) || owner.published.ID != programEventManagementID || owner.archived.ID != programEventManagementID || owner.deleted.ID != programEventManagementID {
		t.Fatalf("native flow = %v", owner.calls)
	}
	if _, err := tools.CallTool(t.Context(), mcpserver.Principal{}, "program_event_unpublish", idArguments); !errors.Is(err, mcpserver.ErrUnknownTool) {
		t.Fatalf("invented lifecycle accepted: %v", err)
	}
}

func TestProgramEventManagementPreservesOwningAuthorityAndSafeErrors(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeUnauthenticated, connect.CodePermissionDenied, connect.CodeNotFound, connect.CodeInvalidArgument, connect.CodeFailedPrecondition, connect.CodeAborted, connect.CodeInternal, connect.CodeUnavailable} {
		for _, name := range []string{ToolProgramEventCreate, ToolProgramEventSettingsGet, ToolProgramEventSettingsUpdate, ToolProgramEventPublish, ToolProgramEventArchive, ToolProgramEventDelete} {
			t.Run(code.String()+"/"+name, func(t *testing.T) {
				owner := newRecordingProgramEventManagement()
				owner.err = connect.NewError(code, errors.New("owning service decision"))
				tools, _ := NewProgramEventManagementTools(owner)
				arguments := toolArguments(t, `{"document_id":"`+programEventManagementID+`"}`)
				switch name {
				case ToolProgramEventCreate:
					arguments = programEventManagementCreateArguments(t)
				case ToolProgramEventSettingsUpdate:
					arguments["all_day"] = json.RawMessage(`true`)
				}
				ctx := context.WithValue(t.Context(), programEventManagementContextKey{}, "authenticated actor")
				result, err := tools.CallTool(ctx, mcpserver.Principal{}, name, arguments)
				if len(owner.calls) != 1 || owner.ctx.Value(programEventManagementContextKey{}) != "authenticated actor" || result.StructuredContent != nil {
					t.Fatalf("authority bypass or leaked result: calls=%v, result=%#v, err=%v", owner.calls, result, err)
				}
				if code == connect.CodeInternal {
					if err != owner.err { //nolint:errorlint // asserts the error is returned unchanged
						t.Fatalf("internal error changed before generic handler: %v", err)
					}
					return
				}
				want := "owning service decision"
				if code == connect.CodeUnavailable {
					want = "The service is temporarily unavailable"
				}
				var safe *mcpserver.ToolExecutionError
				if !errors.As(err, &safe) || safe.Message != want {
					t.Fatalf("safe owning error = %v, want %q", err, want)
				}
			})
		}
	}
}

type programEventManagementContextKey struct{}

type recordingProgramEventManagement struct {
	event     *programevent.EventView
	err       error
	ctx       context.Context
	calls     []string
	create    programevent.CreateProgramEventCommand
	update    programevent.UpdateProgramEventCommand
	published programevent.PublishProgramEventCommand
	archived  programevent.ArchiveProgramEventCommand
	deleted   programevent.DeleteProgramEventCommand
}

func newRecordingProgramEventManagement() *recordingProgramEventManagement {
	return &recordingProgramEventManagement{event: &programevent.EventView{Event: programevent.Event{
		ID: programEventManagementID, Title: "Concert", Slug: "concert", Status: programevent.StatusDraft,
		SourceLocale: "ko", TypeID: programEventManagementTypeID, Timezone: "Asia/Seoul",
		LocationMode: programevent.LocationModeOnline,
		StartsAt:     time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC),
		UpdatedAt:    time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC),
	}, DocumentRevision: programEventManagementRevision}}
}

func (owner *recordingProgramEventManagement) record(ctx context.Context, call string) error {
	owner.ctx = ctx
	owner.calls = append(owner.calls, call)
	return owner.err
}

func (owner *recordingProgramEventManagement) CreateProgramEvent(ctx context.Context, command programevent.CreateProgramEventCommand) (programevent.EventView, error) {
	owner.create = command
	if err := owner.record(ctx, "create"); err != nil {
		return programevent.EventView{}, err
	}
	return *owner.event, nil
}

func (owner *recordingProgramEventManagement) GetProgramEvent(ctx context.Context, query programevent.GetProgramEventQuery) (programevent.EventView, error) {
	if err := owner.record(ctx, "get"); err != nil {
		return programevent.EventView{}, err
	}
	if query.ID != owner.event.ID {
		return programevent.EventView{}, connect.NewError(connect.CodeNotFound, errors.New("program event not found"))
	}
	return *owner.event, nil
}

func (owner *recordingProgramEventManagement) UpdateProgramEvent(ctx context.Context, command programevent.UpdateProgramEventCommand) (programevent.EventUpdate, error) {
	owner.update = command
	if err := owner.record(ctx, "update"); err != nil {
		return programevent.EventUpdate{}, err
	}
	return programevent.EventUpdate{ID: command.ID, Changed: true, UpdatedAt: owner.event.UpdatedAt}, nil
}

func (owner *recordingProgramEventManagement) lifecycle(id string, status programevent.Status) programevent.LifecycleChange {
	change := programevent.LifecycleChange{ID: id, Changed: true, Status: status, UpdatedAt: owner.event.UpdatedAt}
	if owner.event.PublishedAt != nil {
		publishedAt := *owner.event.PublishedAt
		change.PublishedAt = &publishedAt
	}
	return change
}

func (owner *recordingProgramEventManagement) PublishProgramEvent(ctx context.Context, command programevent.PublishProgramEventCommand) (programevent.LifecycleChange, error) {
	owner.published = command
	if err := owner.record(ctx, "publish"); err != nil {
		return programevent.LifecycleChange{}, err
	}
	owner.event.Status = programevent.StatusPublished
	if owner.event.PublishedAt == nil {
		publishedAt := owner.event.UpdatedAt
		owner.event.PublishedAt = &publishedAt
	}
	return owner.lifecycle(command.ID, programevent.StatusPublished), nil
}

func (owner *recordingProgramEventManagement) ArchiveProgramEvent(ctx context.Context, command programevent.ArchiveProgramEventCommand) (programevent.LifecycleChange, error) {
	owner.archived = command
	if err := owner.record(ctx, "archive"); err != nil {
		return programevent.LifecycleChange{}, err
	}
	owner.event.Status = programevent.StatusArchived
	return owner.lifecycle(command.ID, programevent.StatusArchived), nil
}

func (owner *recordingProgramEventManagement) DeleteProgramEvent(ctx context.Context, command programevent.DeleteProgramEventCommand) error {
	owner.deleted = command
	return owner.record(ctx, "delete")
}
