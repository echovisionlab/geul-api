package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/documentsearch"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

const ToolDocumentSearch = "document_search"

const (
	// documentSearchDefaultBlocks and documentSearchMaxBlocks bound the blocks one
	// document adds to a result.
	documentSearchDefaultBlocks = 20
	documentSearchMaxBlocks     = 256
	documentSearchMaxLocales    = 64
)

var documentSearchTools = toolRegistry[DocumentSearchTools]{
	defineTool(mcpserver.Tool{
		Name: ToolDocumentSearch, Title: "Search document text",
		Description: "Find which documents contain some text, across every document type's bodies, titles and slugs, including link addresses, ignoring case; runs of spaces count as one. Use it when you do not know which document holds a sentence, a label or a link; document_list only matches titles. " +
			"Each match gives its document_id, slug and title, and the matching blocks with a preview of each; document_text_replace and document_text_format can act on them with no further read. " +
			"Searches each document's source locale, or the given locales. Omit document_type to search every type. One call reads up to 100 candidates: continue with next_offset until it is null.",
		InputSchema: json.RawMessage(documentSearchInputJSONSchema), OutputSchema: json.RawMessage(documentSearchOutputJSONSchema),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(true, false, false),
		Meta:            oauthSecurityMeta(),
	}, (*DocumentSearchTools).search),
}

// DocumentSearcher is the use case behind document_search.
type DocumentSearcher interface {
	Search(ctx context.Context, query documentsearch.Query) (documentsearch.Result, error)
}

// DocumentSearchTools translates document_search calls into the search use case.
type DocumentSearchTools struct {
	searcher DocumentSearcher
}

func NewDocumentSearchTools(searcher DocumentSearcher) (*DocumentSearchTools, error) {
	if interfaceValueIsNil(searcher) {
		return nil, errors.New("MCP document searcher is required")
	}
	return &DocumentSearchTools{searcher: searcher}, nil
}

// DocumentSearchReader reads documents for a search, telling apart a read the caller
// may not make from one that failed.
type DocumentSearchReader struct {
	application AIDocumentApplication
}

func NewDocumentSearchReader(application AIDocumentApplication) (*DocumentSearchReader, error) {
	if interfaceValueIsNil(application) {
		return nil, errors.New("MCP document search reader needs the document application")
	}
	return &DocumentSearchReader{application: application}, nil
}

func (reader *DocumentSearchReader) Read(ctx context.Context, request core.ReadRequest) (core.Projection, error) {
	projection, err := reader.application.Read(ctx, request)
	if err == nil {
		return projection, nil
	}
	switch errs.CodeOf(err) {
	case connect.CodeNotFound, connect.CodePermissionDenied, connect.CodeUnauthenticated:
		return core.Projection{}, fmt.Errorf("%w: %w", documentsearch.ErrAccessRefused, err)
	}
	return core.Projection{}, err
}

func (*DocumentSearchTools) ToolNames() []string { return documentSearchTools.names() }

func (*DocumentSearchTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return documentSearchTools.list(), nil
}

func (tools *DocumentSearchTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	return documentSearchTools.call(tools, ctx, name, arguments)
}

type documentSearchArguments struct {
	Query        string        `json:"query"`
	DocumentType core.Domain   `json:"document_type,omitempty"`
	Locales      []core.Locale `json:"locales,omitempty"`
	BlockLimit   int           `json:"block_limit,omitempty"`
	Offset       int           `json:"offset,omitempty"`
	Exhaustive   bool          `json:"exhaustive,omitempty"`
}

