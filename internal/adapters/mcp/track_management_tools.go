package mcp

import (
	"context"
	"errors"
	"fmt"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	releasedomain "github.com/echovisionlab/geul-api/internal/release"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	ToolTrackList           = "track_list"
	ToolTrackCreate         = "track_create"
	ToolTrackSettingsUpdate = "track_settings_update"
	ToolTrackDelete         = "track_delete"
	ToolTrackCreditsSet     = "track_credits_set"
	ToolTrackReorder        = "track_reorder"
)

var trackManagementTools = toolRegistry[TrackManagementTools]{
	defineTool(oauthTool(ToolTrackList, "List Release tracks", "Read all authorized tracks in one Release, their editable settings, credits, and observed credit snapshots. Pass a returned observed snapshot unchanged to track_credits_set. Attach original audio with file_transfer k=track_audio, track_id, and the current audio_original_file_id as expected_current_file_id; omit that expectation when no original is attached. Direct audio requires browser-prepared derivatives before completion. Clear audio with track_settings_update clear_audio_original.", trackListInputJSONSchema, trackListOutputJSONSchema, true, false), func(tools *TrackManagementTools, ctx context.Context, name string, call toolCall[trackListArguments]) (mcpserver.ToolResult, error) {
		var input trackListArguments
		if err := call.Decode(&input); err != nil {
			return executionError(err)
		}
		if err := validateUUID("release_id", input.ReleaseID); err != nil {
			return executionError(err)
		}
		tracks, err := tools.application.ListTracksByRelease(ctx, releasedomain.ListTracksQuery{ReleaseID: input.ReleaseID})
		if err != nil {
			return expectedToolError(err)
		}
		return contentResult(trackListOutput(releasedomain.TracksWithCreditsMessage(ctx, tracks)))
	}),
	defineTool(oauthTool(ToolTrackCreate, "Create Track", "Append a Track to an active Release. The server assigns its track_number. Set title and optional duration or lyrics; edit credits with track_credits_set after track_list.", trackCreateInputJSONSchema, trackSettingsOutputJSONSchema, false, false), (*TrackManagementTools).create),
	defineTool(oauthTool(ToolTrackSettingsUpdate, "Update Track settings", "Update supplied Track settings through the owning Release service. Omitted values remain unchanged; clear_duration and clear_lyrics take precedence over supplied values. clear_audio_original removes the original audio association and disables its downloads. Use track_reorder for a complete Release ordering. An empty title leaves the existing title unchanged.", trackSettingsUpdateInputJSONSchema, trackSettingsOutputJSONSchema, false, true), (*TrackManagementTools).update),
	defineTool(oauthTool(ToolTrackDelete, "Delete Track", "Delete a Track through the owning Release authority, including existing audio-upload cleanup. A finalizing upload must finish before deletion can succeed.", trackIDInputJSONSchema, trackDeleteOutputJSONSchema, false, true), func(tools *TrackManagementTools, ctx context.Context, name string, call toolCall[trackDeleteArguments]) (mcpserver.ToolResult, error) {
		var input trackDeleteArguments
		if err := call.Decode(&input); err != nil {
			return executionError(err)
		}
		if err := validateUUID("track_id", input.TrackID); err != nil {
			return executionError(err)
		}
		if err := tools.application.DeleteTrack(ctx, releasedomain.DeleteTrackCommand{ID: input.TrackID}); err != nil {
			return expectedToolError(err)
		}
		return contentResult(map[string]any{"success": true})
	}),
	defineTool(oauthTool(ToolTrackCreditsSet, "Set Track credits", "Apply desired credits against the required observed TrackCreditsSnapshot returned by track_list or track_credits_set. Preserve existing credit IDs; omit IDs for new credits. An empty desired credits array removes observed credits while preserving concurrent additions. The owning service determines new credit order from the desired array and preserves existing order.", trackCreditsSetInputJSONSchema, trackWithCreditsOutputJSONSchema, false, true), (*TrackManagementTools).setCredits),
	defineTool(oauthTool(ToolTrackReorder, "Reorder Release tracks", "Supply every Track ID in one Release exactly once, in the desired order. Resolve the complete current list with track_list; partial or mixed-Release reorders are rejected by the owning Release service.", trackReorderInputJSONSchema, trackListOutputJSONSchema, false, true), (*TrackManagementTools).reorder),
}

