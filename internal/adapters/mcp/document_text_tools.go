package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/documenttext"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

const (
	ToolTextReplace = "document_text_replace"
	ToolTextFormat  = "document_text_format"

	// textEditMaxEdits bounds the edits of one call.
	textEditMaxEdits = 500
)

const textEditCommonDescription = "Every edit of one document applies together or not at all, across its locales; each document stands alone, so a failure in one leaves the others applied and reports which. " +
	"Needs no read and no revisions: find guards the edit, and a document that changes meanwhile is retried once. " +
	"With dry_run the whole edit runs and rolls back, so the result shows what would change without changing anything; check all_occurrences this way. "

var documentTextTools = toolRegistry[DocumentTextTools]{
	defineTool(mcpserver.Tool{
		Name: ToolTextReplace, Title: "Replace text in documents",
		Description: "Replace exact text (a word or sentence) in one or many documents and locales in one call, keeping the marks, links and colors around it: the cheapest way to change text. " +
			"find must occur exactly once in that locale's text, copied exactly (case and spacing matter), within block_id when given or else anywhere in the document, unless all_occurrences changes every match. An empty replace deletes it. " +
			"To change a translation's words the same way, add an edit with that locale. To overwrite a translation instead, add translations to the source edit: it sets the whole translated text of the unit holding the replacement. " +
			textEditCommonDescription + "It changes nothing in a document whose find is missing or occurs more than once, and says so.",
		InputSchema: json.RawMessage(textReplaceInputJSONSchema), OutputSchema: json.RawMessage(textEditOutputJSONSchema),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, true, false),
		Meta:            oauthSecurityMeta(),
	}, (*DocumentTextTools).replaceText),
	defineTool(mcpserver.Tool{
		Name: ToolTextFormat, Title: "Format text in documents",
		Description: "Change links and formatting in one or many documents and locales in one call, without rewriting the text. Select with find (exact text, which may span existing marks) or find_link (an exact link address), then set link, add_marks, remove_marks, text_color or background_color; an empty link or color removes it. " +
			"For example, find_link with link moves every use of an address to a new one. The selection must match exactly once unless all_occurrences, within block_id when given. " +
			textEditCommonDescription,
		InputSchema: json.RawMessage(textFormatInputJSONSchema), OutputSchema: json.RawMessage(textEditOutputJSONSchema),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, true, false),
		Meta:            oauthSecurityMeta(),
	}, (*DocumentTextTools).formatText),
}

// DocumentTextEditor is the use case behind the text tools.
type DocumentTextEditor interface {
	EditDocuments(ctx context.Context, edits []documenttext.Edit, dryRun bool) ([]documenttext.DocumentResult, error)
}

// DocumentTextTools translates text tool calls into the text edit use case.
type DocumentTextTools struct {
	editor DocumentTextEditor
}

func NewDocumentTextTools(editor DocumentTextEditor) (*DocumentTextTools, error) {
	if interfaceValueIsNil(editor) {
		return nil, errors.New("MCP document text editor is required")
	}
	return &DocumentTextTools{editor: editor}, nil
}

func (*DocumentTextTools) ToolNames() []string { return documentTextTools.names() }

func (*DocumentTextTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return documentTextTools.list(), nil
}

func (tools *DocumentTextTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	return documentTextTools.call(tools, ctx, name, arguments)
}

// textEditScope is where one edit applies.
type textEditScope struct {
	DocumentType   core.Domain            `json:"document_type"`
	DocumentID     core.DocumentReference `json:"document_id"`
	Locale         core.Locale            `json:"locale"`
	Block          core.BlockID           `json:"block_id,omitempty"`
	AllOccurrences bool                   `json:"all_occurrences,omitempty"`
}

func (scope textEditScope) edit(textEdit core.TextEdit, translations map[string]string) documenttext.Edit {
	textEdit.Block, textEdit.AllOccurrences = scope.Block, scope.AllOccurrences
	return documenttext.Edit{
		Document: core.DocumentIdentity{Domain: scope.DocumentType, Reference: scope.DocumentID},
		Locale:   scope.Locale, TextEdit: textEdit, Translations: translations,
	}
}

type textReplaceEdit struct {
	textEditScope
	Find         *string           `json:"find"`
	Replace      *string           `json:"replace"`
	Translations map[string]string `json:"translations,omitempty"`
}

type textReplaceArguments struct {
	Edits  []textReplaceEdit `json:"edits"`
	DryRun bool              `json:"dry_run,omitempty"`
}

type textFormatEdit struct {
	textEditScope
	Find            string            `json:"find,omitempty"`
	FindLink        string            `json:"find_link,omitempty"`
	Link            *string           `json:"link,omitempty"`
	AddMarks        []core.InlineKind `json:"add_marks,omitempty"`
	RemoveMarks     []core.InlineKind `json:"remove_marks,omitempty"`
	TextColor       *string           `json:"text_color,omitempty"`
	BackgroundColor *string           `json:"background_color,omitempty"`
}

type textFormatArguments struct {
	Edits  []textFormatEdit `json:"edits"`
	DryRun bool             `json:"dry_run,omitempty"`
}

