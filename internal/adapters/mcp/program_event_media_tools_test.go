package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/programevent"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

const eventMediaDocumentID = "11111111-1111-4111-8111-111111111111"
const eventMediaFileID = "22222222-2222-4222-8222-222222222222"
const eventMediaID = "33333333-3333-4333-8333-333333333333"
const eventMediaPeerID = "44444444-4444-4444-8444-444444444444"

func TestProgramEventMediaDescriptors(t *testing.T) {
	_, err := NewProgramEventMediaTools(nil)
	require.Error(t, err)
	var nilOwner *recordingEventMedia
	_, err = NewProgramEventMediaTools(nilOwner)
	require.Error(t, err)
	tools, err := NewProgramEventMediaTools(newRecordingEventMedia())
	require.NoError(t, err)
	require.Equal(t, []string{ToolProgramEventMediaList, ToolProgramEventMediaAdd, ToolProgramEventMediaRemove, ToolProgramEventMediaReorder}, tools.ToolNames())
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	require.NoError(t, err)
	require.Len(t, listed, 4)
	for _, tool := range listed {
		assertMCPToolOAuthSecurity(t, tool)
		for _, raw := range []json.RawMessage{tool.InputSchema, tool.OutputSchema} {
			var schema map[string]any
			require.NoError(t, json.Unmarshal(raw, &schema))
			require.Equal(t, "object", schema["type"])
			require.Equal(t, false, schema["additionalProperties"])
			assertSchemaRootRefsResolve(t, schema, schema)
		}
	}
	require.Contains(t, listed[1].Description, "omitted and empty alt/caption clear")
	require.Contains(t, listed[3].Description, "unchanged order retains the existing primary")
	listed[0].InputSchema[0] = '!'
	fresh, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	require.NoError(t, err)
	require.True(t, json.Valid(fresh[0].InputSchema), "callers must not mutate registered schemas")
}

