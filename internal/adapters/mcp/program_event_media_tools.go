package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/programevent"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	ToolProgramEventMediaList    = "program_event_media_list"
	ToolProgramEventMediaAdd     = "program_event_media_add"
	ToolProgramEventMediaRemove  = "program_event_media_remove"
	ToolProgramEventMediaReorder = "program_event_media_reorder"
)

var programEventMediaTools = []mcpserver.Tool{
	oauthTool(ToolProgramEventMediaList, "List Program Event media", "Read all authorized event media, native role/order/primary state, File identities, and shared alt/caption values. No locale fallback or body is returned. Use the complete current IDs for one role when calling program_event_media_reorder; updated_at is an observation, not a media CAS token.", contentIDInputJSONSchema, programEventMediaListOutputJSONSchema, true, false),
	oauthTool(ToolProgramEventMediaAdd, "Add Program Event media", "Attach an existing File through the native event media service. The role defaults to poster. Reusing the same event/role/file updates that row's shared alt/caption; both omitted and empty alt/caption clear the native nullable values. Send both existing texts to preserve one while editing the other. make_primary selects the role's primary media; the native service selects the first primary automatically. This does not edit a target-locale title or caption. Read program_event_media_list afterward to see the whole collection.", programEventMediaAddInputJSONSchema, programEventMediaMutationOutputJSONSchema, false, true),
	oauthTool(ToolProgramEventMediaRemove, "Remove Program Event media", "Remove the media row returned by program_event_media_list through the existing event authority. This detaches its File relation without deleting the File. The native service promotes a replacement primary if needed. Read program_event_media_list afterward to see the resulting collection.", programEventMediaRemoveInputJSONSchema, programEventMediaMutationOutputJSONSchema, false, true),
	oauthTool(ToolProgramEventMediaReorder, "Reorder Program Event media", "Supply every media row ID for one event and role exactly once, in desired order. Use IDs from the complete program_event_media_list; partial or mixed-event/role lists are rejected by the native service. When the order changes, the first item becomes primary; unchanged order retains the existing primary. Empty is accepted only for an empty role. Read program_event_media_list afterward for the resulting collection.", programEventMediaReorderInputJSONSchema, programEventMediaMutationOutputJSONSchema, false, true),
}

// ProgramEventMediaApplication is the exact native owning service boundary.
// Authorization, File attachment validation and transactional ordering stay there.
type ProgramEventMediaApplication interface {
	GetProgramEvent(context.Context, programevent.GetProgramEventQuery) (*managev1.ProgramEvent, error)
	AddProgramEventMedia(context.Context, programevent.AddProgramEventMediaCommand) (programevent.MediaChange, error)
	DeleteProgramEventMedia(context.Context, programevent.DeleteProgramEventMediaCommand) (programevent.MediaChange, error)
	ReorderProgramEventMedia(context.Context, programevent.ReorderProgramEventMediaCommand) (programevent.MediaOrder, error)
}

type ProgramEventMediaTools struct{ application ProgramEventMediaApplication }

func NewProgramEventMediaTools(application ProgramEventMediaApplication) (*ProgramEventMediaTools, error) {
	if interfaceValueIsNil(application) {
		return nil, errors.New("MCP Program Event media application is required")
	}
	return &ProgramEventMediaTools{application: application}, nil
}
func (*ProgramEventMediaTools) ToolNames() []string {
	return toolDefinitionNames(programEventMediaTools)
}
func (*ProgramEventMediaTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return cloneToolDefinitions(programEventMediaTools), nil
}