func (tools *DocumentTextTools) replaceText(ctx context.Context, _ string, call toolCall[textReplaceArguments]) (mcpserver.ToolResult, error) {
	var input textReplaceArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	edits := make([]documenttext.Edit, 0, len(input.Edits))
	for index, edit := range input.Edits {
		if edit.Find == nil || edit.Replace == nil {
			return executionError(fmt.Errorf("edit %d needs find and replace", index))
		}
		if *edit.Find == "" {
			return executionError(fmt.Errorf("edit %d: find cannot be empty", index))
		}
		if len(edit.Translations) != 0 && *edit.Replace == "" {
			return executionError(fmt.Errorf("edit %d: translations need a non-empty replace to find their unit by", index))
		}
		edits = append(edits, edit.textEditScope.edit(core.TextEdit{Find: *edit.Find, Replace: edit.Replace}, edit.Translations))
	}
	return tools.applyEdits(ctx, edits, input.DryRun)
}

func (tools *DocumentTextTools) formatText(ctx context.Context, _ string, call toolCall[textFormatArguments]) (mcpserver.ToolResult, error) {
	var input textFormatArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	edits := make([]documenttext.Edit, 0, len(input.Edits))
	for _, edit := range input.Edits {
		edits = append(edits, edit.textEditScope.edit(core.TextEdit{Find: edit.Find, FindLink: edit.FindLink, Format: &core.InlineFormat{
			AddMarks: edit.AddMarks, RemoveMarks: edit.RemoveMarks, Link: edit.Link, TextColor: edit.TextColor, BackgroundColor: edit.BackgroundColor,
		}}, nil))
	}
	return tools.applyEdits(ctx, edits, input.DryRun)
}

func (tools *DocumentTextTools) applyEdits(ctx context.Context, edits []documenttext.Edit, dryRun bool) (mcpserver.ToolResult, error) {
	if len(edits) == 0 || len(edits) > textEditMaxEdits {
		return executionError(fmt.Errorf("edits must contain 1 to %d edits", textEditMaxEdits))
	}
	for index, edit := range edits {
		if err := validateMCPDocumentReference(edit.Document.Reference); err != nil {
			return executionError(fmt.Errorf("edit %d: %w", index, err))
		}
		if err := requireLocale(edit.Locale, "locale"); err != nil {
			return executionError(fmt.Errorf("edit %d: %w", index, err))
		}
		if edit.TextEdit.Block == "" && edit.TextEdit.Find != "" && strings.TrimSpace(edit.TextEdit.Find) == "" {
			return executionError(fmt.Errorf("edit %d: find made only of spaces needs block_id", index))
		}
	}
	documents, err := tools.editor.EditDocuments(ctx, edits, dryRun)
	if err != nil {
		return expectedToolError(err)
	}
	results := make([]textEditDocumentResult, len(documents))
	for index, document := range documents {
		results[index] = newTextEditDocumentResult(ctx, document, dryRun)
	}
	return encodeDocumentBatch(results)
}

type textEditLocaleResult struct {
	Locale           core.Locale            `json:"locale"`
	DocumentRevision core.Revision          `json:"document_revision,omitempty"`
	TargetRevision   *core.Revision         `json:"target_revision,omitempty"`
	Blocks           []core.BlockID         `json:"blocks"`
	Changes          []core.TextEditPreview `json:"changes,omitempty"`
}

type textEditDocumentResult struct {
	documentOutcome
	EditIndex    *int                                   `json:"edit_index,omitempty"`
	Locales      []textEditLocaleResult                 `json:"locales,omitempty"`
	Translations map[string]translationEditLocaleResult `json:"translations,omitempty"`
}

func newTextEditDocumentResult(ctx context.Context, document documenttext.DocumentResult, dryRun bool) textEditDocumentResult {
	result := textEditDocumentResult{documentOutcome: documentOutcome{
		DocumentType: document.Document.Domain, DocumentID: document.Document.Reference, Status: documentStatusApplied,
	}}
	if document.Err != nil {
		result.recordFailure(ctx, document.Err)
		return result
	}
	if dryRun {
		result.Status = documentStatusWouldApply
	}
	for _, locale := range document.Locales {
		result.Locales = append(result.Locales, textEditLocaleResult{
			Locale: locale.Locale, DocumentRevision: locale.DocumentRevision, TargetRevision: locale.TargetRevision,
			Blocks: locale.Blocks, Changes: locale.Previews,
		})
	}
	if document.Translations != nil {
		result.Translations = translationEditLocaleResults(document.Translations)
	}
	if dryRun {
		// Rolled back with the writes: no revision of these exists.
		for locale, translation := range result.Translations {
			translation.TargetRevision = ""
			result.Translations[locale] = translation
		}
	}
	return result
}

// recordFailure explains why the document's edit did not apply.
func (result *textEditDocumentResult) recordFailure(ctx context.Context, err error) {
	var refusal *documenttext.RefusalError
	var validationError *core.ValidationError
	result.Status = documentStatusFailed
	switch {
	case errors.As(err, &refusal):
		result.Error, result.EditIndex = refusal.Message, refusal.EditIndex
	case documenttext.IsConflict(err):
		result.Error = "another write reached the document during this edit, twice; this edit was not applied, so send it again"
	case errors.As(err, &validationError) && len(validationError.Result.Issues) != 0:
		messages := make([]string, 0, len(validationError.Result.Issues))
		for _, issue := range validationError.Result.Issues {
			messages = append(messages, issue.Message)
		}
		result.Error = strings.Join(messages, "; ")
	default:
		_, toolErr := expectedToolError(err)
		result.fail(ctx, toolErr)
	}
}

var (
	_ mcpserver.ToolRegistry   = (*DocumentTextTools)(nil)
	_ mcpserver.ToolDispatcher = (*DocumentTextTools)(nil)
	_ ToolProvider             = (*DocumentTextTools)(nil)
)
