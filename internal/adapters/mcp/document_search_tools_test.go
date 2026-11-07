package mcp

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/documentsearch"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/label"
	"github.com/echovisionlab/geul-api/internal/legal"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/menu"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/series"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

const (
	searchDocumentA = "44444444-4444-4444-8444-444444444441"
	searchDocumentB = "44444444-4444-4444-8444-444444444442"
	searchDocumentC = "44444444-4444-4444-8444-444444444443"
)

// searchDocuments answers outline reads per document, as the owning domains do.
type searchDocuments struct {
	recordingAIDocumentApplication
	mu    sync.Mutex
	nodes map[core.DocumentReference][]core.Node
	fail  map[core.DocumentReference]bool
	reads []core.ReadRequest
}

func (documents *searchDocuments) Read(_ context.Context, request core.ReadRequest) (core.Projection, error) {
	documents.mu.Lock()
	defer documents.mu.Unlock()
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

func (documents *searchDocuments) readsOf(document string) []core.ReadRequest {
	var reads []core.ReadRequest
	for _, read := range documents.reads {
		if string(read.Document.Reference) == document {
			reads = append(reads, read)
		}
	}
	return reads
}

func mustDocumentSearchTools(t *testing.T, posts []postdomain.AIDocumentListItem, documents *searchDocuments) *DocumentSearchTools {
	t.Helper()
	return mustDocumentSearchToolsWith(t, posts, documents, testOtherDocumentDiscovery())
}

func mustDocumentSearchToolsWith(t *testing.T, posts []postdomain.AIDocumentListItem, documents AIDocumentApplication, others DocumentDiscoveryDeps) *DocumentSearchTools {
	t.Helper()
	return mustIndexedSearchTools(t, posts, documents, others, &fakeSearchIndex{err: errSearchIndexUnavailable})
}

func mustIndexedSearchTools(t *testing.T, posts []postdomain.AIDocumentListItem, documents AIDocumentApplication, others DocumentDiscoveryDeps, index documentsearch.Index) *DocumentSearchTools {
	t.Helper()
	discovery, err := newDiscoveryToolsForTest(
		&recordingPostDocumentDiscovery{result: postdomain.AIDocumentListResult{Items: posts, Total: int64(len(posts)), Limit: 50}},
		&recordingWorkDocumentDiscovery{}, &recordingPageDocumentDiscovery{}, &recordingProgramEventDocumentDiscovery{},
		&recordingReleaseDocumentDiscovery{}, &recordingArtistDocumentDiscovery{}, others,
	)
	if err != nil {
		t.Fatal(err)
	}
	return mustSearchToolsOver(t, discovery, documents, index)
}

// readsOneByOne opens a batch as reads of the application, so the tests see each read.
type readsOneByOne struct{ AIDocumentApplication }

func (documents readsOneByOne) OpenBatch(context.Context, func(core.DocumentReference) bool, []core.LoadRequest) core.Reader {
	return documents
}

// everyDocumentScope is a caller who may read everything; its documents still decide.
type everyDocumentScope struct{}

func (everyDocumentScope) Resolve(context.Context, []core.Domain) (documentsearch.Scope, error) {
	return documentsearch.Scope{All: true}, nil
}

func mustSearchToolsOver(t *testing.T, lists documentsearch.Lister, documents AIDocumentApplication, index documentsearch.Index) *DocumentSearchTools {
	t.Helper()
	reader, err := NewDocumentSearchReader(readsOneByOne{documents})
	if err != nil {
		t.Fatal(err)
	}
	searcher, err := documentsearch.NewService(lists, reader, index, everyDocumentScope{})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := NewDocumentSearchTools(searcher)
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

	result, err := mustDocumentSearchTools(t, posts, documents).CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSearch, toolArguments(t, `{"exhaustive":true,"query":"TOPOFON","document_type":"post","block_limit":5}`))

	if err != nil || result.IsError {
		t.Fatalf("document_search = %+v, %v", result, err)
	}
	matches := result.StructuredContent["matches"].([]any)
	if len(matches) != 2 {
		t.Fatalf("matches = %+v", matches)
	}
	first := matches[0].(map[string]any)
	if first["document_id"] != searchDocumentA || len(first["blocks"].([]any)) != 5 || first["more_blocks"] != float64(2) {
		t.Fatalf("body match = %+v", first)
	}
	if !reflect.DeepEqual(first["blocks"].([]any)[0], map[string]any{"block_id": "b1", "preview": "around b1"}) {
		t.Fatalf("block = %+v", first["blocks"])
	}
	if second := matches[1].(map[string]any); second["document_id"] != searchDocumentB || second["slug"] != slug || second["blocks"] != nil {
		t.Fatalf("slug match = %+v", second)
	}
	if result.StructuredContent["scanned"] != float64(2) || result.StructuredContent["unreadable"] != float64(1) || result.StructuredContent["next_offset"] != nil {
		t.Fatalf("progress = %+v", result.StructuredContent)
	}
	if documents.readsOf(searchDocumentA)[0].Locale != "ko" || documents.readsOf(searchDocumentB)[0].Locale != "en" {
		t.Fatalf("each document must be read in its source locale: %+v", documents.reads)
	}
}

