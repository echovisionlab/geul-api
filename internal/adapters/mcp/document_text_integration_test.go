//go:build integration

package mcp_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	aidocumentadapter "github.com/echovisionlab/geul-api/internal/adapters/aidocument"
	emailauthoringadapter "github.com/echovisionlab/geul-api/internal/adapters/emailauthoring"
	mcpadapter "github.com/echovisionlab/geul-api/internal/adapters/mcp"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	translationadapter "github.com/echovisionlab/geul-api/internal/adapters/translation"
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/documentsearch"
	searchpostgres "github.com/echovisionlab/geul-api/internal/documentsearch/postgres"
	"github.com/echovisionlab/geul-api/internal/documenttext"
	"github.com/echovisionlab/geul-api/internal/fileref"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/mutation"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"
	"github.com/echovisionlab/geul-api/internal/page"
	pagepostgres "github.com/echovisionlab/geul-api/internal/page/postgres"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	translationapplication "github.com/echovisionlab/geul-api/internal/translation/application"
	translationpostgres "github.com/echovisionlab/geul-api/internal/translation/postgres"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := orytest.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start MCP integration suite: %v\n", err)
		os.Exit(1)
	}
	orytest.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	orytest.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close MCP integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

// documentStack is the production document tools over a real Page and the real
// translation service, joined by the real shared transaction.
type documentStack struct {
	t            *testing.T
	ctx          context.Context
	db           *gorm.DB
	documents    *mcpadapter.AIDocumentTools
	text         *mcpadapter.DocumentTextTools
	translations *mcpadapter.TranslationTools
	relay        *countingRelayClient
	pageID       string
	slug         string
	source       string
	target       string
}

func newDocumentStack(t *testing.T) *documentStack {
	t.Helper()
	stack := orytest.PrepareOryIntegrationTest(t)
	db, spiceDB := stack.DB, stack.SpiceDBClient
	identityID := orytest.IntegrationUUID()
	memberID := orytest.SeedPostIntegrationIdentity(t, db, identityID, "Document tools admin")
	orytest.GrantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	ctx := orytest.NewAuditContext(t, identityID, memberID)
	audit := apitelemetry.NewDurableWriter(db)

	contents, err := contentblock.NewGeneratedStore(allowFileReuse{})
	require.NoError(t, err)
	planner := ogpostgres.NewPlanner("https://cdn.example.com", renderConfig{}, pageadapter.NewProjection())
	refresher := ogpostgres.NewRefresher(planner, ogpostgres.NewResolver(pageadapter.NewRequests()))
	pageUnits, err := pagepostgres.NewUnits(db, pagepostgres.Ports{
		ContentBlocks: contents, Signals: noopPublisher{},
		Runtime:       pageadapter.NewRuntime("https://cdn.example.com", refresher),
		MediaHydrator: passthroughHydrator{}, MenuTargets: noopMenuTargets{}, Members: memberadapter.PageMembers{},
		ShareLinks: sharelinkadapter.PageLinks{},
	})
	require.NoError(t, err)
	pages, err := page.NewService(page.Deps{Units: pageUnits, SpiceDB: spiceDB, Fence: identitystate.PostgresFence{}, Audit: audit, Files: noFeaturedImages{}})
	require.NoError(t, err)

	registration, err := aidocumentadapter.NewPageRegistration(pages)
	require.NoError(t, err)
	application, err := core.NewService(registration.Port)
	require.NoError(t, err)
	relayClient := &countingRelayClient{}
	relay, err := aidocumentadapter.NewInteractiveMutationRelay(relayClient)
	require.NoError(t, err)
	interactive, err := aidocumentadapter.NewInteractiveMutationApplication(application, relay, noopPublisher{}, intrav1.InteractiveAIDocumentMutationOrigin_INTERACTIVE_AI_DOCUMENT_MUTATION_ORIGIN_MCP)
	require.NoError(t, err)

	registry, err := translationadapter.NewDomainRegistry(emailauthoringadapter.NewCampaignDeliveryReferences(), audit)
	require.NoError(t, err)
	interchange := translationadapter.NewPageInterchangePort(audit, sharedtelemetry.NewPageLocaleContentAuditRecord)
	translationUnits, err := translationpostgres.NewUnits(db, translationpostgres.Ports{
		Domains: registry, Interchange: interchange, ContentBlocks: contents, OGPlanner: planner, OGRefresher: refresher,
		SpiceDB: spiceDB, Signals: noopPublisher{}, CDNDomain: "https://cdn.example.com",
	})
	require.NoError(t, err)
	translationService, err := translationapplication.NewService(translationapplication.Deps{
		Units: translationUnits, SpiceDB: spiceDB, Fence: identitystate.PostgresFence{}, Audit: audit, Plans: registry, XLIFFFiles: noXLIFFFiles{},
	})
	require.NoError(t, err)

	documents, err := mcpadapter.NewAIDocumentTools(interactive)
	require.NoError(t, err)
	units, err := mcpadapter.NewDocumentTranslationUnits(translationService)
	require.NoError(t, err)
	editor, err := documenttext.NewService(interactive, units, func(ctx context.Context, work func(context.Context) error) error {
		return mutation.InSharedTransaction(ctx, db, work)
	})
	require.NoError(t, err)
	text, err := mcpadapter.NewDocumentTextTools(editor)
	require.NoError(t, err)
	translations, err := mcpadapter.NewTranslationTools(translationService)
	require.NoError(t, err)

	slug := "text-tools-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	created, err := page.NewConnectHandler(pages).CreatePage(ctx, connect.NewRequest(&managev1.CreatePageRequest{Title: "Findable Heading " + slug, Slug: &slug}))
	require.NoError(t, err)

	result := &documentStack{t: t, ctx: ctx, db: db, documents: documents, text: text, translations: translations, relay: relayClient, pageID: created.Msg.Id, slug: slug}
	metadata := result.call(documents, mcpadapter.ToolDocumentRead, map[string]any{"mode": "metadata", "locale": "en"})
	result.source = metadata["source_locale"].(string)
	result.target = "ko"
	if result.source == "ko" {
		result.target = "en"
	}
	return result
}

