package mcp

import (
	"encoding/json"
	"errors"
	"fmt"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

const syncRequiredGuidance = " On sync_required nothing changed: run the returned document_read recipe (it rereads the blocks the edit targeted), compare the latest values with the intended edit, and retry with the new revisions."

// documentSyncResult is a routine recovery control result, never an applied change.
type documentSyncResult struct {
	Status                  string         `json:"status"`
	Reason                  string         `json:"reason"`
	Applied                 bool           `json:"applied"`
	CurrentDocumentRevision core.Revision  `json:"current_document_revision"`
	CurrentTargetRevision   *core.Revision `json:"current_target_revision"`
	AffectedHandles         []string       `json:"affected_handles"`
	DiscardPreviousPages    bool           `json:"discard_previous_pages"`
	Read                    syncReadRecipe `json:"read"`
	Instructions            string         `json:"instructions"`
}

type syncReadRecipe struct {
	Tool      string        `json:"tool"`
	Arguments readArguments `json:"arguments"`
}

func syncRequiredResult(reason string, documentRevision core.Revision, targetRevision *core.Revision, handles []string, input readArguments, discardPages bool) (mcpserver.ToolResult, error) {
	if documentRevision == "" {
		return mcpserver.ToolResult{}, errors.New("application returned sync recovery without current document revision")
	}
	input.Cursor = ""
	instructions := "No change was applied. Follow the document_read recipe, then reread the latest target values and compare them with the previous read and intended edit. Preserve concurrent values when preparing a new edit; never blindly replace revisions and replay the old mutation. Affected handles identify pending operation targets, not values known to have changed. Revisions do not identify who changed the document."
	if discardPages {
		instructions = "Discard all previous pages and restart the read with the returned recipe; do not combine pages from different revisions. " + instructions
	}
	encoded, err := json.Marshal(documentSyncResult{
		Status: "sync_required", Reason: reason, Applied: false,
		CurrentDocumentRevision: documentRevision, CurrentTargetRevision: targetRevision,
		AffectedHandles: append([]string{}, handles...), DiscardPreviousPages: discardPages,
		Read: syncReadRecipe{Tool: ToolDocumentRead, Arguments: input}, Instructions: instructions,
	})
	if err != nil {
		return mcpserver.ToolResult{}, fmt.Errorf("encode MCP document sync recovery: %w", err)
	}
	return structuredResult(encoded, false)
}

func mutationConflict(err error) *core.Conflict {
	var conflictError *core.ConflictError
	if errors.As(err, &conflictError) {
		return &conflictError.Conflict
	}
	var validationError *core.ValidationError
	if errors.As(err, &validationError) {
		return validationError.Result.Conflict
	}
	return nil
}

func mutationSyncResult(request core.ApplyRequest, conflict *core.Conflict) (mcpserver.ToolResult, error) {
	var reason string
	switch conflict.Code {
	case core.ConflictDocumentRevision:
		reason = "document_revision_changed"
	case core.ConflictTargetRevision:
		reason = "target_revision_changed"
	default:
		return mcpserver.ToolResult{}, fmt.Errorf("application returned unsupported conflict code %q", conflict.Code)
	}
	// Reread only the existing blocks the edit targeted; a batch that only
	// inserts blocks needs the outline to find its parents again.
	recipe := readArguments{DocumentType: request.Profile, DocumentID: request.Document, Locale: request.Locale, Mode: core.ReadOutline, Limit: core.DefaultOutlineReadLimit}
	if blocks := core.ExistingTargetBlocks(request.Operations); len(blocks) > 0 && len(blocks) <= core.DefaultOutlineReadLimit {
		recipe.Mode, recipe.Blocks, recipe.Limit = core.ReadBlocks, blocks, len(blocks)
	}
	return syncRequiredResult(reason, conflict.CurrentDocumentRevision, conflict.CurrentTargetRevision, conflict.AffectedHandles, recipe, false)
}

// outputSchemaWithSync keeps recursive success references rooted in the combined
// schema instead of relocating $defs underneath the success alternative.
func outputSchemaWithSync(successJSON string) json.RawMessage {
	var success map[string]json.RawMessage
	if err := json.Unmarshal([]byte(successJSON), &success); err != nil {
		panic(fmt.Sprintf("invalid static document output schema: %v", err))
	}
	definitions := success["$defs"]
	delete(success, "$defs")
	encodedSuccess, err := json.Marshal(success)
	if err != nil {
		panic(fmt.Sprintf("encode static document output schema: %v", err))
	}
	combined := map[string]any{
		"type":  "object",
		"oneOf": []json.RawMessage{encodedSuccess, json.RawMessage(documentSyncOutputJSONSchema)},
	}
	if definitions != nil {
		combined["$defs"] = definitions
	}
	encoded, err := json.Marshal(combined)
	if err != nil {
		panic(fmt.Sprintf("encode static document sync output schema: %v", err))
	}
	return encoded
}
