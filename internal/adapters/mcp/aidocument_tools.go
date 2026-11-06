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
)

const (
	ToolDocumentRead   = "document_read"
	ToolBlockDelete    = "document_block_delete"
	ToolMetadataUpdate = "document_metadata_update"
	ToolDocumentApply  = "document_apply"
	ToolBlockCreate    = "document_block_create"
	ToolBlockUpdate    = "document_block_update"
)

var documentTools = toolRegistry[AIDocumentTools]{
	defineTool(mcpserver.Tool{
		Name: ToolBlockCreate, Title: "Create document block",
		Description: "Create one plain-text paragraph or heading, or a rectangular table of plain-text rows, in a Page rich-text section or Post body; a paragraph also works in any document with blocks. Revisions are optional because this only adds a block. " +
			"A Page requires parent_block_id, a rich-text section; after_block_id must be a sibling. Omit after_block_id to insert first, or for a table to append. The server assigns the Block UUID." + syncRequiredGuidance,
		InputSchema: json.RawMessage(blockCreateInputJSONSchema), OutputSchema: mutationOutputSchema(focusedMutationOutputJSONSchema),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, false, false),
		Meta:            oauthSecurityMeta(),
	}, (*AIDocumentTools).createBlock),
	defineTool(mcpserver.Tool{
		Name: ToolBlockUpdate, Title: "Update document block",
		Description: "Replace the whole text of one paragraph or heading with plain text, and a heading's level and alignment. To change a word or sentence, document_text_replace is cheaper. Refused when the block has marks, links, colors, breaks or site variables, because plain text would remove them; use document_text_replace or document_text_format. " +
			"Pass document_revision (and target_revision for a translation) from your latest document_read or the previous edit; no reread is needed between consecutive edits." + syncRequiredGuidance,
		InputSchema: json.RawMessage(blockUpdateInputJSONSchema), OutputSchema: mutationOutputSchema(focusedMutationOutputJSONSchema),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, true, false),
		Meta:            oauthSecurityMeta(),
	}, (*AIDocumentTools).updateBlock),
	defineTool(mcpserver.Tool{
		Name: ToolDocumentRead, Title: "Read AI document",
		Description: "Read a document. mode=metadata returns only its revisions and locale state. mode=outline lists blocks with their kind, parent, order and a preview: the start of the block's locale text. Use it to find the blocks to read or change. " +
			"Add query to an outline to list only blocks whose text contains it (ignoring case, with runs of spaces counting as one), with the preview around the match: the fastest way to find a sentence. " +
			"mode=blocks returns full values for block_ids; mode=fields returns only the selected fields. An outline accepts no selectors. " +
			"Pages default to 256 outline blocks or 128 blocks/fields; limit sets up to 256. Follow next_cursor until it is null and do not mix pages from different revisions. " +
			"Use the document_id from document_list or another listing tool." + syncRequiredGuidance,
		InputSchema: readInputSchema(), OutputSchema: projectionOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(true, false, false),
		Meta:            oauthSecurityMeta(),
	}, (*AIDocumentTools).read),
	defineTool(mcpserver.Tool{
		Name: ToolBlockDelete, Title: "Delete document block",
		Description: "Delete one existing Block by its stable handle. " +
			"Pass document_revision (and target_revision for a translation) from your latest document_read or the previous edit; no reread is needed between consecutive edits. Structural deletion is allowed only in the current source locale." + syncRequiredGuidance,
		InputSchema: json.RawMessage(blockDeleteInputJSONSchema), OutputSchema: mutationOutputSchema(focusedMutationOutputJSONSchema),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, true, false),
		Meta:            oauthSecurityMeta(),
	}, (*AIDocumentTools).deleteBlock),
	defineTool(mcpserver.Tool{
		Name: ToolMetadataUpdate, Title: "Update document metadata",
		Description: "Update title or summary for a Post, Work, Page, or Program Event, localized title for a Release, Terms of Service or Privacy Policy, source-owned category_ids or tag_ids for a Post, or source-owned document_layout for a Page. Program Event title is source-owned; its summary is locale-owned. Release and legal policies have no summary field. Legal policy titles can be edited in every lifecycle with the required source or translation permissions; editing sends no notice email. " +
			"For Program Event, clear_summary removes the source summary; use an empty summary string for an existing target locale's explicit empty value. " +
			"Pass document_revision (and target_revision for a translation) from your latest document_read or the previous edit; no reread is needed between consecutive edits. Passing an empty category_ids or tag_ids array removes every item in that relation." + syncRequiredGuidance,
		InputSchema: json.RawMessage(documentMetadataUpdateInputJSONSchema), OutputSchema: mutationOutputSchema(focusedMutationOutputJSONSchema),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, true, false),
		Meta:            oauthSecurityMeta(),
	}, (*AIDocumentTools).updateMetadata),
	defineTool(mcpserver.Tool{
		Name: ToolDocumentApply, Title: "Apply AI document mutation",
		Description: "Apply a typed operation batch that the focused tools cannot represent, or with dry_run check it and get the normalized operations without changing anything. " +
			"To change words, links or marks, document_text_replace and document_text_format are simpler and need no read. Up to 100 operations apply atomically in one call. Pass document_revision (and target_revision for a translation) from your latest document_read or the previous edit; no reread is needed between consecutive edits. Page rich blocks require a rich-text section parent. A table needs insert_block plus set_field table (shared UUID rows/cells, id equal to handle, header flags) and set_field tableContent (locale cell content). Pages forbid inline math. See document_catalog." + syncRequiredGuidance,
		InputSchema: mutationInputSchema(), OutputSchema: acceptedOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, true, false),
		Meta:            oauthSecurityMeta(),
	}, (*AIDocumentTools).apply),
}