type toolCaller interface {
	CallTool(context.Context, mcpserver.Principal, string, mcpserver.ToolArguments) (mcpserver.ToolResult, error)
}

func toolArguments(t *testing.T, values map[string]any) mcpserver.ToolArguments {
	t.Helper()
	arguments := mcpserver.ToolArguments{}
	for name, value := range values {
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		arguments[name] = encoded
	}
	return arguments
}

// call runs a document-scoped tool on this stack's Page.
func (s *documentStack) call(tools toolCaller, name string, values map[string]any) map[string]any {
	s.t.Helper()
	values["document_type"], values["document_id"] = "page", s.pageID
	result, err := tools.CallTool(s.ctx, mcpserver.Principal{}, name, toolArguments(s.t, values))
	require.NoError(s.t, err, name)
	require.False(s.t, result.IsError, "%s: %+v", name, result.Content)
	return result.StructuredContent
}

func (s *documentStack) edit(tool string, dryRun bool, edits ...map[string]any) map[string]any {
	s.t.Helper()
	for _, edit := range edits {
		edit["document_type"], edit["document_id"] = "page", s.pageID
	}
	result, err := s.text.CallTool(s.ctx, mcpserver.Principal{}, tool, toolArguments(s.t, map[string]any{"edits": edits, "dry_run": dryRun}))
	require.NoError(s.t, err, tool)
	return result.StructuredContent["documents"].([]any)[0].(map[string]any)
}

// seedBlocks writes a plain paragraph and one whose text is split by formatting
// and holds a link.
func (s *documentStack) seedBlocks() {
	s.t.Helper()
	revision := s.call(s.documents, mcpadapter.ToolDocumentRead, map[string]any{"mode": "metadata", "locale": s.source})["document_revision"]
	section, plain, rich := uuid.NewString(), uuid.NewString(), uuid.NewString()
	s.call(s.documents, mcpadapter.ToolDocumentApply, map[string]any{
		"locale": s.source, "expected_document_revision": revision,
		"operations": []map[string]any{
			{"op": "insert_block", "block_id": section, "kind": "rich-text"},
			{"op": "set_field", "block_id": section, "field": "settings", "value": map[string]any{"type": "object", "fields": []any{}}},
			{"op": "insert_block", "block_id": plain, "kind": "paragraph", "parent_block_id": section},
			{"op": "set_field", "block_id": plain, "field": "content", "value": map[string]any{"type": "rich_text", "content": []any{map[string]any{"type": "text", "text": "Sign up today"}}}},
			{"op": "insert_block", "block_id": rich, "kind": "paragraph", "parent_block_id": section, "after_block_id": plain},
			{"op": "set_field", "block_id": rich, "field": "content", "value": map[string]any{"type": "rich_text", "content": []any{
				map[string]any{"type": "text", "text": "Hel"},
				map[string]any{"type": "bold", "content": []any{map[string]any{"type": "text", "text": "lo World"}}},
				map[string]any{"type": "text", "text": " see "},
				map[string]any{"type": "link", "url": "https://old.example.test/docs", "content": []any{map[string]any{"type": "text", "text": "the docs"}}},
			}}},
		},
	})
	written, err := s.translations.CallTool(s.ctx, mcpserver.Principal{}, mcpadapter.ToolTranslationUnitsPut, toolArguments(s.t, map[string]any{"documents": []map[string]any{{
		"document_type": "page", "document_id": s.pageID,
		"edits": []map[string]any{{"source": "Sign up today", "targets": map[string]string{s.target: "오늘 가입하세요"}}},
	}}}))
	require.NoError(s.t, err)
	require.False(s.t, written.IsError, "%+v", written.StructuredContent)
}

