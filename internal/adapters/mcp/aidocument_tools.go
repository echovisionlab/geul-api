package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	"github.com/google/uuid"
)

const (
	ToolDocumentOpen     = "document_open"
	ToolDocumentRead     = "document_read"
	ToolParagraphCreate  = "document_paragraph_create"
	ToolParagraphUpdate  = "document_paragraph_update"
	ToolHeadingCreate    = "document_heading_create"
	ToolHeadingUpdate    = "document_heading_update"
	ToolTableCreate      = "document_table_create"
	ToolBlockDelete      = "document_block_delete"
	ToolMetadataUpdate   = "document_metadata_update"
	ToolDocumentValidate = "document_validate"
	ToolDocumentApply    = "document_apply"
)

var documentTools = toolRegistry[AIDocumentTools]{
	defineTool(mcpserver.Tool{
		Name: ToolDocumentOpen, Title: "Open AI document",
		Description: "Optional: return a document's revisions and locale state without blocks. document_read returns the same envelope, so read directly when you already have d. " +
			"Use d returned by document_list or another listing tool; never pass a URL slug as d.",
		InputSchema: openInputSchema(), OutputSchema: openOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(true, false, false),
		Meta:            oauthSecurityMeta(),
	}, (*AIDocumentTools).open),
	defineTool(mcpserver.Tool{
		Name: ToolDocumentRead, Title: "Read AI document",
		Description: "Read a document. m=outline lists every block's handle, kind, parent and order, with pv mapping handles to the start of their locale text: use it to find the blocks to read or change. " +
			"m=blocks returns full values for the handles in b; m=fields returns selected values ([block,field] or [block,relation,item,field]). outline accepts no selectors. " +
			"Pages default to 256 outline nodes or 128 blocks/fields; n sets up to 256. Follow next until it is null and do not mix pages from different revisions. " +
			"Site variable atoms are [\"siteVariable\",key] or [\"siteVariable\",key,true]. Use d from document_list or another listing tool." + syncRequiredGuidance,
		InputSchema: readInputSchema(), OutputSchema: projectionOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(true, false, false),
		Meta:            oauthSecurityMeta(),
	}, (*AIDocumentTools).read),
	defineTool(mcpserver.Tool{
		Name: ToolParagraphCreate, Title: "Create document paragraph",
		Description: "Create one plain-text Paragraph Block in an existing document. " +
			"Pass dr (and tr for a translation) from your latest document_read or the previous edit; no reread is needed between consecutive edits. Page paragraphs require parent_block_id set to an existing rich-text section; after_block_id, when present, must be a sibling in that parent. The server assigns the new Block UUID." + syncRequiredGuidance,
		InputSchema: json.RawMessage(paragraphCreateInputJSONSchema), OutputSchema: mutationOutputSchema(focusedMutationOutputJSONSchema),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, false, false),
		Meta:            oauthSecurityMeta(),
	}, (*AIDocumentTools).createParagraph),
	defineTool(mcpserver.Tool{
		Name: ToolParagraphUpdate, Title: "Update document paragraph",
		Description: "Replace the text of one existing Paragraph Block with plain text. Refused when the paragraph has marks, links, colors, breaks or site variables, because plain text would remove them; " +
			"write that paragraph's full inline content with document_apply fs content [\"i\",...] instead. Pass dr (and tr for a translation) from your latest document_read or the previous edit; no reread is needed between consecutive edits." + syncRequiredGuidance,
		InputSchema: json.RawMessage(paragraphUpdateInputJSONSchema), OutputSchema: mutationOutputSchema(focusedMutationOutputJSONSchema),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, true, false),
		Meta:            oauthSecurityMeta(),
	}, (*AIDocumentTools).updateParagraph),
	defineTool(mcpserver.Tool{Name: ToolHeadingCreate, Title: "Create document heading",
		Description: "Create a plain-text heading with level 1..3 and optional alignment in a Page rich-text section or Post body. Pass dr (and tr for a translation) from your latest document_read or the previous edit; no reread is needed between consecutive edits. Page requires parent_block_id; after_block_id must be a sibling. Omit after_block_id to insert first. The server assigns the Block UUID." + syncRequiredGuidance,
		InputSchema: headingInputSchema(false), OutputSchema: mutationOutputSchema(focusedMutationOutputJSONSchema), SecuritySchemes: oauthSecuritySchemes(), Annotations: toolAnnotations(false, false, false), Meta: oauthSecurityMeta()}, func(tools *AIDocumentTools, ctx context.Context, name string, call toolCall[headingArguments]) (mcpserver.ToolResult, error) {
		return tools.mutateHeading(ctx, call, false)
	}),
	defineTool(mcpserver.Tool{Name: ToolHeadingUpdate, Title: "Update document heading",
		Description: "Replace an existing heading's text with plain text and optionally its level (1..3) and alignment, in a Page or Post. Refused when the heading has formatting; use document_apply for it. Omitted level and alignment are preserved. Pass dr (and tr for a translation) from your latest document_read or the previous edit; no reread is needed between consecutive edits." + syncRequiredGuidance,
		InputSchema: headingInputSchema(true), OutputSchema: mutationOutputSchema(focusedMutationOutputJSONSchema), SecuritySchemes: oauthSecuritySchemes(), Annotations: toolAnnotations(false, true, false), Meta: oauthSecurityMeta()}, func(tools *AIDocumentTools, ctx context.Context, name string, call toolCall[headingArguments]) (mcpserver.ToolResult, error) {
		return tools.mutateHeading(ctx, call, true)
	}),
	defineTool(mcpserver.Tool{Name: ToolTableCreate, Title: "Create document table",
		Description: "Create a rectangular table of plain-text rows in a Page rich-text section or Post body, with optional header row/column. Pass dr (and tr for a translation) from your latest document_read or the previous edit; no reread is needed between consecutive edits. Page requires parent_block_id. Insert after after_block_id, or omit it to append to that section/body. The server assigns Block, row and cell UUIDs and writes structure and locale content in one mutation." + syncRequiredGuidance,
		InputSchema: tableCreateInputSchema(), OutputSchema: mutationOutputSchema(focusedMutationOutputJSONSchema), SecuritySchemes: oauthSecuritySchemes(), Annotations: toolAnnotations(false, false, false), Meta: oauthSecurityMeta()}, (*AIDocumentTools).createTable),
	defineTool(mcpserver.Tool{
		Name: ToolBlockDelete, Title: "Delete document block",
		Description: "Delete one existing Block by its stable handle. " +
			"Pass dr (and tr for a translation) from your latest document_read or the previous edit; no reread is needed between consecutive edits. Structural deletion is allowed only in the current source locale." + syncRequiredGuidance,
		InputSchema: json.RawMessage(blockDeleteInputJSONSchema), OutputSchema: mutationOutputSchema(focusedMutationOutputJSONSchema),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, true, false),
		Meta:            oauthSecurityMeta(),
	}, (*AIDocumentTools).deleteBlock),
	defineTool(mcpserver.Tool{
		Name: ToolMetadataUpdate, Title: "Update document metadata",
		Description: "Update title or summary for a Post, Work, Page, or Program Event, localized title for a Release, Terms of Service or Privacy Policy, source-owned category_ids or tag_ids for a Post, or source-owned document_layout for a Page. Program Event title is source-owned; its summary is locale-owned. Release and legal policies have no summary field. Legal policy titles can be edited in every lifecycle with the required source or translation permissions; editing sends no notice email. " +
			"For Program Event, clear_summary removes the source summary; use an empty summary string for an existing target locale's explicit empty value. " +
			"Pass dr (and tr for a translation) from your latest document_read or the previous edit; no reread is needed between consecutive edits. Passing an empty category_ids or tag_ids array removes every item in that relation." + syncRequiredGuidance,
		InputSchema: json.RawMessage(documentMetadataUpdateInputJSONSchema), OutputSchema: mutationOutputSchema(focusedMutationOutputJSONSchema),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, true, false),
		Meta:            oauthSecurityMeta(),
	}, (*AIDocumentTools).updateMetadata),
	defineTool(mcpserver.Tool{
		Name: ToolDocumentValidate, Title: "Validate AI document mutation",
		Description: "Dry-run compact typed DCDP/1 operations without mutating the document when the user explicitly asks to validate or preview an advanced batch. " +
			"If the user then chooses to apply it, pass the returned normalized operations to document_apply." + syncRequiredGuidance,
		InputSchema: mutationInputSchema(), OutputSchema: validationOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(true, false, false),
		Meta:            oauthSecurityMeta(),
	}, (*AIDocumentTools).validate),
	defineTool(mcpserver.Tool{
		Name: ToolDocumentApply, Title: "Apply AI document mutation",
		Description: "Apply an advanced typed DCDP/1 operation batch that the focused tools cannot represent. " +
			"Use it to keep formatting when editing text: send the block's full inline content read with m=blocks. Up to 100 operations apply atomically in one call. Pass dr (and tr for a translation) from your latest document_read or the previous edit; no reread is needed between consecutive edits. Use schema-defined tuples; document_validate is optional unless the user requests a dry run. Page rich blocks require a rich-text section parent. Heading level is ['n','1'|'2'|'3']. Tables use bi plus fs table (shared UUID rows/cells, id equal to handle, header flags) and fs tableContent (locale cell inline content). Pages forbid inline math. See document_catalog." + syncRequiredGuidance,
		InputSchema: mutationInputSchema(), OutputSchema: acceptedOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, true, false),
		Meta:            oauthSecurityMeta(),
	}, (*AIDocumentTools).apply),
}

