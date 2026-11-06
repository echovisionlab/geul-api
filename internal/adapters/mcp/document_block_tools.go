package mcp

import (
	"context"
	"errors"
	"fmt"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/google/uuid"
)

type paragraphCreateArguments struct {
	focusedMutationArguments
	Parent core.BlockID `json:"parent_block_id,omitempty"`
	After  core.BlockID `json:"after_block_id,omitempty"`
	Text   *string      `json:"text"`
}

type paragraphUpdateArguments struct {
	focusedMutationArguments
	Block core.BlockID `json:"block_id"`
	Text  *string      `json:"text"`
}

type blockDeleteArguments struct {
	focusedMutationArguments
	Block core.BlockID `json:"block_id"`
}

// blockCreateArguments and blockUpdateArguments name every field of the block
// kinds these tools delegate to; each kind's own operation refuses the others.
type blockCreateArguments struct {
	focusedMutationArguments
	Kind         string       `json:"kind"`
	Parent       core.BlockID `json:"parent_block_id,omitempty"`
	After        core.BlockID `json:"after_block_id,omitempty"`
	Text         *string      `json:"text,omitempty"`
	Level        *int         `json:"level,omitempty"`
	Alignment    *string      `json:"alignment,omitempty"`
	Rows         [][]string   `json:"rows,omitempty"`
	HeaderRow    bool         `json:"header_row,omitempty"`
	HeaderColumn bool         `json:"header_column,omitempty"`
}

type blockUpdateArguments struct {
	focusedMutationArguments
	Kind      string       `json:"kind"`
	Block     core.BlockID `json:"block_id"`
	Text      *string      `json:"text"`
	Level     *int         `json:"level,omitempty"`
	Alignment *string      `json:"alignment,omitempty"`
}

func (tools *AIDocumentTools) createBlock(ctx context.Context, _ string, call toolCall[blockCreateArguments]) (mcpserver.ToolResult, error) {
	var input blockCreateArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	arguments := withoutArguments(call.Arguments, "kind")
	switch input.Kind {
	case "paragraph":
		return tools.createParagraph(ctx, ToolBlockCreate, toolCall[paragraphCreateArguments]{Arguments: arguments})
	case "heading":
		return tools.createHeading(ctx, toolCall[headingArguments]{Arguments: arguments})
	case "table":
		return tools.createTable(ctx, ToolBlockCreate, toolCall[tableCreateArguments]{Arguments: arguments})
	}
	return executionError(fmt.Errorf("kind must be paragraph, heading or table, got %q", input.Kind))
}

func (tools *AIDocumentTools) updateBlock(ctx context.Context, _ string, call toolCall[blockUpdateArguments]) (mcpserver.ToolResult, error) {
	var input blockUpdateArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	arguments := withoutArguments(call.Arguments, "kind")
	switch input.Kind {
	case "paragraph":
		return tools.updateParagraph(ctx, ToolBlockUpdate, toolCall[paragraphUpdateArguments]{Arguments: arguments})
	case "heading":
		return tools.updateHeading(ctx, toolCall[headingArguments]{Arguments: arguments})
	}
	return executionError(fmt.Errorf("kind must be paragraph or heading, got %q", input.Kind))
}

func (tools *AIDocumentTools) createParagraph(ctx context.Context, _ string, call toolCall[paragraphCreateArguments]) (mcpserver.ToolResult, error) {
	arguments := call.Arguments

	var input paragraphCreateArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if input.Text == nil {
		return executionError(errors.New("text is required and must be a string"))
	}
	if err := rejectNullArguments(arguments, "parent_block_id", "after_block_id"); err != nil {
		return executionError(err)
	}
	focused, err := withCurrentRevisions(ctx, tools.application, input.focusedMutationArguments)
	if err != nil {
		return expectedToolError(err)
	}
	block := core.BlockID(uuid.NewString())
	request, err := focusedApplyRequest(focused, []core.Operation{
		core.InsertBlockOperation(block, "paragraph", input.Parent, input.After),
		core.SetFieldOperation(block, "content", core.RichText(core.InlineText(*input.Text))),
	})
	if err != nil {
		return executionError(err)
	}
	return tools.applyRequest(ctx, request, block)
}

func (tools *AIDocumentTools) updateParagraph(ctx context.Context, _ string, call toolCall[paragraphUpdateArguments]) (mcpserver.ToolResult, error) {
	var input paragraphUpdateArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if input.Text == nil {
		return executionError(errors.New("text is required and must be a string"))
	}
	if err := tools.rejectFormattedReplacement(ctx, input.focusedMutationArguments, input.Block); err != nil {
		return expectedToolError(err)
	}
	request, err := focusedApplyRequest(input.focusedMutationArguments, []core.Operation{
		core.SetFieldOperation(input.Block, "content", core.RichText(core.InlineText(*input.Text))),
	})
	if err != nil {
		return executionError(err)
	}
	return tools.applyRequest(ctx, request, "")
}

func (tools *AIDocumentTools) deleteBlock(ctx context.Context, _ string, call toolCall[blockDeleteArguments]) (mcpserver.ToolResult, error) {
	var input blockDeleteArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	request, err := focusedApplyRequest(input.focusedMutationArguments, []core.Operation{core.DeleteBlockOperation(input.Block)})
	if err != nil {
		return executionError(err)
	}
	return tools.applyRequest(ctx, request, "")
}