func TestProgramEventMediaEveryAdvertisedRoleForwardsToOwner(t *testing.T) {
	var schema struct {
		Properties struct {
			Role struct {
				Enum []string `json:"enum"`
			} `json:"role"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal([]byte(programEventMediaAddInputJSONSchema), &schema))
	require.Equal(t, []string{"poster", "gallery", "lineup", "sponsor", "social", "venue"}, schema.Properties.Role.Enum)
	for _, role := range schema.Properties.Role.Enum {
		t.Run(role, func(t *testing.T) {
			owner := newRecordingEventMedia()
			tools, _ := NewProgramEventMediaTools(owner)
			arguments := eventMediaArguments(t, ToolProgramEventMediaAdd)
			arguments["role"], _ = json.Marshal(role)
			_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolProgramEventMediaAdd, arguments)
			require.NoError(t, err)
			require.Equal(t, role, owner.request.(programevent.AddProgramEventMediaCommand).Role)
		})
	}
}

func TestProgramEventMediaListProjectsNativeOrderAndFeedsCompleteReorder(t *testing.T) {
	owner := newRecordingEventMedia()
	owner.event.Document = &contentv1.RichTextDocument{}
	owner.event.Title = "body and title excluded"
	owner.event.Locales = []programevent.LocaleView{{Locale: "ko", ContentHTML: "private body"}}
	owner.event.Artists = []programevent.Relation{{ID: eventMediaPeerID}}
	owner.event.Media = []programevent.Media{
		{ID: eventMediaPeerID, FileID: eventMediaFileID, Role: "gallery", SortOrder: 7, IsPrimary: true, Caption: proto.String(""), CreatedAt: owner.event.UpdatedAt, UpdatedAt: owner.event.UpdatedAt},
		{ID: eventMediaID, FileID: eventMediaPeerID, Role: "gallery", SortOrder: 2, Alt: proto.String("alt")},
	}
	tools, _ := NewProgramEventMediaTools(owner)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolProgramEventMediaList, eventMediaArguments(t, ToolProgramEventMediaList))
	require.NoError(t, err)
	require.Equal(t, programevent.GetProgramEventQuery{ID: eventMediaDocumentID}, owner.request)
	output := result.StructuredContent
	require.Len(t, output, 5, "body, title and hydrated relations must not enter media projection")
	require.Equal(t, "ko", output["source_locale"])
	require.Equal(t, "2026-10-05T00:00:00Z", output["updated_at"])
	media := output["media"].([]any)
	require.Len(t, media, 2)
	first := media[0].(map[string]any)
	require.Equal(t, eventMediaPeerID, first["id"])
	require.Equal(t, eventMediaFileID, first["file_id"])
	require.Equal(t, float64(7), first["sort_order"])
	require.Equal(t, true, first["is_primary"])
	require.Equal(t, "", first["caption"])
	require.NotContains(t, first, "alt", "absent native optional text must remain absent")
	require.Equal(t, "2026-10-05T00:00:00Z", first["created_at"])
	second := media[1].(map[string]any)
	require.Equal(t, "alt", second["alt"])
	require.NotContains(t, second, "caption")
	arguments := eventMediaArguments(t, ToolProgramEventMediaReorder)
	arguments["role"] = json.RawMessage(`"gallery"`)
	arguments["media_ids"], _ = json.Marshal([]string{second["id"].(string), first["id"].(string)})
	_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolProgramEventMediaReorder, arguments)
	require.NoError(t, err)
	require.Equal(t, programevent.ReorderProgramEventMediaCommand{EventID: eventMediaDocumentID, Role: "gallery", MediaIDs: []string{eventMediaID, eventMediaPeerID}}, owner.request)
	require.Equal(t, []string{"get", "reorder"}, owner.calls)
}

func TestProgramEventMediaAddPreservesNullableTextInputAndNativeResultWithoutRead(t *testing.T) {
	for _, test := range []struct {
		name, values string
		alt, caption *string
		primary      bool
	}{
		{name: "omitted native clear"},
		{name: "explicit empty native clear", values: `,"alt":"","caption":""`, alt: proto.String(""), caption: proto.String("")},
		{name: "upsert both texts and primary", values: `,"alt":"photo","caption":"caption","make_primary":true`, alt: proto.String("photo"), caption: proto.String("caption"), primary: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner := newRecordingEventMedia()
			owner.getErr = errors.New("reading after a committed mutation is unavailable")
			tools, _ := NewProgramEventMediaTools(owner)
			arguments := toolArguments(t, `{"document_id":"`+eventMediaDocumentID+`","file_id":"`+eventMediaFileID+`"`+test.values+`}`)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolProgramEventMediaAdd, arguments)
			require.NoError(t, err)
			require.Equal(t, programevent.AddProgramEventMediaCommand{EventID: eventMediaDocumentID, FileID: eventMediaFileID, Role: "poster", Alt: test.alt, Caption: test.caption, MakePrimary: test.primary}, owner.request)
			require.Equal(t, []string{"add"}, owner.calls)
			require.Equal(t, true, result.StructuredContent["changed"])
			require.Equal(t, owner.add.Media.ID, result.StructuredContent["media"].(map[string]any)["id"])
			assertEventMediaReadRecipe(t, result)
		})
	}
}

func TestProgramEventMediaRemoveAndReorderUseNativeResultsWithoutRead(t *testing.T) {
	for _, name := range []string{ToolProgramEventMediaRemove, ToolProgramEventMediaReorder} {
		t.Run(name, func(t *testing.T) {
			owner := newRecordingEventMedia()
			owner.getErr = errors.New("post-write read unavailable")
			tools, _ := NewProgramEventMediaTools(owner)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, name, eventMediaArguments(t, name))
			require.NoError(t, err)
			if name == ToolProgramEventMediaRemove {
				require.Equal(t, programevent.DeleteProgramEventMediaCommand{EventID: eventMediaDocumentID, MediaID: eventMediaID}, owner.request)
				require.Equal(t, eventMediaID, result.StructuredContent["media_id"])
				require.Equal(t, true, result.StructuredContent["changed"])
			} else {
				require.Equal(t, programevent.ReorderProgramEventMediaCommand{EventID: eventMediaDocumentID, Role: "poster", MediaIDs: []string{eventMediaID, eventMediaPeerID}}, owner.request)
				require.Equal(t, false, result.StructuredContent["changed"], "native no-op must survive")
				require.Equal(t, []any{eventMediaID, eventMediaPeerID}, result.StructuredContent["media_ids"])
			}
			require.Len(t, owner.calls, 1)
			assertEventMediaReadRecipe(t, result)
		})
	}
	owner := newRecordingEventMedia()
	tools, _ := NewProgramEventMediaTools(owner)
	arguments := eventMediaArguments(t, ToolProgramEventMediaReorder)
	arguments["media_ids"] = json.RawMessage(`[]`)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolProgramEventMediaReorder, arguments)
	require.NoError(t, err)
	require.Empty(t, owner.request.(programevent.ReorderProgramEventMediaCommand).MediaIDs)
	require.Equal(t, []any{}, result.StructuredContent["media_ids"])
}

func TestProgramEventMediaRejectsInvalidArgumentsBeforeOwner(t *testing.T) {
	for _, test := range []struct{ name, tool, field, raw string }{
		{"unknown field", ToolProgramEventMediaList, "locale", `"en"`},
		{"invalid document ID", ToolProgramEventMediaList, "document_id", `"event"`},
		{"null file ID", ToolProgramEventMediaAdd, "file_id", `null`},
		{"invalid media ID", ToolProgramEventMediaRemove, "media_id", `"media"`},
		{"null role", ToolProgramEventMediaAdd, "role", `null`},
		{"empty role", ToolProgramEventMediaAdd, "role", `""`},
		{"unknown role", ToolProgramEventMediaReorder, "role", `"other"`},
		{"null alt", ToolProgramEventMediaAdd, "alt", `null`},
		{"null caption", ToolProgramEventMediaAdd, "caption", `null`},
		{"null primary", ToolProgramEventMediaAdd, "make_primary", `null`},
		{"wrong primary type", ToolProgramEventMediaAdd, "make_primary", `"yes"`},
		{"missing IDs", ToolProgramEventMediaReorder, "media_ids", ``},
		{"null IDs", ToolProgramEventMediaReorder, "media_ids", `null`},
		{"invalid ID item", ToolProgramEventMediaReorder, "media_ids", `[null]`},
		{"duplicate IDs", ToolProgramEventMediaReorder, "media_ids", `["` + eventMediaID + `","` + eventMediaID + `"]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner := newRecordingEventMedia()
			tools, _ := NewProgramEventMediaTools(owner)
			arguments := eventMediaArguments(t, test.tool)
			if test.raw == "" {
				delete(arguments, test.field)
			} else {
				arguments[test.field] = json.RawMessage(test.raw)
			}
			_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, test.tool, arguments)
			var invalid *mcpserver.ToolExecutionError
			require.ErrorAs(t, err, &invalid)
			require.Empty(t, owner.calls)
		})
	}
}