func mutationInputSchema() json.RawMessage {
	return json.RawMessage(mutationInputJSONSchema)
}

func validationOutputSchema() json.RawMessage {
	return outputSchemaWithSync(validationOutputJSONSchema)
}

func openInputSchema() json.RawMessage  { return json.RawMessage(openInputJSONSchema) }
func openOutputSchema() json.RawMessage { return json.RawMessage(openOutputJSONSchema) }
func readInputSchema() json.RawMessage  { return json.RawMessage(readInputJSONSchema) }
func projectionOutputSchema() json.RawMessage {
	return outputSchemaWithSync(projectionOutputJSONSchema)
}
func acceptedOutputSchema() json.RawMessage { return mutationOutputSchema(acceptedOutputJSONSchema) }

func mutationOutputSchema(successJSON string) json.RawMessage {
	return outputSchemaWithSync(`{"type":"object","oneOf":[` + successJSON + `,` + validationOutputJSONSchema + `]}`)
}

// AIDocumentTools is a static MCP registry and dispatcher backed by the same
// application service used by the generated AIDocumentService transport.
// Resource authorization and aggregate CAS remain in that injected service.
type AIDocumentTools struct {
	application AIDocumentApplication
}

// AIDocumentApplication is the minimal capability consumed by this transport.
// The owning application core may be shared with other transports, but MCP
// never calls another transport adapter.
type AIDocumentApplication interface {
	Open(context.Context, core.OpenRequest) (core.OpenMetadata, error)
	Read(context.Context, core.ReadRequest) (core.Projection, error)
	Validate(context.Context, core.ApplyRequest) (core.ValidationResult, error)
	Apply(context.Context, core.ApplyRequest) (core.ApplyResult, error)
}