func TestDocumentSearchListsTwentyBlocksByDefault(t *testing.T) {
	var blocks []core.Node
	for index := range documentSearchDefaultBlocks + 3 {
		blocks = append(blocks, core.Node{ID: core.BlockID(syntheticUUID(index)), Kind: "paragraph"})
	}
	posts := []postdomain.AIDocumentListItem{{ID: searchDocumentA, Title: "First", SourceLocale: "ko"}}
	documents := &searchDocuments{nodes: map[core.DocumentReference][]core.Node{searchDocumentA: blocks}}

	result, err := mustDocumentSearchTools(t, posts, documents).CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSearch, toolArguments(t, `{"query":"x","document_type":"post","exhaustive":true}`))

	match := result.StructuredContent["matches"].([]any)[0].(map[string]any)
	if err != nil || len(match["blocks"].([]any)) != documentSearchDefaultBlocks || match["more_blocks"] != float64(3) {
		t.Fatalf("match = %+v, %v", match, err)
	}
}

func TestDocumentSearchReadsEveryRequestedLocaleAndContinuesWithOffset(t *testing.T) {
	posts := make([]postdomain.AIDocumentListItem, 0, documentsearch.ReadLimit)
	for range documentsearch.ReadLimit {
		posts = append(posts, postdomain.AIDocumentListItem{ID: searchDocumentA, Title: "Post", SourceLocale: "ko"})
	}
	documents := &searchDocuments{nodes: map[core.DocumentReference][]core.Node{searchDocumentA: {{ID: "b", Kind: "paragraph"}}}}
	discovery := &recordingPostDocumentDiscovery{result: postdomain.AIDocumentListResult{Items: posts, Total: documentsearch.ReadLimit + 20, Limit: documentsearch.ReadLimit}}
	listTools, err := newDiscoveryToolsForTest(discovery, &recordingWorkDocumentDiscovery{}, &recordingPageDocumentDiscovery{}, &recordingProgramEventDocumentDiscovery{}, &recordingReleaseDocumentDiscovery{}, &recordingArtistDocumentDiscovery{}, testOtherDocumentDiscovery())
	if err != nil {
		t.Fatal(err)
	}
	tools := mustSearchToolsOver(t, listTools, documents, &fakeSearchIndex{err: errSearchIndexUnavailable})

	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSearch, toolArguments(t, `{"exhaustive":true,"query":"x","document_type":"post","locales":["de","fr"]}`))

	if err != nil || result.StructuredContent["next_offset"] != float64(documentsearch.ReadLimit) {
		t.Fatalf("document_search = %+v, %v", result, err)
	}
	if len(documents.reads) != 2*documentsearch.ReadLimit {
		t.Fatalf("reads = %d", len(documents.reads))
	}
	matches := result.StructuredContent["matches"].([]any)
	if len(matches) != 2*documentsearch.ReadLimit {
		t.Fatalf("matches = %d", len(matches))
	}
	locales := map[any]bool{}
	for _, match := range matches[:2] {
		locales[match.(map[string]any)["locale"]] = true
	}
	if !locales["de"] || !locales["fr"] {
		t.Fatalf("each match must name its locale: %+v", matches[:2])
	}
}

