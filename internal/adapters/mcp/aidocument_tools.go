package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	"github.com/google/uuid"
)

const (
	ToolDocumentOpen     = "document_open"
	ToolDocumentRead     = "document_read"
	ToolParagraphCreate  = "document_paragraph_create"
	ToolParagraphUpdate  = "document_paragraph_update"
	ToolBlockDelete      = "document_block_delete"
	ToolMetadataUpdate   = "document_metadata_update"
	ToolDocumentValidate = "document_validate"
	ToolDocumentApply    = "document_apply"
)

var documentTools = []mcpserver.Tool{
	{
		Name: ToolDocumentOpen, Title: "Open AI document",
		Description: "Open a locale-aware DCDP/1 document by canonical UUID. " +
			"Use d returned by document_list when selecting a Post; do not pass a URL slug as d.",
		InputSchema: openInputSchema(), OutputSchema: openOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(true, false, false),
		Meta:            oauthSecurityMeta(),
	},
	{
		Name: ToolDocumentRead, Title: "Read AI document",
		Description: "Read a compact outline, selected blocks, or selected fields by stable handles. " +
			"Pass the canonical document UUID d returned by document_list or document_open.",
		InputSchema: readInputSchema(), OutputSchema: projectionOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(true, false, false),
		Meta:            oauthSecurityMeta(),
	},
	{
		Name: ToolParagraphCreate, Title: "Create document paragraph",
		Description: "Create one plain-text Paragraph Block in an existing document. " +
			"Read the document first and pass its exact current revision. Page paragraphs require parent_block_id set to an existing rich-text section; after_block_id, when present, must be a sibling in that parent. The server assigns the new Block UUID.",
		InputSchema: json.RawMessage(paragraphCreateInputJSONSchema), OutputSchema: json.RawMessage(focusedMutationOutputJSONSchema),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, false, false),
		Meta:            oauthSecurityMeta(),
	},
	{
		Name: ToolParagraphUpdate, Title: "Update document paragraph",
		Description: "Replace the plain text of one existing Paragraph Block. " +
			"Read the Block first and pass its stable block_id with the exact current document and target revisions.",
		InputSchema: json.RawMessage(paragraphUpdateInputJSONSchema), OutputSchema: json.RawMessage(focusedMutationOutputJSONSchema),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, true, false),
		Meta:            oauthSecurityMeta(),
	},
	{
		Name: ToolBlockDelete, Title: "Delete document block",
		Description: "Delete one existing Block by its stable handle. " +
			"Read the document first and pass the exact current document revision. Structural deletion is allowed only in the current source locale.",
		InputSchema: json.RawMessage(blockDeleteInputJSONSchema), OutputSchema: json.RawMessage(focusedMutationOutputJSONSchema),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, true, false),
		Meta:            oauthSecurityMeta(),
	},
	{
		Name: ToolMetadataUpdate, Title: "Update document metadata",
		Description: "Update locale-owned title or summary for a Post, Work, or Page, and source-owned category_ids or tag_ids for a Post. " +
			"Read the document first and pass its exact current revisions. Passing an empty category_ids or tag_ids array removes every item in that relation.",
		InputSchema: json.RawMessage(documentMetadataUpdateInputJSONSchema), OutputSchema: json.RawMessage(focusedMutationOutputJSONSchema),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, true, false),
		Meta:            oauthSecurityMeta(),
	},
	{
		Name: ToolDocumentValidate, Title: "Validate AI document mutation",
		Description: "Dry-run compact typed DCDP/1 operations without mutating the document when the user explicitly asks to validate or preview an advanced batch. " +
			"If the user then chooses to apply it, pass the returned normalized operations to document_apply.",
		InputSchema: mutationInputSchema(), OutputSchema: validationOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(true, false, false),
		Meta:            oauthSecurityMeta(),
	},
	{
		Name: ToolDocumentApply, Title: "Apply AI document mutation",
		Description: "Apply an advanced typed DCDP/1 operation batch that the focused paragraph tools cannot represent. " +
			"Use exact current revisions and schema-defined tuples; document_validate is optional unless the user requests a dry run.",
		InputSchema: mutationInputSchema(), OutputSchema: acceptedOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, true, false),
		Meta:            oauthSecurityMeta(),
	},
}