func TestProgramEventMediaPreservesOwningAuthorityAndErrors(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeUnauthenticated, connect.CodePermissionDenied, connect.CodeNotFound, connect.CodeInvalidArgument, connect.CodeFailedPrecondition, connect.CodeInternal} {
		for _, name := range []string{ToolProgramEventMediaList, ToolProgramEventMediaAdd, ToolProgramEventMediaRemove, ToolProgramEventMediaReorder} {
			t.Run(code.String()+"/"+name, func(t *testing.T) {
				owner := newRecordingEventMedia()
				owner.err = connect.NewError(code, errors.New("native owning decision"))
				tools, _ := NewProgramEventMediaTools(owner)
				ctx := context.WithValue(t.Context(), eventMediaContextKey{}, "authenticated actor")
				result, err := tools.CallTool(ctx, mcpserver.Principal{}, name, eventMediaArguments(t, name))
				require.Len(t, owner.calls, 1)
				require.Equal(t, "authenticated actor", owner.ctx.Value(eventMediaContextKey{}))
				require.Nil(t, result.StructuredContent)
				if code == connect.CodeInternal {
					require.Same(t, owner.err, err)
				} else {
					var safe *mcpserver.ToolExecutionError
					require.ErrorAs(t, err, &safe)
					require.Equal(t, "native owning decision", safe.Message)
				}
			})
		}
	}
	owner := newRecordingEventMedia()
	tools, _ := NewProgramEventMediaTools(owner)
	_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, "program_event_media_update", nil)
	require.ErrorIs(t, err, mcpserver.ErrUnknownTool)
	require.Empty(t, owner.calls)
}