func NewAIDocumentTools(application AIDocumentApplication) (*AIDocumentTools, error) {
	if interfaceValueIsNil(application) {
		return nil, errors.New("MCP AI document application is required")
	}
	return &AIDocumentTools{application: application}, nil
}

func (*AIDocumentTools) ToolNames() []string { return documentTools.names() }

func (tools *AIDocumentTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return documentTools.list(), nil
}

func (tools *AIDocumentTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	return documentTools.call(tools, ctx, name, arguments)
}

type openArguments struct {
	Profile  core.Domain            `json:"p"`
	Document core.DocumentReference `json:"d"`
	Locale   core.Locale            `json:"l"`
}

type readArguments struct {
	Profile  core.Domain            `json:"p"`
	Document core.DocumentReference `json:"d"`
	Locale   core.Locale            `json:"l"`
	Mode     core.ReadMode          `json:"m"`
	Blocks   []core.BlockID         `json:"b,omitempty"`
	Fields   [][]string             `json:"f,omitempty"`
	Limit    int                    `json:"n,omitempty"`
	Cursor   core.Cursor            `json:"c,omitempty"`
}

type focusedMutationArguments struct {
	Profile                  core.Domain            `json:"document_type"`
	Document                 core.DocumentReference `json:"document_id"`
	Locale                   core.Locale            `json:"locale"`
	ExpectedDocumentRevision core.Revision          `json:"expected_document_revision"`
	ExpectedTargetRevision   *core.Revision         `json:"expected_target_revision,omitempty"`
}

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

