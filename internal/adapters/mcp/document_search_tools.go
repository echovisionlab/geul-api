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

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

const ToolDocumentSearch = "document_search"

const (
	// documentSearchScanLimit bounds the documents read by one call; next_offset
	// continues the scan.
	documentSearchScanLimit = 50
	// documentSearchBlocksPerDocument keeps one document from filling the result.
	documentSearchBlocksPerDocument = 5
)

// documentSearchProfiles is the scan order when no profile is given.
var documentSearchProfiles = []string{"page", "post", "work", "program_event", "release", "artist"}

var documentSearchTools = toolRegistry[DocumentSearchTools]{
	defineTool(mcpserver.Tool{
		Name: ToolDocumentSearch, Title: "Search document text",
		Description: "Find which documents contain some text, across document bodies, titles and slugs, including link addresses, ignoring case. Use it when you do not know which document holds a sentence, a label or a link; document_list only matches titles. " +
			"Each match gives d, its slug and title, and up to five blocks as [handle, preview] to read or edit. Searches each document's source locale, or l when given. " +
			"Omit p to search every type. One call reads up to 50 documents: continue with next_offset until it is null.",
		InputSchema: json.RawMessage(documentSearchInputJSONSchema), OutputSchema: json.RawMessage(documentSearchOutputJSONSchema),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(true, false, false),
		Meta:            oauthSecurityMeta(),
	}, (*DocumentSearchTools).search),
}

const documentSearchInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["q"],
  "properties":{
    "q":{"type":"string","minLength":1,"maxLength":200,"description":"Text to find, ignoring case and spacing."},
    "p":{"enum":["page","post","work","program_event","release","artist"],"description":"Search only this document type. Omit to search every type."},
    "l":{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$","description":"Search this locale instead of each document's source locale."},
    "offset":{"type":"integer","minimum":0,"maximum":2147483647,"default":0,"description":"next_offset from the previous call."}
  }
}`

const documentSearchOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["matches","scanned","total","next_offset"],
  "properties":{
    "matches":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["p","d","title","source_locale"],"properties":{
      "p":{"type":"string","description":"Document type, passed as p to document_read."},
      "d":{"type":"string","description":"Document UUID, passed as d to document_read."},
      "title":{"type":"string"},"slug":{"type":"string"},"source_locale":{"type":"string"},
      "blocks":{"type":"array","description":"Matching blocks as [handle, preview around the match].","items":{"type":"array","prefixItems":[{"type":"string"},{"type":"string"}],"items":false,"minItems":2,"maxItems":2}},
      "more_blocks":{"type":"integer","minimum":1,"description":"Further matching blocks; list them with document_read m=outline q."}
    }}},
    "scanned":{"type":"integer","minimum":0,"description":"Documents read by this call."},
    "unreadable":{"type":"integer","minimum":1,"description":"Documents this call could not read, which were left out."},
    "total":{"type":"integer","minimum":0,"description":"Documents in the searched types."},
    "next_offset":{"type":["integer","null"],"description":"Pass as offset to continue; null when every document was read."}
  }
}`

// DocumentSearchTools answers "which document says this" by listing through the
// owning services and reading each document's outline, so authorization and the
// meaning of a match stay with the tools a caller would use by hand.
type DocumentSearchTools struct {
	discovery *DocumentDiscoveryTools
	documents AIDocumentApplication
}

func NewDocumentSearchTools(discovery *DocumentDiscoveryTools, documents AIDocumentApplication) (*DocumentSearchTools, error) {
	if discovery == nil {
		return nil, errors.New("MCP document search discovery is required")
	}
	if interfaceValueIsNil(documents) {
		return nil, errors.New("MCP document search application is required")
	}
	return &DocumentSearchTools{discovery: discovery, documents: documents}, nil
}

func (*DocumentSearchTools) ToolNames() []string { return documentSearchTools.names() }

func (*DocumentSearchTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return documentSearchTools.list(), nil
}

func (tools *DocumentSearchTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	return documentSearchTools.call(tools, ctx, name, arguments)
}

type documentSearchArguments struct {
	Query   string      `json:"q"`
	Profile string      `json:"p,omitempty"`
	Locale  core.Locale `json:"l,omitempty"`
	Offset  int         `json:"offset,omitempty"`
}

type documentSearchMatch struct {
	Profile      string      `json:"p"`
	Document     any         `json:"d"`
	Title        any         `json:"title"`
	Slug         any         `json:"slug,omitempty"`
	SourceLocale any         `json:"source_locale"`
	Blocks       [][2]string `json:"blocks,omitempty"`
	MoreBlocks   int         `json:"more_blocks,omitempty"`
}

type documentSearchOutput struct {
	Matches    []documentSearchMatch `json:"matches"`
	Scanned    int                   `json:"scanned"`
	Unreadable int                   `json:"unreadable,omitempty"`
	Total      int64                 `json:"total"`
	NextOffset *int                  `json:"next_offset"`
}