// The gap this search closed: a site-wide change must also see the documents the
// site reuses, not only its main content.
func TestDocumentSearchCoversEveryDocumentType(t *testing.T) {
	others := testOtherDocumentDiscovery()
	fake := others.Labels.(*fakeOtherDocumentDiscovery)
	documents := &searchDocuments{nodes: map[core.DocumentReference][]core.Node{}}
	for _, id := range fake.ids() {
		documents.nodes[core.DocumentReference(id)] = []core.Node{{ID: "b", Kind: "paragraph"}}
	}

	result, err := mustDocumentSearchToolsWith(t, nil, documents, others).CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSearch, toolArguments(t, `{"query":"x","exhaustive":true}`))

	if err != nil || result.IsError {
		t.Fatalf("document_search = %+v, %v", result, err)
	}
	found := map[any]bool{}
	for _, match := range result.StructuredContent["matches"].([]any) {
		found[match.(map[string]any)["document_type"]] = true
	}
	for _, profile := range []string{"label", "menu", "form", "post_series", "terms", "privacy", "campaign", "email_template", "email_layout"} {
		if !found[profile] {
			t.Errorf("no match of type %s in %v", profile, found)
		}
	}
}

// A type the caller may not list must not stop the rest of the search, but must
// show up, or a site-wide change would quietly miss it.
func TestDocumentSearchCountsATypeItCouldNotList(t *testing.T) {
	others := testOtherDocumentDiscovery()
	others.Labels.(*fakeOtherDocumentDiscovery).labelsErr = errors.New("denied")
	documents := &searchDocuments{}
	tools := mustDocumentSearchToolsWith(t, nil, documents, others)

	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSearch, toolArguments(t, `{"query":"x","exhaustive":true}`))
	if err != nil || result.IsError || result.StructuredContent["unreadable"] != float64(1) {
		t.Fatalf("every type = %+v, %v", result.StructuredContent, err)
	}

	if _, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSearch, toolArguments(t, `{"query":"x","document_type":"label","exhaustive":true}`)); err == nil {
		t.Fatal("searching only the type that failed must report the failure")
	}
}

var errSearchIndexUnavailable = errors.New("function document_search_candidates does not exist")

type fakeSearchIndex struct {
	candidates []documentsearch.Document
	err        error
	calls      int
	scope      documentsearch.Scope
}

func (index *fakeSearchIndex) Candidates(_ context.Context, query documentsearch.IndexQuery) ([]documentsearch.Document, error) {
	index.calls++
	index.scope = query.Scope
	return index.candidates, index.err
}

type fixedSearchScope struct {
	scope documentsearch.Scope
	err   error
}

func (fixed fixedSearchScope) Resolve(context.Context, []core.Domain) (documentsearch.Scope, error) {
	return fixed.scope, fixed.err
}

func mustScopedSearchTools(t *testing.T, documents AIDocumentApplication, index documentsearch.Index, scopes documentsearch.Scopes) *DocumentSearchTools {
	t.Helper()
	discovery, err := newDiscoveryToolsForTest(
		&recordingPostDocumentDiscovery{}, &recordingWorkDocumentDiscovery{}, &recordingPageDocumentDiscovery{}, &recordingProgramEventDocumentDiscovery{},
		&recordingReleaseDocumentDiscovery{}, &recordingArtistDocumentDiscovery{}, testOtherDocumentDiscovery(),
	)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewDocumentSearchReader(readsOneByOne{documents})
	if err != nil {
		t.Fatal(err)
	}
	searcher, err := documentsearch.NewService(discovery, reader, index, scopes)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := NewDocumentSearchTools(searcher)
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

// The index pages only through what the caller may find, so a page's size and
// next_offset say nothing about anyone else's documents.
func TestDocumentSearchAsksTheIndexOnlyAboutTheCallersScope(t *testing.T) {
	scope := documentsearch.Scope{Documents: map[core.DocumentReference]struct{}{searchDocumentA: {}}}
	index := &fakeSearchIndex{}
	tools := mustScopedSearchTools(t, &searchDocuments{}, index, fixedSearchScope{scope: scope})

	if _, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSearch, toolArguments(t, `{"query":"x"}`)); err != nil {
		t.Fatal(err)
	}

	if index.calls != 1 || !index.scope.Allows(searchDocumentA) || index.scope.Allows(searchDocumentB) {
		t.Fatalf("calls = %d, scope = %+v", index.calls, index.scope)
	}
}

func TestDocumentSearchWithNothingInScopeAsksNoIndex(t *testing.T) {
	index := &fakeSearchIndex{}
	tools := mustScopedSearchTools(t, &searchDocuments{}, index, fixedSearchScope{})

	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSearch, toolArguments(t, `{"query":"x"}`))

	if err != nil || index.calls != 0 || len(result.StructuredContent["matches"].([]any)) != 0 || result.StructuredContent["next_offset"] != nil {
		t.Fatalf("err = %v, calls = %d, result = %+v", err, index.calls, result.StructuredContent)
	}
}

