package mcp

import (
	"context"
	"errors"
	"reflect"
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
)

const (
	searchDocumentA = "44444444-4444-4444-8444-444444444441"
	searchDocumentB = "44444444-4444-4444-8444-444444444442"
	searchDocumentC = "44444444-4444-4444-8444-444444444443"
)

// searchDocuments answers outline reads per document, as the owning domains do.
type searchDocuments struct {
	recordingAIDocumentApplication
	nodes map[core.DocumentReference][]core.Node
	fail  map[core.DocumentReference]bool
	reads []core.ReadRequest
}

func (documents *searchDocuments) Read(_ context.Context, request core.ReadRequest) (core.Projection, error) {
	documents.reads = append(documents.reads, request)
	if documents.fail[request.Document.Reference] {
		return core.Projection{}, errors.New("unreadable")
	}
	previews := map[core.BlockID]string{}
	for _, node := range documents.nodes[request.Document.Reference] {
		previews[node.ID] = "around " + string(node.ID)
	}
	return core.Projection{Nodes: documents.nodes[request.Document.Reference], Previews: previews}, nil
}

func mustDocumentSearchTools(t *testing.T, posts []postdomain.AIDocumentListItem, documents *searchDocuments) *DocumentSearchTools {
	t.Helper()
	discovery, err := NewDocumentDiscoveryTools(
		&recordingPostDocumentDiscovery{result: postdomain.AIDocumentListResult{Items: posts, Total: int64(len(posts)), Limit: 50}},
		&recordingWorkDocumentDiscovery{}, &recordingPageDocumentDiscovery{}, &recordingProgramEventDocumentDiscovery{},
		&recordingReleaseDocumentDiscovery{}, &recordingArtistDocumentDiscovery{},
	)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := NewDocumentSearchTools(discovery, documents)
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

func TestDocumentSearchFindsBodiesAndNamesAndSkipsUnreadableDocuments(t *testing.T) {
	slug := "products/topofon"
	posts := []postdomain.AIDocumentListItem{
		{ID: searchDocumentA, Title: "First", SourceLocale: "ko"},
		{ID: searchDocumentB, Title: "Second", Slug: &slug, SourceLocale: "en"},
		{ID: searchDocumentC, Title: "Third", SourceLocale: "ko"},
	}
	var manyBlocks []core.Node
	for _, id := range []core.BlockID{"b1", "b2", "b3", "b4", "b5", "b6", "b7"} {
		manyBlocks = append(manyBlocks, core.Node{ID: id, Kind: "paragraph"})
	}
	documents := &searchDocuments{
		nodes: map[core.DocumentReference][]core.Node{searchDocumentA: manyBlocks},
		fail:  map[core.DocumentReference]bool{searchDocumentC: true},
	}

	result, err := mustDocumentSearchTools(t, posts, documents).CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSearch, toolArguments(t, `{"q":"TOPOFON","p":"post"}`))

	if err != nil || result.IsError {
		t.Fatalf("document_search = %+v, %v", result, err)
	}
	matches := result.StructuredContent["matches"].([]any)
	if len(matches) != 2 {
		t.Fatalf("matches = %+v", matches)
	}
	first := matches[0].(map[string]any)
	if first["d"] != searchDocumentA || len(first["blocks"].([]any)) != documentSearchBlocksPerDocument || first["more_blocks"] != float64(2) {
		t.Fatalf("body match = %+v", first)
	}
	if !reflect.DeepEqual(first["blocks"].([]any)[0], []any{"b1", "around b1"}) {
		t.Fatalf("block = %+v", first["blocks"])
	}
	if second := matches[1].(map[string]any); second["d"] != searchDocumentB || second["slug"] != slug || second["blocks"] != nil {
		t.Fatalf("slug match = %+v", second)
	}
	if result.StructuredContent["scanned"] != float64(3) || result.StructuredContent["unreadable"] != float64(1) || result.StructuredContent["next_offset"] != nil {
		t.Fatalf("progress = %+v", result.StructuredContent)
	}
	if documents.reads[0].Locale != "ko" || documents.reads[1].Locale != "en" {
		t.Fatalf("each document must be read in its source locale: %+v", documents.reads)
	}
}

func TestDocumentSearchReadsARequestedLocaleAndContinuesWithOffset(t *testing.T) {
	posts := make([]postdomain.AIDocumentListItem, 0, documentSearchScanLimit)
	for range documentSearchScanLimit {
		posts = append(posts, postdomain.AIDocumentListItem{ID: searchDocumentA, Title: "Post", SourceLocale: "ko"})
	}
	documents := &searchDocuments{}
	discovery := &recordingPostDocumentDiscovery{result: postdomain.AIDocumentListResult{Items: posts, Total: 60, Limit: 50}}
	tools := mustDocumentSearchTools(t, nil, documents)
	tools.discovery.posts = discovery

	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSearch, toolArguments(t, `{"q":"x","p":"post","l":"de"}`))

	if err != nil || result.StructuredContent["next_offset"] != float64(documentSearchScanLimit) || result.StructuredContent["total"] != float64(60) {
		t.Fatalf("document_search = %+v, %v", result, err)
	}
	if documents.reads[0].Locale != "de" {
		t.Fatalf("locale = %q", documents.reads[0].Locale)
	}
}

func TestDocumentSearchRejectsAnEmptyQueryAndAnUnknownType(t *testing.T) {
	documents := &searchDocuments{}
	for _, arguments := range []string{`{"q":"   "}`, `{"q":"x","p":"menu"}`, `{"q":"x","offset":-1}`} {
		if _, err := mustDocumentSearchTools(t, nil, documents).CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSearch, toolArguments(t, arguments)); err == nil {
			t.Fatalf("%s succeeded", arguments)
		}
	}
}