type metadataUpdateArguments struct {
	focusedMutationArguments
	Title          *string                  `json:"title,omitempty"`
	Summary        *string                  `json:"summary,omitempty"`
	ClearSummary   bool                     `json:"clear_summary,omitempty"`
	CategoryIDs    *[]string                `json:"category_ids,omitempty"`
	TagIDs         *[]string                `json:"tag_ids,omitempty"`
	DocumentLayout *documentLayoutArguments `json:"document_layout,omitempty"`
}

func (tools *AIDocumentTools) open(ctx context.Context, _ string, call toolCall[openArguments]) (mcpserver.ToolResult, error) {
	var input openArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if err := validateMCPDocumentReference(input.Document); err != nil {
		return executionError(err)
	}
	metadata, err := tools.application.Open(ctx, core.OpenRequest{
		Document: core.DocumentIdentity{Domain: input.Profile, Reference: input.Document}, Locale: input.Locale,
	})
	if err != nil {
		return expectedToolError(err)
	}
	encoded, err := core.EncodeOpenMetadata(metadata)
	if err != nil {
		return mcpserver.ToolResult{}, fmt.Errorf("encode MCP document metadata: %w", err)
	}
	return structuredResult(encoded, false)
}

func (tools *AIDocumentTools) read(ctx context.Context, _ string, call toolCall[readArguments]) (mcpserver.ToolResult, error) {
	var input readArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if err := validateMCPDocumentReference(input.Document); err != nil {
		return executionError(err)
	}
	request := core.ReadRequest{
		Document: core.DocumentIdentity{Domain: input.Profile, Reference: input.Document}, Locale: input.Locale,
		Mode: input.Mode, Blocks: input.Blocks, Limit: defaultReadLimit(input.Mode, input.Limit), Cursor: input.Cursor,
	}
	for index, field := range input.Fields {
		switch len(field) {
		case 2:
			request.Fields = append(request.Fields, core.FieldSelection{Block: core.BlockID(field[0]), Field: core.FieldID(field[1])})
		case 4:
			request.Fields = append(request.Fields, core.FieldSelection{Block: core.BlockID(field[0]), Relation: core.RelationID(field[1]), Item: core.RelationItemID(field[2]), Field: core.FieldID(field[3])})
		default:
			return executionError(fmt.Errorf("field selector %d must contain 2 or 4 stable handles", index))
		}
	}
	projection, err := tools.application.Read(ctx, request)
	if err != nil {
		var cursorError *core.CursorError
		if errors.As(err, &cursorError) {
			return syncRequiredResult("read_continuation_invalid", cursorError.CurrentDocumentRevision, cursorError.CurrentTargetRevision, nil, input, true)
		}
		return expectedToolError(err)
	}
	encoded, err := core.EncodeProjection(projection)
	if err != nil {
		return mcpserver.ToolResult{}, fmt.Errorf("encode MCP document projection: %w", err)
	}
	return structuredResult(encoded, false)
}

// MCP reads default to pages large enough that typical documents fit in one
// call: outline nodes are small, block values are larger.
const (
	defaultOutlineReadLimit = 256
	defaultValueReadLimit   = 128
)

func defaultReadLimit(mode core.ReadMode, requested int) int {
	if requested != 0 {
		return requested
	}
	if mode == core.ReadOutline {
		return defaultOutlineReadLimit
	}
	return defaultValueReadLimit
}

