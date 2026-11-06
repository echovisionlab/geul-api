package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/google/uuid"
)

type headingArguments struct {
	focusedMutationArguments
	Parent    core.BlockID `json:"parent_block_id,omitempty"`
	After     core.BlockID `json:"after_block_id,omitempty"`
	Block     core.BlockID `json:"block_id,omitempty"`
	Text      *string      `json:"text"`
	Level     *int         `json:"level,omitempty"`
	Alignment *string      `json:"alignment,omitempty"`
}

func validateRichBlockProfile(input focusedMutationArguments, parent core.BlockID, create bool) error {
	if input.DocumentType != core.DomainPage && input.DocumentType != core.DomainPost {
		return errors.New("document_type must be page or post")
	}
	if create && input.DocumentType == core.DomainPage && parent == "" {
		return errors.New("Page rich blocks require parent_block_id identifying a rich-text section")
	}
	if input.DocumentType == core.DomainPost && parent != "" {
		return errors.New("Post body blocks must omit parent_block_id")
	}
	return nil
}

func (tools *AIDocumentTools) createHeading(ctx context.Context, call toolCall[headingArguments]) (mcpserver.ToolResult, error) {
	return tools.writeHeading(ctx, call, false)
}

func (tools *AIDocumentTools) updateHeading(ctx context.Context, call toolCall[headingArguments]) (mcpserver.ToolResult, error) {
	return tools.writeHeading(ctx, call, true)
}

func (tools *AIDocumentTools) writeHeading(ctx context.Context, call toolCall[headingArguments], update bool) (mcpserver.ToolResult, error) {
	arguments := call.Arguments

	var input headingArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if err := rejectNullArguments(arguments, "parent_block_id", "after_block_id", "alignment", "level"); err != nil {
		return executionError(err)
	}
	if input.Text == nil {
		return executionError(errors.New("text is required and must be a string"))
	}
	if input.Level == nil && !update {
		return executionError(errors.New("level is required"))
	}
	if input.Level != nil && (*input.Level < 1 || *input.Level > 3) {
		return executionError(errors.New("level must be 1, 2, or 3"))
	}
	if input.Alignment != nil && *input.Alignment != "left" && *input.Alignment != "center" && *input.Alignment != "right" {
		return executionError(errors.New("alignment must be left, center, or right"))
	}
	if err := validateRichBlockProfile(input.focusedMutationArguments, input.Parent, !update); err != nil {
		return executionError(err)
	}
	var operations []core.Operation
	created := core.BlockID("")
	if update {
		if input.Block == "" {
			return executionError(errors.New("block_id is required"))
		}
		if _, ok := arguments["parent_block_id"]; ok {
			return executionError(errors.New("heading_update does not accept parent_block_id"))
		}
		if _, ok := arguments["after_block_id"]; ok {
			return executionError(errors.New("heading_update does not accept after_block_id"))
		}
		if err := tools.rejectFormattedReplacement(ctx, input.focusedMutationArguments, input.Block); err != nil {
			return expectedToolError(err)
		}
	} else {
		if _, ok := arguments["block_id"]; ok {
			return executionError(errors.New("heading_create assigns block_id"))
		}
		created = core.BlockID(uuid.NewString())
		input.Block = created
		operations = append(operations, core.InsertBlockOperation(created, "heading", input.Parent, input.After))
	}
	operations = append(operations, core.SetFieldOperation(input.Block, "content", core.RichText(core.InlineText(*input.Text))))
	if input.Level != nil {
		operations = append(operations, core.SetFieldOperation(input.Block, "level", core.Number(strconv.Itoa(*input.Level))))
	}
	if input.Alignment != nil {
		operations = append(operations, core.SetFieldOperation(input.Block, "textAlignment", core.Text(*input.Alignment)))
	}
	focused := input.focusedMutationArguments
	if !update {
		var err error
		focused, err = withCurrentRevisions(ctx, tools.application, focused)
		if err != nil {
			return expectedToolError(err)
		}
	}
	request, err := focusedApplyRequest(focused, operations)
	if err != nil {
		return executionError(err)
	}
	return tools.applyRequest(ctx, request, created)
}

type tableCreateArguments struct {
	focusedMutationArguments
	Parent       core.BlockID `json:"parent_block_id,omitempty"`
	After        core.BlockID `json:"after_block_id,omitempty"`
	Rows         [][]string   `json:"rows"`
	HeaderRow    bool         `json:"header_row,omitempty"`
	HeaderColumn bool         `json:"header_column,omitempty"`
}