func mutationInputSchema() json.RawMessage {
	return json.RawMessage(mutationInputJSONSchema)
}

func validationOutputSchema() json.RawMessage {
	return json.RawMessage(validationOutputJSONSchema)
}

func openInputSchema() json.RawMessage        { return json.RawMessage(openInputJSONSchema) }
func openOutputSchema() json.RawMessage       { return json.RawMessage(openOutputJSONSchema) }
func readInputSchema() json.RawMessage        { return json.RawMessage(readInputJSONSchema) }
func projectionOutputSchema() json.RawMessage { return json.RawMessage(projectionOutputJSONSchema) }
func acceptedOutputSchema() json.RawMessage   { return json.RawMessage(acceptedOutputJSONSchema) }

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

func (*AIDocumentTools) ToolNames() []string {
	return toolDefinitionNames(documentTools)
}

func (tools *AIDocumentTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return cloneToolDefinitions(documentTools), nil
}

func (tools *AIDocumentTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	switch name {
	case ToolDocumentOpen:
		return tools.open(ctx, arguments)
	case ToolDocumentRead:
		return tools.read(ctx, arguments)
	case ToolParagraphCreate:
		return tools.createParagraph(ctx, arguments)
	case ToolParagraphUpdate:
		return tools.updateParagraph(ctx, arguments)
	case ToolBlockDelete:
		return tools.deleteBlock(ctx, arguments)
	case ToolMetadataUpdate:
		return tools.updateMetadata(ctx, arguments)
	case ToolDocumentValidate:
		return tools.validate(ctx, arguments)
	case ToolDocumentApply:
		return tools.apply(ctx, arguments)
	default:
		return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
	}
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
	Text   string       `json:"text"`
}

type paragraphUpdateArguments struct {
	focusedMutationArguments
	Block core.BlockID `json:"block_id"`
	Text  string       `json:"text"`
}

type blockDeleteArguments struct {
	focusedMutationArguments
	Block core.BlockID `json:"block_id"`
}

type metadataUpdateArguments struct {
	focusedMutationArguments
	Title        *string   `json:"title,omitempty"`
	Summary      *string   `json:"summary,omitempty"`
	ClearSummary bool      `json:"clear_summary,omitempty"`
	CategoryIDs  *[]string `json:"category_ids,omitempty"`
	TagIDs       *[]string `json:"tag_ids,omitempty"`
}