// rejectFormattedReplacement keeps the plain-text focused tools from silently
// dropping marks, links, colors or breaks from an existing block's content.
func (tools *AIDocumentTools) rejectFormattedReplacement(ctx context.Context, input focusedMutationArguments, block core.BlockID) error {
	projection, err := tools.application.Read(ctx, core.ReadRequest{
		Document: core.DocumentIdentity{Domain: input.Profile, Reference: input.Document}, Locale: input.Locale,
		Mode: core.ReadFields, Fields: []core.FieldSelection{{Block: block, Field: "content"}},
	})
	if err != nil {
		return err
	}
	for _, node := range projection.Nodes {
		for _, field := range node.Localized {
			if field.ID == "content" && core.HasInlineFormatting(field.Value) {
				return &core.InputError{Message: "block " + string(block) + " has formatting (marks, links, colors, breaks or site variables) that plain-text replacement would remove; " +
					"read it with document_read m=blocks and write the full inline content with document_apply fs content [\"i\",...] instead"}
			}
		}
	}
	return nil
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
	block := core.BlockID(uuid.NewString())
	request, err := focusedApplyRequest(input.focusedMutationArguments, []core.Operation{
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

func (tools *AIDocumentTools) updateMetadata(ctx context.Context, _ string, call toolCall[metadataUpdateArguments]) (mcpserver.ToolResult, error) {
	arguments := call.Arguments

	var input metadataUpdateArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if err := rejectNullArguments(arguments, "title", "summary", "clear_summary", "category_ids", "tag_ids", "document_layout"); err != nil {
		return executionError(err)
	}
	if input.Summary != nil && input.ClearSummary {
		return executionError(errors.New("summary and clear_summary cannot be used together"))
	}
	if input.Profile == core.DomainRelease && (input.Summary != nil || input.ClearSummary) {
		return executionError(errors.New("Release documents have no summary field"))
	}
	if (input.Profile == core.DomainTerms || input.Profile == core.DomainPrivacy) && (input.Summary != nil || input.ClearSummary) {
		return executionError(fmt.Errorf("%s documents have no summary field", input.Profile))
	}
	if input.Profile != core.DomainPost && (input.CategoryIDs != nil || input.TagIDs != nil) {
		return executionError(errors.New("category_ids and tag_ids are supported only for Post documents"))
	}
	if input.DocumentLayout != nil && input.Profile != core.DomainPage {
		return executionError(errors.New("document_layout here is supported only for Page documents; use post_settings_update for Post layout"))
	}
	operations := make([]core.Operation, 0, 4)
	if input.Title != nil {
		operations = append(operations, core.SetFieldOperation("document", "title", core.Text(*input.Title)))
	}
	if input.Summary != nil {
		operations = append(operations, core.SetFieldOperation("document", "summary", core.Text(*input.Summary)))
	} else if input.ClearSummary {
		operations = append(operations, core.UnsetFieldOperation("document", "summary"))
	}
	if input.CategoryIDs != nil {
		value, err := metadataUUIDList(*input.CategoryIDs)
		if err != nil {
			return executionError(fmt.Errorf("category_ids: %w", err))
		}
		operations = append(operations, core.SetFieldOperation("document", "categoryIds", value))
	}
	if input.TagIDs != nil {
		value, err := metadataUUIDList(*input.TagIDs)
		if err != nil {
			return executionError(fmt.Errorf("tag_ids: %w", err))
		}
		operations = append(operations, core.SetFieldOperation("document", "tagIds", value))
	}
	if input.DocumentLayout != nil {
		layout, err := input.DocumentLayout.proto()
		if err != nil {
			return executionError(err)
		}
		operations = append(operations, core.SetFieldOperation("document", "documentLayout", core.Object(
			core.ObjectValue("contentHeight", core.Text(input.DocumentLayout.ContentHeight)),
			core.ObjectValue("pageChrome", core.Text(input.DocumentLayout.PageChrome)),
			core.ObjectValue("footer", core.Text(input.DocumentLayout.Footer)),
			core.ObjectValue("hideTableOfContents", core.Boolean(layout.HideTableOfContents)),
		)))
	}
	if len(operations) == 0 {
		return executionError(errors.New("at least one metadata field is required"))
	}
	request, err := focusedApplyRequest(input.focusedMutationArguments, operations)
	if err != nil {
		return executionError(err)
	}
	return tools.applyRequest(ctx, request, "")
}

func metadataUUIDList(ids []string) (core.Value, error) {
	items := make([]core.ListItem, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, raw := range ids {
		id, err := uuidutil.ParseCanonical(raw, "item_id")
		if err != nil {
			return core.Value{}, err
		}
		canonical := id.String()
		if _, duplicate := seen[canonical]; duplicate {
			return core.Value{}, fmt.Errorf("duplicate UUID %q", canonical)
		}
		seen[canonical] = struct{}{}
		items = append(items, core.StableItem(core.RelationItemID(canonical), core.Text(canonical)))
	}
	return core.List(items...), nil
}

func focusedApplyRequest(input focusedMutationArguments, operations []core.Operation) (core.ApplyRequest, error) {
	if err := validateMCPDocumentReference(input.Document); err != nil {
		return core.ApplyRequest{}, err
	}
	return core.ApplyRequest{
		Protocol:                 core.ProtocolVersion,
		Profile:                  input.Profile,
		Document:                 input.Document,
		Locale:                   input.Locale,
		ExpectedDocumentRevision: input.ExpectedDocumentRevision,
		ExpectedTargetRevision:   input.ExpectedTargetRevision,
		Operations:               operations,
	}, nil
}

func (tools *AIDocumentTools) validate(ctx context.Context, _ string, call toolCall[core.ApplyRequest]) (mcpserver.ToolResult, error) {
	arguments := call.Arguments
	request, err := decodeMutation(arguments)
	if err != nil {
		return executionError(err)
	}
	validation, err := tools.application.Validate(ctx, request)
	if err != nil {
		if conflict := mutationConflict(err); conflict != nil {
			slog.WarnContext(ctx, "MCP document mutation conflicted", "failure_cause", failurecause.Message(err), "document_id", string(request.Document), "locale", string(request.Locale), "domain", string(request.Profile))
			return mutationSyncResult(request, conflict)
		}
		return expectedToolError(err)
	}
	if validation.Conflict != nil {
		return mutationSyncResult(request, validation.Conflict)
	}
	encoded, err := encodeValidation(validation)
	if err != nil {
		return mcpserver.ToolResult{}, fmt.Errorf("encode MCP document validation: %w", err)
	}
	return structuredResult(encoded, false)
}

func (tools *AIDocumentTools) apply(ctx context.Context, _ string, call toolCall[core.ApplyRequest]) (mcpserver.ToolResult, error) {
	arguments := call.Arguments
	request, err := decodeMutation(arguments)
	if err != nil {
		return executionError(err)
	}
	return tools.applyRequest(ctx, request, "")
}

func (tools *AIDocumentTools) applyRequest(ctx context.Context, request core.ApplyRequest, createdBlock core.BlockID) (mcpserver.ToolResult, error) {
	result, err := tools.application.Apply(ctx, request)
	if err != nil {
		if conflict := mutationConflict(err); conflict != nil {
			slog.WarnContext(ctx, "MCP document mutation conflicted", "failure_cause", failurecause.Message(err), "document_id", string(request.Document), "locale", string(request.Locale), "domain", string(request.Profile))
			return mutationSyncResult(request, conflict)
		}
		var validationError *core.ValidationError
		if errors.As(err, &validationError) {
			slog.WarnContext(ctx, "MCP document mutation rejected", failurecause.Attr(err), "document_id", string(request.Document), "locale", string(request.Locale), "domain", string(request.Profile), "issue_count", len(validationError.Result.Issues), core.IssuesLogAttr(validationError.Result.Issues))
			encoded, encodeErr := encodeValidation(validationError.Result)
			if encodeErr != nil {
				return mcpserver.ToolResult{}, fmt.Errorf("encode MCP document rejection: %w", encodeErr)
			}
			return structuredResult(encoded, true)
		}
		return expectedToolError(err)
	}
	encoded, err := encodeFocusedAccepted(result, createdBlock)
	if err != nil {
		return mcpserver.ToolResult{}, fmt.Errorf("encode MCP accepted mutation: %w", err)
	}
	return structuredResult(encoded, false)
}

func decodeMutation(arguments mcpserver.ToolArguments) (core.ApplyRequest, error) {
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return core.ApplyRequest{}, err
	}
	request, err := core.DecodeApplyRequest(encoded)
	if err != nil {
		return core.ApplyRequest{}, err
	}
	if err := validateMCPDocumentReference(request.Document); err != nil {
		return core.ApplyRequest{}, err
	}
	return request, nil
}

func validateMCPDocumentReference(reference core.DocumentReference) error {
	_, err := uuidutil.ParseCanonical(string(reference), "d")
	return err
}

var (
	_ mcpserver.ToolRegistry   = (*AIDocumentTools)(nil)
	_ mcpserver.ToolDispatcher = (*AIDocumentTools)(nil)
	_ ToolProvider             = (*AIDocumentTools)(nil)
)