type TrackManagementApplication interface {
	ListTracksByRelease(context.Context, releasedomain.ListTracksQuery) ([]releasedomain.TrackWithCredits, error)
	CreateTrack(context.Context, releasedomain.CreateTrackCommand) (releasedomain.Track, error)
	UpdateTrack(context.Context, releasedomain.UpdateTrackCommand) (releasedomain.Track, error)
	DeleteTrack(context.Context, releasedomain.DeleteTrackCommand) error
	SetTrackCredits(context.Context, releasedomain.SetTrackCreditsCommand) (releasedomain.TrackWithCredits, error)
	ReorderTracks(context.Context, releasedomain.ReorderTracksCommand) ([]releasedomain.TrackWithCredits, error)
}

type TrackManagementTools struct{ application TrackManagementApplication }

func NewTrackManagementTools(application TrackManagementApplication) (*TrackManagementTools, error) {
	if interfaceValueIsNil(application) {
		return nil, errors.New("MCP Track management application is required")
	}
	return &TrackManagementTools{application: application}, nil
}

func (*TrackManagementTools) ToolNames() []string { return trackManagementTools.names() }

func (*TrackManagementTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return trackManagementTools.list(), nil
}

func (tools *TrackManagementTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	return trackManagementTools.call(tools, ctx, name, arguments)
}

type trackCreateArguments struct {
	ReleaseID       string  `json:"release_id"`
	Title           string  `json:"title"`
	DurationSeconds *int32  `json:"duration_seconds,omitempty"`
	Lyrics          *string `json:"lyrics,omitempty"`
}

func (tools *TrackManagementTools) create(ctx context.Context, _ string, call toolCall[trackCreateArguments]) (mcpserver.ToolResult, error) {
	arguments := call.Arguments

	var input trackCreateArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if err := rejectNullArguments(arguments, "duration_seconds", "lyrics"); err != nil {
		return executionError(err)
	}
	if err := validateUUID("release_id", input.ReleaseID); err != nil {
		return executionError(err)
	}
	if input.Title == "" {
		return executionError(errors.New("title is required"))
	}
	track, err := tools.application.CreateTrack(ctx, releasedomain.CreateTrackCommand{
		ReleaseID: input.ReleaseID, Title: input.Title, DurationSeconds: input.DurationSeconds, Lyrics: input.Lyrics,
	})
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(trackSettingsOutput(releasedomain.TrackMessage(ctx, track)))
}

type trackSettingsArguments struct {
	TrackID            string  `json:"track_id"`
	TrackNumber        *int32  `json:"track_number,omitempty"`
	Title              *string `json:"title,omitempty"`
	DurationSeconds    *int32  `json:"duration_seconds,omitempty"`
	ProcessingStatus   *string `json:"processing_status,omitempty"`
	Lyrics             *string `json:"lyrics,omitempty"`
	ClearDuration      bool    `json:"clear_duration,omitempty"`
	ClearAudioOriginal bool    `json:"clear_audio_original,omitempty"`
	ClearLyrics        bool    `json:"clear_lyrics,omitempty"`
}

func (tools *TrackManagementTools) update(ctx context.Context, _ string, call toolCall[trackSettingsArguments]) (mcpserver.ToolResult, error) {
	arguments := call.Arguments

	var input trackSettingsArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if err := rejectNullArguments(arguments, "track_number", "title", "duration_seconds", "processing_status", "lyrics", "clear_duration", "clear_audio_original", "clear_lyrics"); err != nil {
		return executionError(err)
	}
	if err := validateUUID("track_id", input.TrackID); err != nil {
		return executionError(err)
	}
	if input.TrackNumber == nil && input.Title == nil && input.DurationSeconds == nil && input.ProcessingStatus == nil && input.Lyrics == nil && !input.ClearDuration && !input.ClearAudioOriginal && !input.ClearLyrics {
		return executionError(errors.New("at least one Track setting is required"))
	}
	track, err := tools.application.UpdateTrack(ctx, releasedomain.UpdateTrackCommand{
		ID: input.TrackID, TrackNumber: input.TrackNumber, Title: input.Title, DurationSeconds: input.DurationSeconds,
		ProcessingStatus: input.ProcessingStatus, Lyrics: input.Lyrics, ClearDuration: input.ClearDuration,
		ClearAudioOriginal: input.ClearAudioOriginal, ClearLyrics: input.ClearLyrics,
	})
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(trackSettingsOutput(releasedomain.TrackMessage(ctx, track)))
}