func mutationInputSchema() json.RawMessage {
	return json.RawMessage(mutationInputJSONSchema)
}

func readInputSchema() json.RawMessage { return json.RawMessage(readInputJSONSchema) }
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
	DocumentType core.Domain            `json:"document_type"`
	DocumentID   core.DocumentReference `json:"document_id"`
	Locale       core.Locale            `json:"locale"`
}

type readFieldSelection struct {
	Block    core.BlockID        `json:"block_id"`
	Relation core.RelationID     `json:"relation_id,omitempty"`
	Item     core.RelationItemID `json:"item_id,omitempty"`
	Field    core.FieldID        `json:"field"`
}

type readArguments struct {
	DocumentType core.Domain            `json:"document_type"`
	DocumentID   core.DocumentReference `json:"document_id"`
	Locale       core.Locale            `json:"locale"`
	Mode         core.ReadMode          `json:"mode"`
	Blocks       []core.BlockID         `json:"block_ids,omitempty"`
	Fields       []readFieldSelection   `json:"fields,omitempty"`
	Limit        int                    `json:"limit,omitempty"`
	Cursor       core.Cursor            `json:"cursor,omitempty"`
	Query        string                 `json:"query,omitempty"`
}

type focusedMutationArguments struct {
	DocumentType             core.Domain            `json:"document_type"`
	DocumentID               core.DocumentReference `json:"document_id"`
	Locale                   core.Locale            `json:"locale"`
	ExpectedDocumentRevision core.Revision          `json:"expected_document_revision"`
	ExpectedTargetRevision   *core.Revision         `json:"expected_target_revision,omitempty"`
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

// readModeMetadata asks document_read for the envelope with no blocks.
const readModeMetadata core.ReadMode = "metadata"

func (tools *AIDocumentTools) open(ctx context.Context, _ string, call toolCall[openArguments]) (mcpserver.ToolResult, error) {
	var input openArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if err := validateMCPDocumentReference(input.DocumentID); err != nil {
		return executionError(err)
	}
	if err := requireLocale(input.Locale, "locale"); err != nil {
		return executionError(err)
	}
	metadata, err := tools.application.Open(ctx, core.OpenRequest{
		Document: core.DocumentIdentity{Domain: input.DocumentType, Reference: input.DocumentID}, Locale: input.Locale,
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
	if err := validateMCPDocumentReference(input.DocumentID); err != nil {
		return executionError(err)
	}
	if err := requireLocale(input.Locale, "locale"); err != nil {
		return executionError(err)
	}
	if input.Mode == readModeMetadata {
		return tools.open(ctx, ToolDocumentRead, toolCall[openArguments]{Arguments: withoutArguments(call.Arguments, "mode")})
	}
	request := core.ReadRequest{
		Document: core.DocumentIdentity{Domain: input.DocumentType, Reference: input.DocumentID}, Locale: input.Locale,
		Mode: input.Mode, Blocks: input.Blocks, Limit: defaultReadLimit(input.Mode, input.Limit), Cursor: input.Cursor, Query: input.Query,
	}
	for _, field := range input.Fields {
		request.Fields = append(request.Fields, core.FieldSelection{Block: field.Block, Relation: field.Relation, Item: field.Item, Field: field.Field})
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
func defaultReadLimit(mode core.ReadMode, requested int) int {
	if requested != 0 {
		return requested
	}
	if mode == core.ReadOutline {
		return core.DefaultOutlineReadLimit
	}
	return core.DefaultValueReadLimit
}

// rejectFormattedReplacement keeps the plain-text focused tools from silently
// dropping marks, links, colors or breaks from an existing block's content.
func (tools *AIDocumentTools) rejectFormattedReplacement(ctx context.Context, input focusedMutationArguments, block core.BlockID) error {
	projection, err := tools.application.Read(ctx, core.ReadRequest{
		Document: core.DocumentIdentity{Domain: input.DocumentType, Reference: input.DocumentID}, Locale: input.Locale,
		Mode: core.ReadFields, Fields: []core.FieldSelection{{Block: block, Field: "content"}},
	})
	if err != nil {
		return err
	}
	for _, node := range projection.Nodes {
		for _, field := range node.Localized {
			if field.ID == "content" && core.HasInlineFormatting(field.Value) {
				return &core.InputError{Message: "block " + string(block) + " has formatting (marks, links, colors, breaks or site variables) that plain-text replacement would remove; " +
					"change it with document_text_replace or document_text_format, or read it with document_read mode=blocks and write its content with document_apply"}
			}
		}
	}
	return nil
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
	if input.DocumentType == core.DomainRelease && (input.Summary != nil || input.ClearSummary) {
		return executionError(errors.New("Release documents have no summary field"))
	}
	if (input.DocumentType == core.DomainTerms || input.DocumentType == core.DomainPrivacy) && (input.Summary != nil || input.ClearSummary) {
		return executionError(fmt.Errorf("%s documents have no summary field", input.DocumentType))
	}
	if input.DocumentType != core.DomainPost && (input.CategoryIDs != nil || input.TagIDs != nil) {
		return executionError(errors.New("category_ids and tag_ids are supported only for Post documents"))
	}
	if input.DocumentLayout != nil && input.DocumentType != core.DomainPage {
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

// withCurrentRevisions fills omitted revisions for an edit that only adds a
// block, so nothing it could overwrite needs a read first. Apply still rejects
// the revisions if the document moves before the edit lands.
func withCurrentRevisions(ctx context.Context, application AIDocumentApplication, input focusedMutationArguments) (focusedMutationArguments, error) {
	if input.ExpectedDocumentRevision != "" {
		return input, nil
	}
	if err := validateMCPDocumentReference(input.DocumentID); err != nil {
		return input, err
	}
	if err := requireLocale(input.Locale, "locale"); err != nil {
		return input, err
	}
	metadata, err := application.Open(ctx, core.OpenRequest{
		Document: core.DocumentIdentity{Domain: input.DocumentType, Reference: input.DocumentID}, Locale: input.Locale,
	})
	if err != nil {
		return input, err
	}
	input.ExpectedDocumentRevision = metadata.DocumentRevision
	input.ExpectedTargetRevision = metadata.TargetRevision
	return input, nil
}

func focusedApplyRequest(input focusedMutationArguments, operations []core.Operation) (core.ApplyRequest, error) {
	if err := validateMCPDocumentReference(input.DocumentID); err != nil {
		return core.ApplyRequest{}, err
	}
	if err := requireLocale(input.Locale, "locale"); err != nil {
		return core.ApplyRequest{}, err
	}
	return core.ApplyRequest{
		Protocol:                 core.ProtocolVersion,
		Profile:                  input.DocumentType,
		Document:                 input.DocumentID,
		Locale:                   input.Locale,
		ExpectedDocumentRevision: input.ExpectedDocumentRevision,
		ExpectedTargetRevision:   input.ExpectedTargetRevision,
		Operations:               operations,
	}, nil
}

func (tools *AIDocumentTools) validate(ctx context.Context, _ string, call toolCall[documentApplyArguments]) (mcpserver.ToolResult, error) {
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

// documentApplyArguments is the operation batch and whether to only check it.
type documentApplyArguments struct {
	core.ApplyRequest
	DryRun bool `json:"dry_run,omitempty"`
}

func (tools *AIDocumentTools) apply(ctx context.Context, name string, call toolCall[documentApplyArguments]) (mcpserver.ToolResult, error) {
	arguments := call.Arguments
	var dryRun bool
	if raw, present := arguments["dry_run"]; present {
		if err := json.Unmarshal(raw, &dryRun); err != nil {
			return executionError(errors.New("dry_run must be a boolean"))
		}
		arguments = withoutArguments(arguments, "dry_run")
	}
	if dryRun {
		return tools.validate(ctx, name, toolCall[documentApplyArguments]{Arguments: arguments})
	}
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
	encoded, err := json.Marshal(withoutArguments(arguments, "dry_run"))
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

// requireLocale names what to pass when a document locale is missing.
func requireLocale(locale core.Locale, argument string) error {
	if locale == "" {
		return fmt.Errorf("%s is required: pass the document's source_locale from document_list or document_read, or a translation locale", argument)
	}
	return nil
}

func validateMCPDocumentReference(reference core.DocumentReference) error {
	_, err := uuidutil.ParseCanonical(string(reference), "document_id")
	return err
}

var (
	_ mcpserver.ToolRegistry   = (*AIDocumentTools)(nil)
	_ mcpserver.ToolDispatcher = (*AIDocumentTools)(nil)
	_ ToolProvider             = (*AIDocumentTools)(nil)
)