func (tools *AIDocumentTools) createTable(ctx context.Context, _ string, call toolCall[tableCreateArguments]) (mcpserver.ToolResult, error) {
	arguments := call.Arguments

	var input tableCreateArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if err := rejectNullArguments(arguments, "parent_block_id", "after_block_id", "header_row", "header_column", "rows"); err != nil {
		return executionError(err)
	}
	if err := validateRichBlockProfile(input.focusedMutationArguments, input.Parent, true); err != nil {
		return executionError(err)
	}
	if len(input.Rows) == 0 || len(input.Rows) > 100 || len(input.Rows[0]) == 0 || len(input.Rows[0]) > 100 {
		return executionError(errors.New("rows must contain 1..100 rows and 1..100 columns"))
	}
	for _, row := range input.Rows {
		if len(row) != len(input.Rows[0]) {
			return executionError(errors.New("rows must be rectangular"))
		}
	}
	// encoding/json accepts null for strings; reject it before it becomes empty text.
	var rawRows [][]json.RawMessage
	if err := json.Unmarshal(arguments["rows"], &rawRows); err != nil {
		return executionError(err)
	}
	for _, row := range rawRows {
		for _, cell := range row {
			if string(cell) == "null" {
				return executionError(errors.New("table cells must be strings"))
			}
		}
	}
	focused, err := withCurrentRevisions(ctx, tools.application, input.focusedMutationArguments)
	if err != nil {
		return expectedToolError(err)
	}
	block := core.BlockID(uuid.NewString())
	shared, localized := plainTableValues(input.Rows, input.HeaderRow, input.HeaderColumn)
	request, err := focusedApplyRequest(focused, []core.Operation{
		core.InsertBlockOperation(block, "table", input.Parent, input.After),
		core.SetFieldOperation(block, "table", shared),
		core.SetFieldOperation(block, "tableContent", localized),
	})
	if err != nil {
		return executionError(err)
	}
	if input.After == "" {
		if _, err := core.EncodeApplyRequest(request); err != nil {
			return executionError(err)
		}
		after, err := tools.lastBodyBlock(ctx, request, input.Parent)
		if err != nil {
			if conflict := mutationConflict(err); conflict != nil {
				return mutationSyncResult(request, conflict)
			}
			// A concurrent edit between outline pages invalidates the cursor: the document moved on.
			var cursorError *core.CursorError
			if errors.As(err, &cursorError) {
				return mutationSyncResult(request, &core.Conflict{Code: core.ConflictDocumentRevision, CurrentDocumentRevision: cursorError.CurrentDocumentRevision, CurrentTargetRevision: cursorError.CurrentTargetRevision})
			}
			return expectedToolError(err)
		}
		request.Operations[0].InsertBlock.After = after
	}
	return tools.applyRequest(ctx, request, block)
}

func plainTableValues(rows [][]string, headerRow, headerColumn bool) (core.Value, core.Value) {
	var sharedRows, localeRows []core.ListItem
	for r, row := range rows {
		rowID := core.RelationItemID(uuid.NewString())
		var sharedCells, localeCells []core.ListItem
		for col, text := range row {
			cellID := core.RelationItemID(uuid.NewString())
			sharedCells = append(sharedCells, core.StableItem(cellID, core.Object(
				core.ObjectValue("id", core.Text(string(cellID))), core.ObjectValue("header", core.Boolean((headerRow && r == 0) || (headerColumn && col == 0))),
				core.ObjectValue("colspan", core.Number("1")), core.ObjectValue("rowspan", core.Number("1")),
			)))
			localeCells = append(localeCells, core.StableItem(cellID, core.Object(core.ObjectValue("id", core.Text(string(cellID))), core.ObjectValue("content", core.RichText(core.InlineText(text))))))
		}
		sharedRows = append(sharedRows, core.StableItem(rowID, core.Object(core.ObjectValue("id", core.Text(string(rowID))), core.ObjectValue("cells", core.List(sharedCells...)))))
		localeRows = append(localeRows, core.StableItem(rowID, core.Object(core.ObjectValue("id", core.Text(string(rowID))), core.ObjectValue("cells", core.List(localeCells...)))))
	}
	hr, hc := "0", "0"
	if headerRow {
		hr = "1"
	}
	if headerColumn {
		hc = "1"
	}
	return core.Object(core.ObjectValue("headerRows", core.Number(hr)), core.ObjectValue("headerColumns", core.Number(hc)), core.ObjectValue("rows", core.List(sharedRows...))), core.Object(core.ObjectValue("rows", core.List(localeRows...)))
}

func (tools *AIDocumentTools) lastBodyBlock(ctx context.Context, mutation core.ApplyRequest, parent core.BlockID) (core.BlockID, error) {
	request := core.ReadRequest{Document: mutation.Identity(), Locale: mutation.Locale, Mode: core.ReadOutline, Limit: 256}
	var last core.BlockID
	lastOrder := -1
	for {
		projection, err := tools.application.Read(ctx, request)
		if err != nil {
			return "", err
		}
		code := core.ConflictCode("")
		if projection.DocumentRevision != mutation.ExpectedDocumentRevision {
			code = core.ConflictDocumentRevision
		} else if (projection.TargetRevision == nil) != (mutation.ExpectedTargetRevision == nil) || (projection.TargetRevision != nil && *projection.TargetRevision != *mutation.ExpectedTargetRevision) {
			code = core.ConflictTargetRevision
		}
		if code != "" {
			return "", &core.ConflictError{Conflict: core.Conflict{Code: code, CurrentDocumentRevision: projection.DocumentRevision, CurrentTargetRevision: projection.TargetRevision}}
		}
		for _, node := range projection.Nodes {
			if node.Parent == parent && node.ID != "document" && node.Order > lastOrder {
				last, lastOrder = node.ID, node.Order
			}
		}
		if projection.Next == nil {
			return last, nil
		}
		request.Cursor = *projection.Next
	}
}