func TestDocumentSearchFailsWhenTheScopeCannotBeResolved(t *testing.T) {
	index := &fakeSearchIndex{}
	tools := mustScopedSearchTools(t, &searchDocuments{}, index, fixedSearchScope{err: errors.New("authorizer unavailable")})

	if _, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSearch, toolArguments(t, `{"query":"x"}`)); err == nil || index.calls != 0 {
		t.Fatalf("err = %v, calls = %d", err, index.calls)
	}
}

// An index that cannot answer must say so: reading the lists instead would give
// its offset another meaning and quietly lose documents from a search in progress.
func TestDocumentSearchReportsAnIndexThatCannotAnswer(t *testing.T) {
	documents := &searchDocuments{}
	tools := mustDocumentSearchTools(t, []postdomain.AIDocumentListItem{{ID: searchDocumentA, Title: "Listed", SourceLocale: "en"}}, documents)

	_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSearch, toolArguments(t, `{"query":"x"}`))

	var execution *mcpserver.ToolExecutionError
	if !errors.As(err, &execution) || !strings.Contains(execution.Message, "exhaustive") || strings.Contains(execution.Message, "document_search_candidates") || len(documents.reads) != 0 {
		t.Fatalf("err = %v, reads = %d", err, len(documents.reads))
	}
}

// The point of the index: only the candidates are read, however large the site.
func TestDocumentSearchReadsOnlyTheIndexCandidates(t *testing.T) {
	posts := []postdomain.AIDocumentListItem{{ID: searchDocumentB, Title: "Listed but no candidate", SourceLocale: "en"}}
	documents := &searchDocuments{nodes: map[core.DocumentReference][]core.Node{searchDocumentA: {{ID: "b", Kind: "paragraph"}}}}
	index := &fakeSearchIndex{candidates: []documentsearch.Document{{DocumentType: core.DomainPage, DocumentID: searchDocumentA, SourceLocale: "ko", Title: "From the index"}}}
	tools := mustIndexedSearchTools(t, posts, documents, testOtherDocumentDiscovery(), index)

	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSearch, toolArguments(t, `{"query":"x","offset":0}`))

	if err != nil || index.calls != 1 || len(documents.reads) != 1 || string(documents.reads[0].Document.Reference) != searchDocumentA {
		t.Fatalf("reads = %+v, err = %v", documents.reads, err)
	}
	match := result.StructuredContent["matches"].([]any)[0].(map[string]any)
	if match["title"] != "From the index" || match["document_type"] != "page" {
		t.Fatalf("match = %+v", match)
	}
	if result.StructuredContent["next_offset"] != nil {
		t.Fatalf("progress = %+v", result.StructuredContent)
	}
}

// The index asks for one more than a page, so a full page knows more follow.
func TestDocumentSearchContinuesWhenTheIndexHasMore(t *testing.T) {
	candidates := make([]documentsearch.Document, documentsearch.ReadLimit+1)
	for position := range candidates {
		candidates[position] = documentsearch.Document{DocumentType: core.DomainPage, DocumentID: core.DocumentReference(syntheticUUID(position)), SourceLocale: "ko", Title: "t"}
	}
	documents := &searchDocuments{}
	tools := mustIndexedSearchTools(t, nil, documents, testOtherDocumentDiscovery(), &fakeSearchIndex{candidates: candidates})

	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSearch, toolArguments(t, `{"query":"x","offset":10}`))

	if err != nil || len(documents.reads) != documentsearch.ReadLimit || result.StructuredContent["next_offset"] != float64(10+documentsearch.ReadLimit) {
		t.Fatalf("result = %+v, reads = %d, err = %v", result.StructuredContent, len(documents.reads), err)
	}
}

// A candidate the caller may not read must not appear, even as a count, and its
// name must not match: the index returns documents the caller may not see.
func TestDocumentSearchHidesCandidatesTheCallerCannotRead(t *testing.T) {
	index := &fakeSearchIndex{candidates: []documentsearch.Document{{DocumentType: core.DomainPage, DocumentID: searchDocumentA, SourceLocale: "ko", Title: "secret title"}}}
	tools := mustIndexedSearchTools(t, nil, &deniedSearchDocuments{}, testOtherDocumentDiscovery(), index)

	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSearch, toolArguments(t, `{"query":"secret"}`))

	if err != nil || len(result.StructuredContent["matches"].([]any)) != 0 || result.StructuredContent["unreadable"] != nil || result.StructuredContent["scanned"] != float64(0) || result.StructuredContent["next_offset"] != nil {
		t.Fatalf("result = %+v, %v", result.StructuredContent, err)
	}
}