// previews is the text of every block in locale.
func (s *documentStack) previews(locale string) string {
	s.t.Helper()
	outline := s.call(s.documents, mcpadapter.ToolDocumentRead, map[string]any{"mode": "outline", "locale": locale})
	var previews []string
	for _, block := range outline["blocks"].([]any) {
		if preview, ok := block.(map[string]any)["preview"].(string); ok {
			previews = append(previews, preview)
		}
	}
	return strings.Join(previews, " | ")
}

func TestSourceAndTranslationsChangeTogetherOnARealPageIntegration(t *testing.T) {
	t.Parallel()
	s := newDocumentStack(t)
	s.seedBlocks()
	replace := func(find, replacement string, translations map[string]string) map[string]any {
		edit := map[string]any{"locale": s.source, "find": find, "replace": replacement}
		if translations != nil {
			edit["translations"] = translations
		}
		return edit
	}

	preview := s.edit(mcpadapter.ToolTextReplace, true, replace("Sign up", "Join", map[string]string{s.target: "오늘 참여하세요"}))
	require.Equal(t, "would_apply", preview["status"], "%+v", preview)
	require.Contains(t, s.previews(s.source), "Sign up today", "a dry run must change nothing")
	require.Contains(t, s.previews(s.target), "오늘 가입하세요", "a dry run must change nothing")
	relaysBefore := s.relay.calls

	applied := s.edit(mcpadapter.ToolTextReplace, false, replace("Sign up", "Join", map[string]string{s.target: "오늘 참여하세요"}))
	require.Equal(t, "applied", applied["status"], "%+v", applied)
	require.Contains(t, s.previews(s.source), "Join today")
	require.Contains(t, s.previews(s.target), "오늘 참여하세요")
	require.Greater(t, s.relay.calls, relaysBefore, "Collab must hear about a committed edit")

	relaysBefore = s.relay.calls
	failed := s.edit(mcpadapter.ToolTextReplace, false, replace("Join", "Enter", map[string]string{"zz": "never"}))
	require.Equal(t, "failed", failed["status"], "%+v", failed)
	require.Contains(t, s.previews(s.source), "Join today", "a failed translation must undo the source edit")
	require.NotContains(t, s.previews(s.source), "Enter today")
	require.Equal(t, relaysBefore, s.relay.calls, "Collab must not hear about a rolled back edit")

	together := s.edit(mcpadapter.ToolTextReplace, false,
		map[string]any{"locale": s.target, "find": "오늘", "replace": "지금"},
		map[string]any{"locale": s.source, "find": "today", "replace": "now"},
	)
	require.Equal(t, "applied", together["status"], "%+v", together)
	require.Contains(t, s.previews(s.source), "Join now")
	require.Contains(t, s.previews(s.target), "지금 참여하세요")
}

// The title is its own translation unit, so sharing words with a block must not
// stop its translation, or write it to the block.
func TestTitleTranslationIsPlacedWhenABlockHoldsTheSameWordsIntegration(t *testing.T) {
	t.Parallel()
	s := newDocumentStack(t)
	s.seedBlocks()

	result := s.edit(mcpadapter.ToolTextReplace, false, map[string]any{
		"locale": s.source, "find": "Findable Heading " + s.slug, "replace": "Sign up", "translations": map[string]string{s.target: "가입"},
	})

	require.Equal(t, "applied", result["status"], "%+v", result)
	translated := s.previews(s.target)
	require.Regexp(t, `(^| \| )가입( \| |$)`, translated, "the title's translation must be the title's alone")
	require.Contains(t, translated, "오늘 가입하세요", "the block's translation must stay")
}