func eventMediaArguments(t *testing.T, name string) mcpserver.ToolArguments {
	t.Helper()
	values := `"document_id":"` + eventMediaDocumentID + `"`
	switch name {
	case ToolProgramEventMediaAdd:
		values += `,"file_id":"` + eventMediaFileID + `"`
	case ToolProgramEventMediaRemove:
		values += `,"media_id":"` + eventMediaID + `"`
	case ToolProgramEventMediaReorder:
		values += `,"media_ids":["` + eventMediaID + `","` + eventMediaPeerID + `"]`
	}
	return toolArguments(t, `{`+values+`}`)
}

func assertEventMediaReadRecipe(t *testing.T, result mcpserver.ToolResult) {
	t.Helper()
	require.Equal(t, "2026-10-05T00:00:00Z", result.StructuredContent["updated_at"])
	require.Equal(t, map[string]any{"tool": ToolProgramEventMediaList, "arguments": map[string]any{"document_id": eventMediaDocumentID}}, result.StructuredContent["next_read"])
}

type eventMediaContextKey struct{}
type recordingEventMedia struct {
	event       *programevent.EventView
	add         programevent.MediaChange
	remove      programevent.MediaChange
	reorder     programevent.MediaOrder
	request     any
	ctx         context.Context
	calls       []string
	err, getErr error
}

func newRecordingEventMedia() *recordingEventMedia {
	at := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	return &recordingEventMedia{
		event:   &programevent.EventView{Event: programevent.Event{ID: eventMediaDocumentID, SourceLocale: "ko", UpdatedAt: at}},
		add:     programevent.MediaChange{EventID: eventMediaDocumentID, Changed: true, UpdatedAt: at, Media: &programevent.Media{ID: eventMediaID, FileID: eventMediaFileID, Role: "poster", IsPrimary: true}},
		remove:  programevent.MediaChange{EventID: eventMediaDocumentID, Changed: true, UpdatedAt: at},
		reorder: programevent.MediaOrder{Role: "poster", UpdatedAt: at},
	}
}
func (owner *recordingEventMedia) record(ctx context.Context, name string, request any) {
	owner.ctx, owner.request = ctx, request
	owner.calls = append(owner.calls, name)
}
func (owner *recordingEventMedia) GetProgramEvent(ctx context.Context, query programevent.GetProgramEventQuery) (programevent.EventView, error) {
	owner.record(ctx, "get", query)
	if owner.getErr != nil {
		return programevent.EventView{}, owner.getErr
	}
	if owner.err != nil {
		return programevent.EventView{}, owner.err
	}
	return *owner.event, nil
}
func (owner *recordingEventMedia) AddProgramEventMedia(ctx context.Context, command programevent.AddProgramEventMediaCommand) (programevent.MediaChange, error) {
	owner.record(ctx, "add", command)
	return owner.add, owner.err
}
func (owner *recordingEventMedia) DeleteProgramEventMedia(ctx context.Context, command programevent.DeleteProgramEventMediaCommand) (programevent.MediaChange, error) {
	owner.record(ctx, "remove", command)
	return owner.remove, owner.err
}
func (owner *recordingEventMedia) ReorderProgramEventMedia(ctx context.Context, command programevent.ReorderProgramEventMediaCommand) (programevent.MediaOrder, error) {
	owner.record(ctx, "reorder", command)
	return owner.reorder, owner.err
}

var _ ProgramEventMediaApplication = (*programevent.Service)(nil)