func (tools *AIDocumentTools) open(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input openArguments
	if err := decodeArguments(arguments, &input); err != nil {
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

func (tools *AIDocumentTools) read(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input readArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if err := validateMCPDocumentReference(input.Document); err != nil {
		return executionError(err)
	}
	request := core.ReadRequest{
		Document: core.DocumentIdentity{Domain: input.Profile, Reference: input.Document}, Locale: input.Locale,
		Mode: input.Mode, Blocks: input.Blocks, Limit: input.Limit, Cursor: input.Cursor,
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
		return expectedToolError(err)
	}
	encoded, err := core.EncodeProjection(projection)
	if err != nil {
		return mcpserver.ToolResult{}, fmt.Errorf("encode MCP document projection: %w", err)
	}
	return structuredResult(encoded, false)
}

func (tools *AIDocumentTools) createParagraph(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input paragraphCreateArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	block := core.BlockID(uuid.NewString())
	request, err := focusedApplyRequest(input.focusedMutationArguments, []core.Operation{
		core.InsertBlockOperation(block, "paragraph", input.Parent, input.After),
		core.SetFieldOperation(block, "content", core.RichText(core.InlineText(input.Text))),
	})
	if err != nil {
		return executionError(err)
	}
	return tools.applyRequest(ctx, request, block)
}

func (tools *AIDocumentTools) updateParagraph(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input paragraphUpdateArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	request, err := focusedApplyRequest(input.focusedMutationArguments, []core.Operation{
		core.SetFieldOperation(input.Block, "content", core.RichText(core.InlineText(input.Text))),
	})
	if err != nil {
		return executionError(err)
	}
	return tools.applyRequest(ctx, request, "")
}

func (tools *AIDocumentTools) deleteBlock(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input blockDeleteArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	request, err := focusedApplyRequest(input.focusedMutationArguments, []core.Operation{core.DeleteBlockOperation(input.Block)})
	if err != nil {
		return executionError(err)
	}
	return tools.applyRequest(ctx, request, "")
}

func (tools *AIDocumentTools) updateMetadata(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input metadataUpdateArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if input.Summary != nil && input.ClearSummary {
		return executionError(errors.New("summary and clear_summary cannot be used together"))
	}
	if input.Profile != core.DomainPost && (input.CategoryIDs != nil || input.TagIDs != nil) {
		return executionError(errors.New("category_ids and tag_ids are supported only for Post documents"))
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

func (tools *AIDocumentTools) validate(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	request, err := decodeMutation(arguments)
	if err != nil {
		return executionError(err)
	}
	validation, err := tools.application.Validate(ctx, request)
	if err != nil {
		return expectedToolError(err)
	}
	encoded, err := encodeValidation(validation)
	if err != nil {
		return mcpserver.ToolResult{}, fmt.Errorf("encode MCP document validation: %w", err)
	}
	return structuredResult(encoded, false)
}

func (tools *AIDocumentTools) apply(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	request, err := decodeMutation(arguments)
	if err != nil {
		return executionError(err)
	}
	return tools.applyRequest(ctx, request, "")
}

func (tools *AIDocumentTools) applyRequest(ctx context.Context, request core.ApplyRequest, createdBlock core.BlockID) (mcpserver.ToolResult, error) {
	result, err := tools.application.Apply(ctx, request)
	if err != nil {
		var validationError *core.ValidationError
		if errors.As(err, &validationError) {
			encoded, encodeErr := encodeValidation(validationError.Result)
			if encodeErr != nil {
				return mcpserver.ToolResult{}, fmt.Errorf("encode MCP document rejection: %w", encodeErr)
			}
			return structuredResult(encoded, true)
		}
		var conflictError *core.ConflictError
		if errors.As(err, &conflictError) {
			encoded, encodeErr := encodeValidation(core.ValidationResult{Conflict: &conflictError.Conflict})
			if encodeErr != nil {
				return mcpserver.ToolResult{}, fmt.Errorf("encode MCP document conflict: %w", encodeErr)
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

func encodeFocusedAccepted(result core.ApplyResult, createdBlock core.BlockID) ([]byte, error) {
	encoded, err := encodeAccepted(result)
	if err != nil || createdBlock == "" {
		return encoded, err
	}
	var output map[string]any
	if err := json.Unmarshal(encoded, &output); err != nil {
		return nil, err
	}
	output["block_id"] = createdBlock
	return json.Marshal(output)
}

func decodeArguments(arguments mcpserver.ToolArguments, target any) error {
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("multiple argument values are not allowed")
	}
	return nil
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

func encodeValidation(validation core.ValidationResult) ([]byte, error) {
	output := make(map[string]any, 3)
	if len(validation.Normalized) != 0 {
		for _, operation := range validation.Normalized {
			if _, err := json.Marshal(operation); err != nil {
				return nil, fmt.Errorf("invalid normalized operation: %w", err)
			}
		}
		output["o"] = validation.Normalized
	}
	if len(validation.Issues) != 0 {
		issues := make([][4]any, 0, len(validation.Issues))
		for _, issue := range validation.Issues {
			if issue.Operation < 0 {
				return nil, errors.New("application returned a negative validation operation index")
			}
			if !supportedIssueCode(issue.Code) {
				return nil, fmt.Errorf("application returned unsupported validation issue code %q", issue.Code)
			}
			issues = append(issues, [4]any{issue.Operation, issue.Code, issue.Handle, issue.Message})
		}
		output["i"] = issues
	}
	if validation.Conflict != nil {
		if validation.Conflict.Code != core.ConflictDocumentRevision &&
			validation.Conflict.Code != core.ConflictTargetRevision {
			return nil, fmt.Errorf("application returned unsupported conflict code %q", validation.Conflict.Code)
		}
		if validation.Conflict.CurrentDocumentRevision == "" {
			return nil, errors.New("application returned a conflict without current document revision")
		}
		output["x"] = [4]any{
			validation.Conflict.Code,
			validation.Conflict.CurrentDocumentRevision,
			validation.Conflict.CurrentTargetRevision,
			append([]string(nil), validation.Conflict.AffectedHandles...),
		}
	}
	return json.Marshal(output)
}

func encodeAccepted(result core.ApplyResult) ([]byte, error) {
	if result.DocumentRevision == "" {
		return nil, errors.New("application returned an accepted mutation without document revision")
	}
	changes := make([][3]any, 0, len(result.Changes))
	for _, change := range result.Changes {
		if change.Operation < 0 {
			return nil, errors.New("application returned a negative accepted operation index")
		}
		if !supportedOperationKind(change.Kind) {
			return nil, fmt.Errorf("application returned unsupported accepted operation kind %q", change.Kind)
		}
		changes = append(changes, [3]any{
			change.Operation,
			change.Kind,
			append([]string(nil), change.AffectedHandles...),
		})
	}
	output := map[string]any{"dr": result.DocumentRevision, "c": changes}
	if result.TargetRevision != nil {
		output["tr"] = result.TargetRevision
	}
	return json.Marshal(output)
}

func supportedOperationKind(kind core.OperationKind) bool {
	switch kind {
	case core.OperationSetField, core.OperationUnsetField,
		core.OperationInsertBlock, core.OperationDeleteBlock,
		core.OperationMoveBlock, core.OperationReplaceBlockKind,
		core.OperationInsertRelationItem, core.OperationDeleteRelationItem,
		core.OperationMoveRelationItem, core.OperationAttachFile,
		core.OperationDetachFile, core.OperationCreateTranslation,
		core.OperationDeleteTranslation:
		return true
	default:
		return false
	}
}

func supportedIssueCode(code core.IssueCode) bool {
	switch code {
	case core.IssueInvalidOperation, core.IssueUnknownBlock,
		core.IssueDuplicateBlock, core.IssueUnknownBlockKind,
		core.IssueUnknownField, core.IssueUnknownRelation,
		core.IssueUnknownRelationItem, core.IssueDuplicateRelationItem,
		core.IssueInvalidRelationItemMove, core.IssueValueKindMismatch,
		core.IssueSourceAuthorityRequired, core.IssueTargetFieldForbidden,
		core.IssueInvalidBlockRelation, core.IssueBlockCycle,
		core.IssueInvalidFileReference, core.IssueTranslationIsSource,
		core.IssueTranslationAlreadyExists, core.IssueTranslationMissing,
		core.IssueLocaleOperationNotExclusive:
		return true
	default:
		return false
	}
}

func structuredResult(encoded []byte, isError bool) (mcpserver.ToolResult, error) {
	var structured map[string]any
	if err := json.Unmarshal(encoded, &structured); err != nil {
		return mcpserver.ToolResult{}, err
	}
	return mcpserver.ToolResult{
		Content:           []mcpserver.ContentBlock{mcpserver.TextContent(string(encoded))},
		StructuredContent: structured,
		IsError:           isError,
	}, nil
}

func executionError(err error) (mcpserver.ToolResult, error) {
	return mcpserver.ToolResult{}, &mcpserver.ToolExecutionError{Message: err.Error()}
}

func cloneAnnotations(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	result := make(map[string]any, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

var (
	_ mcpserver.ToolRegistry   = (*AIDocumentTools)(nil)
	_ mcpserver.ToolDispatcher = (*AIDocumentTools)(nil)
	_ ToolProvider             = (*AIDocumentTools)(nil)
)