type deniedSearchDocuments struct{ recordingAIDocumentApplication }

func (*deniedSearchDocuments) Read(context.Context, core.ReadRequest) (core.Projection, error) {
	return core.Projection{}, connect.NewError(connect.CodeNotFound, errors.New("page not found"))
}

func TestDocumentSearchExhaustiveSkipsTheIndex(t *testing.T) {
	posts := []postdomain.AIDocumentListItem{{ID: searchDocumentA, Title: "First", SourceLocale: "ko"}}
	documents := &searchDocuments{nodes: map[core.DocumentReference][]core.Node{searchDocumentA: {{ID: "b", Kind: "paragraph"}}}}
	index := &fakeSearchIndex{}
	tools := mustIndexedSearchTools(t, posts, documents, testOtherDocumentDiscovery(), index)

	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSearch, toolArguments(t, `{"query":"x","document_type":"post","exhaustive":true}`))

	if err != nil || index.calls != 0 || len(result.StructuredContent["matches"].([]any)) != 1 {
		t.Fatalf("result = %+v, calls = %d, err = %v", result.StructuredContent, index.calls, err)
	}
}

// more_blocks must count the blocks past the first outline page as well.
func TestDocumentSearchFollowsOutlinePages(t *testing.T) {
	documents := &pagedSearchDocuments{}
	index := &fakeSearchIndex{candidates: []documentsearch.Document{{DocumentType: core.DomainPage, DocumentID: searchDocumentA, SourceLocale: "ko", Title: "t"}}}
	tools := mustIndexedSearchTools(t, nil, documents, testOtherDocumentDiscovery(), index)

	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSearch, toolArguments(t, `{"query":"x","block_limit":1}`))

	match := result.StructuredContent["matches"].([]any)[0].(map[string]any)
	if err != nil || len(match["blocks"].([]any)) != 1 || match["more_blocks"] != float64(2) {
		t.Fatalf("match = %+v, %v", match, err)
	}
}

type pagedSearchDocuments struct{ recordingAIDocumentApplication }

func (*pagedSearchDocuments) Read(_ context.Context, request core.ReadRequest) (core.Projection, error) {
	if request.Cursor == "" {
		next := core.Cursor("second")
		return core.Projection{Nodes: []core.Node{{ID: "a"}, {ID: "b"}}, Next: &next}, nil
	}
	return core.Projection{Nodes: []core.Node{{ID: "c"}}}, nil
}

// A document is unreadable once, however many locales failed to read.
func TestDocumentSearchCountsAnUnreadableDocumentOnce(t *testing.T) {
	documents := &searchDocuments{fail: map[core.DocumentReference]bool{searchDocumentA: true}}
	index := &fakeSearchIndex{candidates: []documentsearch.Document{{DocumentType: core.DomainPage, DocumentID: searchDocumentA, SourceLocale: "ko", Title: "x"}}}
	tools := mustIndexedSearchTools(t, nil, documents, testOtherDocumentDiscovery(), index)

	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSearch, toolArguments(t, `{"query":"x","locales":["de","fr"]}`))

	if err != nil || result.StructuredContent["unreadable"] != float64(1) || len(result.StructuredContent["matches"].([]any)) != 0 {
		t.Fatalf("result = %+v, %v", result.StructuredContent, err)
	}
}

func TestDocumentSearchRejectsAnEmptyQueryAndAnUnknownType(t *testing.T) {
	documents := &searchDocuments{}
	for _, arguments := range []string{`{"query":"   "}`, `{"query":"x","document_type":"nope"}`, `{"query":"x","offset":-1}`, `{"query":"x","block_limit":999}`} {
		if _, err := mustDocumentSearchTools(t, nil, documents).CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSearch, toolArguments(t, arguments)); err == nil {
			t.Fatalf("%s succeeded", arguments)
		}
	}
}

// fakeOtherDocumentDiscovery lists one document of each type.
type fakeOtherDocumentDiscovery struct {
	labelsErr    error
	labelFilters []*commonv1.FilterSpec
	policyStatus *legal.Status
}