// normalize checks the arguments, fills in their defaults, and returns the
// document types to search.
func (input *documentSearchArguments) normalize() ([]core.Domain, error) {
	input.Query = strings.TrimSpace(input.Query)
	if input.Query == "" || utf8.RuneCountInString(input.Query) > core.MaxReadQueryRunes {
		return nil, fmt.Errorf("query must contain 1 to %d characters", core.MaxReadQueryRunes)
	}
	if input.Offset < 0 || input.Offset > documentListMaxOffset {
		return nil, fmt.Errorf("offset must be between 0 and %d", documentListMaxOffset)
	}
	if input.BlockLimit == 0 {
		input.BlockLimit = documentSearchDefaultBlocks
	}
	if input.BlockLimit < 1 || input.BlockLimit > documentSearchMaxBlocks {
		return nil, fmt.Errorf("block_limit must be between 1 and %d", documentSearchMaxBlocks)
	}
	if len(input.Locales) > documentSearchMaxLocales {
		return nil, fmt.Errorf("locales must contain at most %d locales", documentSearchMaxLocales)
	}
	for _, locale := range input.Locales {
		if err := validateLocaleCode(locale); err != nil {
			return nil, err
		}
	}
	if input.DocumentType == "" {
		return listedDocumentTypes, nil
	}
	if !slices.Contains(listedDocumentTypes, input.DocumentType) {
		return nil, unknownDocumentTypeError()
	}
	return []core.Domain{input.DocumentType}, nil
}

type documentSearchBlock struct {
	Block   core.BlockID `json:"block_id"`
	Preview string       `json:"preview,omitempty"`
}

type documentSearchMatch struct {
	DocumentType core.Domain            `json:"document_type"`
	DocumentID   core.DocumentReference `json:"document_id"`
	Title        string                 `json:"title"`
	Slug         *string                `json:"slug,omitempty"`
	SourceLocale core.Locale            `json:"source_locale"`
	Locale       core.Locale            `json:"locale,omitempty"`
	Blocks       []documentSearchBlock  `json:"blocks,omitempty"`
	MoreBlocks   int                    `json:"more_blocks,omitempty"`
}

type documentSearchOutput struct {
	Matches    []documentSearchMatch `json:"matches"`
	Scanned    int                   `json:"scanned"`
	Unreadable int                   `json:"unreadable,omitempty"`
	NextOffset *int                  `json:"next_offset"`
}

func newDocumentSearchOutput(result documentsearch.Result) documentSearchOutput {
	output := documentSearchOutput{
		Matches: make([]documentSearchMatch, len(result.Matches)),
		Scanned: result.Scanned, Unreadable: result.Unreadable, NextOffset: result.NextOffset,
	}
	for index, match := range result.Matches {
		blocks := make([]documentSearchBlock, len(match.Blocks))
		for position, block := range match.Blocks {
			blocks[position] = documentSearchBlock{Block: block.Block, Preview: block.Preview}
		}
		output.Matches[index] = documentSearchMatch{
			DocumentType: match.Document.DocumentType, DocumentID: match.Document.DocumentID, Title: match.Document.Title,
			Slug: match.Document.Slug, SourceLocale: match.Document.SourceLocale,
			Locale: match.Locale, Blocks: blocks, MoreBlocks: match.MoreBlocks,
		}
	}
	return output
}

func (tools *DocumentSearchTools) search(ctx context.Context, _ string, call toolCall[documentSearchArguments]) (mcpserver.ToolResult, error) {
	if err := rejectNullArguments(call.Arguments, "document_type", "locales", "block_limit", "offset", "exhaustive"); err != nil {
		return executionError(err)
	}
	var input documentSearchArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	documentTypes, err := input.normalize()
	if err != nil {
		return executionError(err)
	}
	result, err := tools.searcher.Search(ctx, documentsearch.Query{
		Text: input.Query, DocumentTypes: documentTypes, Locales: input.Locales,
		BlockLimit: input.BlockLimit, Offset: input.Offset, Exhaustive: input.Exhaustive,
	})
	if errors.Is(err, documentsearch.ErrIndexUnavailable) {
		slog.ErrorContext(ctx, "MCP document search index failed", failurecause.Attr(err))
		return executionError(errors.New("the search index could not answer; try again, or pass exhaustive to read every document"))
	}
	if err != nil {
		return expectedToolError(err)
	}
	encoded, err := json.Marshal(newDocumentSearchOutput(result))
	if err != nil {
		return mcpserver.ToolResult{}, fmt.Errorf("encode MCP document search: %w", err)
	}
	return structuredResult(encoded, false)
}

var (
	_ mcpserver.ToolRegistry   = (*DocumentSearchTools)(nil)
	_ mcpserver.ToolDispatcher = (*DocumentSearchTools)(nil)
	_ ToolProvider             = (*DocumentSearchTools)(nil)
)