func TestLinkAddressChangesWithoutRewritingTheTextIntegration(t *testing.T) {
	t.Parallel()
	s := newDocumentStack(t)
	s.seedBlocks()

	result := s.edit(mcpadapter.ToolTextFormat, false, map[string]any{"locale": s.source, "find_link": "https://old.example.test/docs", "link": "https://new.example.test/docs"})

	require.Equal(t, "applied", result["status"], "%+v", result)
	found, err := s.documents.CallTool(s.ctx, mcpserver.Principal{}, mcpadapter.ToolDocumentRead, toolArguments(t, map[string]any{
		"document_type": "page", "document_id": s.pageID, "locale": s.source, "mode": "outline", "query": "new.example.test",
	}))
	require.NoError(t, err)
	require.Len(t, found.StructuredContent["blocks"], 1)
}

// The index reads stored block JSON, so text written through the real tools must
// be what it finds, including text split by formatting.
func TestTextWrittenThroughTheToolsIsFoundByTheSearchIndexIntegration(t *testing.T) {
	t.Parallel()
	s := newDocumentStack(t)
	s.seedBlocks()
	index, err := searchpostgres.NewIndex(s.db)
	require.NoError(t, err)
	for _, text := range []string{"Sign up", "Hello World", "old.example.test/docs"} {
		candidates, err := index.Candidates(s.ctx, documentsearch.IndexQuery{Text: text, DocumentTypes: []core.Domain{core.DomainPage}, Limit: 100})
		require.NoError(t, err, text)
		found := false
		for _, candidate := range candidates {
			found = found || string(candidate.DocumentID) == s.pageID
		}
		require.True(t, found, text)
	}
}

type countingRelayClient struct{ calls int }

func (client *countingRelayClient) RelayInteractiveAIDocumentMutation(context.Context, *connect.Request[intrav1.RelayInteractiveAIDocumentMutationRequest]) (*connect.Response[intrav1.RelayInteractiveAIDocumentMutationResponse], error) {
	client.calls++
	return nil, errors.New("collab is not part of this test")
}

type allowFileReuse struct{}

func (allowFileReuse) AuthorizeFileReuse(context.Context, *gorm.DB, contentblock.Document, contentblock.FullBlock, contentblock.FileReference, contentblock.File) error {
	return nil
}

type passthroughHydrator struct{}

func (passthroughHydrator) HydrateAuthorizedPageBlockMediaWithDB(_ context.Context, _ *gorm.DB, _ string, _ uuid.UUID, _ *auth.UserInfo, items []*fileref.BlockMediaItem) ([]*fileref.BlockMediaItem, error) {
	return items, nil
}

type noFeaturedImages struct{}

func (noFeaturedImages) ResolveAuthorizedPageFeaturedImage(context.Context, string, string) (*fileref.Delivery, error) {
	return nil, nil
}

type noopMenuTargets struct{}

func (noopMenuTargets) UpdateSlug(context.Context, *gorm.DB, string, string, string, string) error {
	return nil
}

func (noopMenuTargets) Remove(context.Context, *gorm.DB, string, string, string) error { return nil }

type noopPublisher struct{}

func (noopPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}
func (noopPublisher) NotifyProtobuf(context.Context, string, proto.Message) error { return nil }
func (noopPublisher) NotifyProtobufWithExecutor(context.Context, eventpkg.DBTX, string, proto.Message) error {
	return nil
}

type renderConfig struct{}

func (renderConfig) Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error) {
	payload := []byte(`{"site_title":"","primary_color":"#b02d23"}`)
	return payload, fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}

type noXLIFFFiles struct {
	translationapplication.TranslationXLIFFFiles
}

// The search only scales if the query's expression is the indexed one.
func TestSearchQueryUsesTheIndexIntegration(t *testing.T) {
	t.Parallel()
	s := newDocumentStack(t)
	var plan []string
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SET LOCAL enable_seqscan = off`).Error; err != nil {
			return err
		}
		return tx.Raw(`EXPLAIN SELECT block_id FROM content_block_locale WHERE public.content_block_search_text(localized_data) &@ 'Sign'`).Scan(&plan).Error
	})
	require.NoError(t, err)
	require.Contains(t, strings.Join(plan, "\n"), "idx_content_block_locale_search")
}