const (
	fakeLabelID    = "a0000000-0000-4000-8000-000000000001"
	fakeMenuID     = "a0000000-0000-4000-8000-000000000002"
	fakeFormID     = "a0000000-0000-4000-8000-000000000003"
	fakeSeriesID   = "a0000000-0000-4000-8000-000000000004"
	fakePolicyID   = "a0000000-0000-4000-8000-000000000005"
	fakeCampaignID = "a0000000-0000-4000-8000-000000000006"
	fakeTemplateID = "a0000000-0000-4000-8000-000000000007"
	fakeLayoutID   = "a0000000-0000-4000-8000-000000000008"
)

func (*fakeOtherDocumentDiscovery) ids() []string {
	return []string{fakeLabelID, fakeMenuID, fakeFormID, fakeSeriesID, fakePolicyID, fakeCampaignID, fakeTemplateID, fakeLayoutID}
}

func (fake *fakeOtherDocumentDiscovery) ListLabelsAdmin(_ context.Context, query label.ListLabelsQuery) (label.AdminLabelPage, error) {
	fake.labelFilters = query.Filters
	if fake.labelsErr != nil {
		return label.AdminLabelPage{}, fake.labelsErr
	}
	return label.AdminLabelPage{Labels: []label.AdminView{{Label: label.View{Label: label.Label{ID: fakeLabelID, SourceLocale: "en"}, Name: "Label"}}}, Pagination: label.Pagination{Total: 1}}, nil
}

func (*fakeOtherDocumentDiscovery) ListMenus(context.Context, menu.ListMenusQuery) (menu.MenuPage, error) {
	return menu.MenuPage{Menus: []menu.Menu{{ID: fakeMenuID, Name: "Menu", SourceLocale: "en"}}, Total: 1}, nil
}

func (*fakeOtherDocumentDiscovery) ListFormsAdmin(context.Context, form.ListFormsAdminQuery) (form.FormList, error) {
	var list form.FormList
	list.Forms = make([]form.Summary, 1)
	list.Forms[0].Title, list.Forms[0].Form.ID, list.Forms[0].Form.SourceLocale = "Form", fakeFormID, "en"
	list.Pagination.Total = 1
	return list, nil
}

func (*fakeOtherDocumentDiscovery) ListSeriesAdmin(context.Context, series.ListSeriesAdminQuery) (series.SeriesPage, error) {
	var page series.SeriesPage
	page.Items = make([]series.ListedSeries, 1)
	page.Items[0].Series.ID, page.Items[0].Series.Title, page.Items[0].Series.SourceLocale = fakeSeriesID, "Series", "en"
	page.Total = 1
	return page, nil
}

func (fake *fakeOtherDocumentDiscovery) ListPolicyVersions(_ context.Context, kind legal.PolicyKind, query legal.ListPolicyVersionsQuery) (legal.PolicyPage, error) {
	fake.policyStatus = query.Status
	return legal.PolicyPage{Versions: []legal.Policy{{ID: fakePolicyID, Title: string(kind), SourceLocale: "en"}}, Total: 1}, nil
}

func (*fakeOtherDocumentDiscovery) ListCampaigns(context.Context, campaign.ListCampaignsQuery) (campaign.CampaignPage, error) {
	var page campaign.CampaignPage
	page.Campaigns = make([]campaign.CampaignDocument, 1)
	page.Campaigns[0].ID, page.Campaigns[0].Name, page.Campaigns[0].SourceLocale = fakeCampaignID, "Campaign", "en"
	page.Pagination.Total = 1
	return page, nil
}

func (*fakeOtherDocumentDiscovery) ListEmailTemplates(context.Context, emailauthoring.ListEmailTemplatesQuery) (emailauthoring.EmailTemplatePage, error) {
	return emailauthoring.EmailTemplatePage{Templates: []emailauthoring.Template{{ID: fakeTemplateID, Name: "Template", SourceLocale: "en"}}, Total: 1}, nil
}

func (*fakeOtherDocumentDiscovery) ListEmailLayouts(context.Context, emailauthoring.ListEmailLayoutsQuery) (emailauthoring.EmailLayoutPage, error) {
	return emailauthoring.EmailLayoutPage{Layouts: []emailauthoring.Layout{{ID: fakeLayoutID, Name: "Layout", SourceLocale: "en"}}, Total: 1}, nil
}

func testOtherDocumentDiscovery() DocumentDiscoveryDeps {
	fake := &fakeOtherDocumentDiscovery{}
	return DocumentDiscoveryDeps{Labels: fake, Menus: fake, Forms: fake, PostSeries: fake, Policies: fake, Campaigns: fake, EmailTemplates: fake, EmailLayouts: fake}
}
