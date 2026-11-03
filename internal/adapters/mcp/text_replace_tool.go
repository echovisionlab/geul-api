package mcp

import (
	"context"
	"errors"
	"strings"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

const ToolTextReplace = "document_text_replace"

type textReplaceArguments struct {
	focusedMutationArguments
	Block   core.BlockID `json:"block_id,omitempty"`
	Find    *string      `json:"find"`
	Replace *string      `json:"replace"`
}

// replaceText changes one exact text occurrence inside existing blocks
// without the caller rewriting the whole block, so marks, links and colors
// around it stay intact.
func (tools *AIDocumentTools) replaceText(ctx context.Context, _ string, call toolCall[textReplaceArguments]) (mcpserver.ToolResult, error) {
	var input textReplaceArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if input.Find == nil || input.Replace == nil {
		return executionError(errors.New("find and replace are required strings"))
	}
	if err := rejectNullArguments(call.Arguments, "block_id"); err != nil {
		return executionError(err)
	}
	if err := validateMCPDocumentReference(input.Document); err != nil {
		return executionError(err)
	}
	if err := requireLocale(input.Locale, "locale"); err != nil {
		return executionError(err)
	}
	if input.Block == "" && strings.TrimSpace(*input.Find) == "" {
		return executionError(errors.New("find made only of spaces needs block_id"))
	}
	identity := core.DocumentIdentity{Domain: input.Profile, Reference: input.Document}
	// The outline search narrows the blocks to read; its query is the start of
	// find, so it matches every block that contains find.
	query := string([]rune(*input.Find)[:min(len([]rune(*input.Find)), core.MaxReadQueryRunes)])
	recipe := readArguments{Profile: input.Profile, Document: input.Document, Locale: input.Locale, Mode: core.ReadOutline, Limit: defaultOutlineReadLimit, Query: query}

	blocks := []core.BlockID{input.Block}
	if input.Block == "" {
		matches, err := tools.readAll(ctx, core.ReadRequest{Document: identity, Locale: input.Locale, Mode: core.ReadOutline, Query: query, Limit: defaultOutlineReadLimit})
		if err != nil {
			return expectedToolError(err)
		}
		if stale, result, err := staleTextReplace(input.focusedMutationArguments, matches, recipe); stale {
			return result, err
		}
		blocks = blocks[:0]
		for _, node := range matches.nodes {
			blocks = append(blocks, node.ID)
		}
	} else {
		recipe.Mode, recipe.Query, recipe.Blocks, recipe.Limit = core.ReadBlocks, "", blocks, 1
	}

	var nodes []core.Node
	if len(blocks) != 0 {
		for start := 0; start < len(blocks); start += defaultValueReadLimit {
			chunk := blocks[start:min(start+defaultValueReadLimit, len(blocks))]
			page, err := tools.readAll(ctx, core.ReadRequest{Document: identity, Locale: input.Locale, Mode: core.ReadBlocks, Blocks: chunk, Limit: defaultValueReadLimit})
			if err != nil {
				return expectedToolError(err)
			}
			if stale, result, err := staleTextReplace(input.focusedMutationArguments, page, recipe); stale {
				return result, err
			}
			nodes = append(nodes, page.nodes...)
		}
	}
	replacement, err := core.ReplaceTextOnce(nodes, *input.Find, *input.Replace)
	if err != nil {
		return expectedToolError(err)
	}
	request, err := focusedApplyRequest(input.focusedMutationArguments, []core.Operation{replacement.Operation})
	if err != nil {
		return executionError(err)
	}
	return tools.applyRequest(ctx, request, "")
}

type readRevisions struct {
	document core.Revision
	target   *core.Revision
}

type readResult struct {
	nodes     []core.Node
	revisions readRevisions
	// mixed reports pages read on different revisions; revisions is the last.
	mixed bool
}

// readAll follows next cursors and reports pages that span revisions.
func (tools *AIDocumentTools) readAll(ctx context.Context, request core.ReadRequest) (readResult, error) {
	var result readResult
	for first := true; ; first = false {
		projection, err := tools.application.Read(ctx, request)
		if err != nil {
			return readResult{}, err
		}
		revisions := readRevisions{document: projection.DocumentRevision, target: projection.TargetRevision}
		if !first && !sameReadRevisions(result.revisions, revisions) {
			result.mixed = true
		}
		result.revisions = revisions
		result.nodes = append(result.nodes, projection.Nodes...)
		if projection.Next == nil {
			return result, nil
		}
		request.Cursor = *projection.Next
	}
}

func sameReadRevisions(a, b readRevisions) bool {
	if a.document != b.document || (a.target == nil) != (b.target == nil) {
		return false
	}
	return a.target == nil || *a.target == *b.target
}

// staleTextReplace turns a read on newer revisions than the caller's into the
// usual sync_required result, before a stale find could match newer text.
func staleTextReplace(input focusedMutationArguments, read readResult, recipe readArguments) (bool, mcpserver.ToolResult, error) {
	expected := readRevisions{document: input.ExpectedDocumentRevision, target: input.ExpectedTargetRevision}
	current := read.revisions
	if !read.mixed && sameReadRevisions(expected, current) {
		return false, mcpserver.ToolResult{}, nil
	}
	reason := "document_revision_changed"
	if expected.document == current.document {
		reason = "target_revision_changed"
	}
	var handles []string
	for _, block := range recipe.Blocks {
		handles = append(handles, string(block))
	}
	result, err := syncRequiredResult(reason, current.document, current.target, handles, recipe, false)
	return true, result, err
}