func (tools *DocumentSearchTools) search(ctx context.Context, _ string, call toolCall[documentSearchArguments]) (mcpserver.ToolResult, error) {
	if err := rejectNullArguments(call.Arguments, "p", "l", "offset"); err != nil {
		return executionError(err)
	}
	var input documentSearchArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	input.Query = strings.TrimSpace(input.Query)
	if input.Query == "" || utf8.RuneCountInString(input.Query) > core.MaxReadQueryRunes {
		return executionError(fmt.Errorf("q must contain 1 to %d characters", core.MaxReadQueryRunes))
	}
	if input.Offset < 0 || input.Offset > 1<<31-1 {
		return executionError(errors.New("offset must be between 0 and 2147483647"))
	}
	profiles := documentSearchProfiles
	if input.Profile != "" {
		if !slices.Contains(documentSearchProfiles, input.Profile) {
			return executionError(fmt.Errorf("p must be one of %s", strings.Join(documentSearchProfiles, ", ")))
		}
		profiles = []string{input.Profile}
	}
	if input.Locale != "" {
		if err := validateCompactLocale(input.Locale); err != nil {
			return executionError(err)
		}
	}
	output, err := tools.scan(ctx, profiles, input)
	if err != nil {
		return expectedToolError(err)
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return mcpserver.ToolResult{}, fmt.Errorf("encode MCP document search: %w", err)
	}
	return structuredResult(encoded, false)
}

// scan reads the profiles' lists as one sequence that offset indexes, up to the
// scan limit, and matches each listed document.
func (tools *DocumentSearchTools) scan(ctx context.Context, profiles []string, input documentSearchArguments) (documentSearchOutput, error) {
	output := documentSearchOutput{Matches: []documentSearchMatch{}}
	skip := input.Offset
	for _, profile := range profiles {
		remaining := documentSearchScanLimit - output.Scanned
		// With nothing left to read, this listing still adds to the total.
		listed, total, _, err := tools.discovery.listDocuments(ctx, profile, "", max(remaining, 1), skip)
		if err != nil {
			return documentSearchOutput{}, err
		}
		output.Total += total
		if int64(skip) >= total {
			skip -= int(total)
			continue
		}
		skip = 0
		if remaining <= 0 {
			continue
		}
		for _, document := range listed {
			output.Scanned++
			match, found, err := tools.matchDocument(ctx, profile, document, input)
			if err != nil {
				output.Unreadable++
				slog.WarnContext(ctx, "MCP document search skipped a document", failurecause.Attr(err), "domain", profile, "document_id", document["d"])
				continue
			}
			if found {
				output.Matches = append(output.Matches, match)
			}
		}
	}
	if next := input.Offset + output.Scanned; int64(next) < output.Total {
		output.NextOffset = &next
	}
	return output, nil
}

func (tools *DocumentSearchTools) matchDocument(ctx context.Context, profile string, document map[string]any, input documentSearchArguments) (documentSearchMatch, bool, error) {
	id, _ := document["d"].(string)
	locale := input.Locale
	if locale == "" {
		source, _ := document["source_locale"].(string)
		locale = core.Locale(source)
	}
	projection, err := tools.documents.Read(ctx, core.ReadRequest{
		Document: core.DocumentIdentity{Domain: core.Domain(profile), Reference: core.DocumentReference(id)},
		Locale:   locale, Mode: core.ReadOutline, Query: input.Query, Limit: defaultOutlineReadLimit,
	})
	if err != nil {
		return documentSearchMatch{}, false, err
	}
	match := documentSearchMatch{Profile: profile, Document: document["d"], Title: document["title"], Slug: document["slug"], SourceLocale: document["source_locale"]}
	for _, node := range projection.Nodes {
		if len(match.Blocks) == documentSearchBlocksPerDocument {
			match.MoreBlocks++
			continue
		}
		match.Blocks = append(match.Blocks, [2]string{string(node.ID), projection.Previews[node.ID]})
	}
	if len(match.Blocks) != 0 || documentSearchNameMatches(document, input.Query) {
		return match, true, nil
	}
	return documentSearchMatch{}, false, nil
}

func documentSearchNameMatches(document map[string]any, query string) bool {
	needle := strings.ToLower(query)
	for _, key := range []string{"title", "slug"} {
		if value, ok := document[key].(string); ok && strings.Contains(strings.ToLower(value), needle) {
			return true
		}
	}
	return false
}

var (
	_ mcpserver.ToolRegistry   = (*DocumentSearchTools)(nil)
	_ mcpserver.ToolDispatcher = (*DocumentSearchTools)(nil)
	_ ToolProvider             = (*DocumentSearchTools)(nil)
)