type trackCreditsSnapshotArguments struct {
	Credits *[]*managev1.TrackCreditInput `json:"credits"`
}

func (tools *TrackManagementTools) setCredits(ctx context.Context, _ string, call toolCall[trackSetCreditsArguments]) (mcpserver.ToolResult, error) {
	var input trackSetCreditsArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if err := validateUUID("track_id", input.TrackID); err != nil {
		return executionError(err)
	}
	if input.Credits == nil || input.Observed == nil || input.Observed.Credits == nil {
		return executionError(errors.New("credits and observed.credits arrays are required; copy observed from track_list"))
	}
	for name, credits := range map[string][]*managev1.TrackCreditInput{"credits": *input.Credits, "observed.credits": *input.Observed.Credits} {
		for index, credit := range credits {
			if credit == nil {
				return executionError(fmt.Errorf("%s[%d] cannot be null", name, index))
			}
			for field, value := range map[string]*string{"id": credit.Id, "artist_id": credit.ArtistId, "member_id": credit.MemberId} {
				if value != nil {
					if err := validateUUID(fmt.Sprintf("%s[%d].%s", name, index, field), *value); err != nil {
						return executionError(err)
					}
				}
			}
		}
	}
	track, err := tools.application.SetTrackCredits(ctx, releasedomain.SetTrackCreditsCommand{
		TrackID: input.TrackID, Credits: releasedomain.TrackCreditInputsFromProto(*input.Credits),
		Observed: &releasedomain.Snapshot[*releasedomain.CreditInput]{Items: releasedomain.TrackCreditInputsFromProto(*input.Observed.Credits)},
	})
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(trackWithCreditsOutput(releasedomain.TrackWithCreditsMessage(ctx, track)))
}

func (tools *TrackManagementTools) reorder(ctx context.Context, _ string, call toolCall[trackReorderArguments]) (mcpserver.ToolResult, error) {
	var input trackReorderArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if len(input.TrackIDs) == 0 {
		return executionError(errors.New("track_ids must contain every Track in one Release"))
	}
	seen := make(map[string]bool, len(input.TrackIDs))
	for _, id := range input.TrackIDs {
		if err := validateUUID("track_ids", id); err != nil {
			return executionError(err)
		}
		if seen[id] {
			return executionError(errors.New("track_ids must not contain duplicates"))
		}
		seen[id] = true
	}
	tracks, err := tools.application.ReorderTracks(ctx, releasedomain.ReorderTracksCommand{TrackIDs: input.TrackIDs})
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(trackListOutput(releasedomain.TracksWithCreditsMessage(ctx, tracks)))
}

func trackSettingsOutput(track *managev1.Track) map[string]any {
	return map[string]any{
		"track_id": track.Id, "release_id": track.ReleaseId, "track_number": track.TrackNumber, "title": track.Title,
		"duration_seconds": track.DurationSeconds, "processing_status": track.ProcessingStatus, "lyrics": track.Lyrics,
		"audio_original_file_id": track.AudioOriginalFileId,
	}
}

func trackWithCreditsOutput(value *managev1.TrackWithCredits) map[string]any {
	output := trackSettingsOutput(value.Track)
	credits := make([]map[string]any, 0, len(value.Credits))
	for _, credit := range value.Credits {
		credits = append(credits, map[string]any{
			"id": credit.Id, "artist_id": credit.ArtistId, "member_id": credit.MemberId,
			"credited_name": credit.CreditedName, "credit_role": credit.CreditRole, "sort_order": credit.SortOrder,
		})
	}
	output["credits"] = credits
	output["observed"] = map[string]any{"credits": credits}
	return output
}

func trackListOutput(values []*managev1.TrackWithCredits) map[string]any {
	tracks := make([]map[string]any, 0, len(values))
	for _, value := range values {
		tracks = append(tracks, trackWithCreditsOutput(value))
	}
	return map[string]any{"tracks": tracks}
}

type trackSetCreditsArguments = struct {
	TrackID  string                         `json:"track_id"`
	Credits  *[]*managev1.TrackCreditInput  `json:"credits"`
	Observed *trackCreditsSnapshotArguments `json:"observed"`
}

type trackReorderArguments = struct {
	TrackIDs []string `json:"track_ids"`
}

type trackListArguments = struct {
	ReleaseID string `json:"release_id"`
}

type trackDeleteArguments = struct {
	TrackID string `json:"track_id"`
}