func (tools *ProgramEventMediaTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	switch name {
	case ToolProgramEventMediaList:
		var input contentIDArguments
		if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		if err := validateUUID("document_id", input.DocumentID); err != nil {
			return executionError(err)
		}
		event, err := tools.application.GetProgramEvent(ctx, programevent.GetProgramEventQuery{ID: input.DocumentID})
		if err != nil {
			return expectedToolError(err)
		}
		media := make([]map[string]any, 0, len(event.Media))
		for _, item := range event.Media {
			if item != nil {
				media = append(media, programEventMediaOutput(item))
			}
		}
		return contentResult(map[string]any{"document_type": "program_event", "document_id": event.Id, "source_locale": event.SourceLocale, "updated_at": timestampString(event.UpdatedAt), "media": media})
	case ToolProgramEventMediaAdd:
		var input struct {
			DocumentID  string  `json:"document_id"`
			FileID      string  `json:"file_id"`
			Role        string  `json:"role,omitempty"`
			Alt         *string `json:"alt,omitempty"`
			Caption     *string `json:"caption,omitempty"`
			MakePrimary bool    `json:"make_primary,omitempty"`
		}
		if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		if err := rejectNullArguments(arguments, "role", "alt", "caption", "make_primary"); err != nil {
			return executionError(err)
		}
		if err := validateUUID("document_id", input.DocumentID); err != nil {
			return executionError(err)
		}
		if err := validateUUID("file_id", input.FileID); err != nil {
			return executionError(err)
		}
		role, err := programEventMediaRole(arguments, input.Role)
		if err != nil {
			return executionError(err)
		}
		change, err := tools.application.AddProgramEventMedia(ctx, programevent.AddProgramEventMediaCommand{EventID: input.DocumentID, FileID: input.FileID, Role: role, Alt: input.Alt, Caption: input.Caption, MakePrimary: input.MakePrimary})
		if err != nil {
			return expectedToolError(err)
		}
		output := programEventMediaMutationOutput(input.DocumentID, change.Changed, change.UpdatedAt)
		output["media"] = programEventMediaOutput(change.Media)
		return contentResult(output)
	case ToolProgramEventMediaRemove:
		var input struct {
			DocumentID string `json:"document_id"`
			MediaID    string `json:"media_id"`
		}
		if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		if err := validateUUID("document_id", input.DocumentID); err != nil {
			return executionError(err)
		}
		if err := validateUUID("media_id", input.MediaID); err != nil {
			return executionError(err)
		}
		change, err := tools.application.DeleteProgramEventMedia(ctx, programevent.DeleteProgramEventMediaCommand{EventID: input.DocumentID, MediaID: input.MediaID})
		if err != nil {
			return expectedToolError(err)
		}
		output := programEventMediaMutationOutput(input.DocumentID, change.Changed, change.UpdatedAt)
		output["media_id"] = input.MediaID
		return contentResult(output)
	case ToolProgramEventMediaReorder:
		var input struct {
			DocumentID string   `json:"document_id"`
			Role       string   `json:"role,omitempty"`
			MediaIDs   []string `json:"media_ids"`
		}
		if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		if err := rejectNullArguments(arguments, "role", "media_ids"); err != nil {
			return executionError(err)
		}
		if err := validateUUID("document_id", input.DocumentID); err != nil {
			return executionError(err)
		}
		if input.MediaIDs == nil {
			return executionError(errors.New("media_ids is required; supply the complete role ordering"))
		}
		role, err := programEventMediaRole(arguments, input.Role)
		if err != nil {
			return executionError(err)
		}
		seen := make(map[string]struct{}, len(input.MediaIDs))
		for _, id := range input.MediaIDs {
			if err := validateUUID("media_ids", id); err != nil {
				return executionError(err)
			}
			if _, exists := seen[id]; exists {
				return executionError(errors.New("media_ids must contain unique media IDs"))
			}
			seen[id] = struct{}{}
		}
		order, err := tools.application.ReorderProgramEventMedia(ctx, programevent.ReorderProgramEventMediaCommand{EventID: input.DocumentID, Role: role, MediaIDs: input.MediaIDs})
		if err != nil {
			return expectedToolError(err)
		}
		output := programEventMediaMutationOutput(input.DocumentID, order.Changed, order.UpdatedAt)
		output["role"] = order.Role
		output["media_ids"] = append([]string{}, input.MediaIDs...)
		return contentResult(output)
	default:
		return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
	}
}

func programEventMediaRole(arguments mcpserver.ToolArguments, role string) (string, error) {
	if _, supplied := arguments["role"]; !supplied {
		return "poster", nil
	}
	switch role {
	case "poster", "gallery", "lineup", "sponsor", "social", "venue":
		return role, nil
	}
	return "", fmt.Errorf("unsupported Program Event media role %q", role)
}

func programEventMediaOutput(media *managev1.ProgramEventMedia) map[string]any {
	output := map[string]any{"id": media.Id, "file_id": media.FileId, "role": media.Role, "sort_order": media.SortOrder, "is_primary": media.IsPrimary}
	if media.Alt != nil {
		output["alt"] = *media.Alt
	}
	if media.Caption != nil {
		output["caption"] = *media.Caption
	}
	if media.CreatedAt != nil {
		output["created_at"] = timestampString(media.CreatedAt)
	}
	if media.UpdatedAt != nil {
		output["updated_at"] = timestampString(media.UpdatedAt)
	}
	return output
}

func programEventMediaMutationOutput(eventID string, changed bool, updatedAt time.Time) map[string]any {
	return map[string]any{"document_type": "program_event", "document_id": eventID, "changed": changed, "updated_at": timestampString(timestamppb.New(updatedAt)), "next_read": map[string]any{"tool": ToolProgramEventMediaList, "arguments": map[string]any{"document_id": eventID}}}
}

var _ ToolProvider = (*ProgramEventMediaTools)(nil)
