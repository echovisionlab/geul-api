//go:build integration

package mcp_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	aidocumentadapter "github.com/echovisionlab/geul-api/internal/adapters/aidocument"
	mcpadapter "github.com/echovisionlab/geul-api/internal/adapters/mcp"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/documentsearch"
	searchpostgres "github.com/echovisionlab/geul-api/internal/documentsearch/postgres"
	"github.com/echovisionlab/geul-api/internal/fileref"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"
	"github.com/echovisionlab/geul-api/internal/page"
	pagepostgres "github.com/echovisionlab/geul-api/internal/page/postgres"
	"github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil/contenttest"
	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/echovisionlab/geul-api/internal/testutil/postintegration"
	"github.com/echovisionlab/geul-api/internal/work"
	workpostgres "github.com/echovisionlab/geul-api/internal/work/postgres"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// searchDomains routes the three domains under test as the full registry routes all.
type searchDomains map[core.Domain]core.DomainPort

func (domains searchDomains) Load(ctx context.Context, identity core.DocumentIdentity, locale core.Locale) (core.Document, error) {
	return domains[identity.Domain].Load(ctx, identity, locale)
}

func (domains searchDomains) ValidateMutation(ctx context.Context, request core.ApplyRequest) (core.ValidationResult, error) {
	return domains[request.Identity().Domain].ValidateMutation(ctx, request)
}

func (domains searchDomains) ExecuteMutation(ctx context.Context, request core.ApplyRequest) (core.ApplyResult, error) {
	return domains[request.Identity().Domain].ExecuteMutation(ctx, request)
}

func (domains searchDomains) LoadBatch(ctx context.Context, authorized func(core.DocumentReference) bool, requests []core.LoadRequest) ([]core.DeferredDocument, error) {
	positions := map[core.Domain][]int{}
	for position, request := range requests {
		positions[request.Document.Domain] = append(positions[request.Document.Domain], position)
	}
	documents := make([]core.DeferredDocument, len(requests))
	for domain, domainPositions := range positions {
		domainRequests := make([]core.LoadRequest, len(domainPositions))
		for index, position := range domainPositions {
			domainRequests[index] = requests[position]
		}
		loaded := core.LoadBatch(ctx, domains[domain], authorized, domainRequests)
		for index, position := range domainPositions {
			documents[position] = loaded[index]
		}
	}
	return documents, nil
}

// readOneByOne is every read a batch would answer, made as a single document read.
type readOneByOne struct {
	*mcpadapter.DocumentSearchReader
}

func (reader readOneByOne) OpenBatch(context.Context, documentsearch.Scope, []core.LoadRequest) documentsearch.Reader {
	return reader.DocumentSearchReader
}

type everyDocument struct{}

func (everyDocument) Resolve(context.Context, []core.Domain) (documentsearch.Scope, error) {
	return documentsearch.Scope{All: true}, nil
}

type noDocumentLists struct{}

func (noDocumentLists) ListSearchableDocuments(context.Context, core.Domain, int, int) (documentsearch.DocumentPage, error) {
	return documentsearch.DocumentPage{}, nil
}

type passthroughWorkHydrator struct{}

func (passthroughWorkHydrator) HydrateAuthorizedWorkBlockMediaWithDB(_ context.Context, _ *gorm.DB, _ string, _ uuid.UUID, _ *auth.UserInfo, items []*fileref.BlockMediaItem) ([]*fileref.BlockMediaItem, error) {
	return items, nil
}

type searchStack struct {
	t          *testing.T
	db         *gorm.DB
	statements *atomic.Int64
	spiceDB    *auth.SpiceDBClient
	tools      *mcpadapter.AIDocumentTools
	posts      postintegration.Posts
	pages      *page.ConnectHandler
	works      *work.ConnectHandler
	oneByOne   *documentsearch.Service
	batched    *documentsearch.Service
	admin      searchActor
}

type searchActor struct {
	identityID string
	memberID   string
	ctx        context.Context
}

func newSearchStack(t *testing.T) *searchStack {
	t.Helper()
	stack := orytest.PrepareOryIntegrationTest(t)
	statements := &atomic.Int64{}
	db := stack.DB.Session(&gorm.Session{Logger: orytest.QueryCounter{Interface: stack.DB.Config.Logger, Count: statements}})
	spiceDB := stack.SpiceDBClient
	audit := apitelemetry.NewDurableWriter(db)
	contents, err := contentblock.NewGeneratedStore(allowFileReuse{})
	require.NoError(t, err)

	planner := ogpostgres.NewPlanner("https://cdn.example.com", renderConfig{}, pageadapter.NewProjection())
	pageRefresher := ogpostgres.NewRefresher(planner, ogpostgres.NewResolver(pageadapter.NewRequests()))
	pageUnits, err := pagepostgres.NewUnits(db, pagepostgres.Ports{
		ContentBlocks: contents, Signals: noopPublisher{},
		Runtime:       pageadapter.NewRuntime("https://cdn.example.com", pageRefresher),
		MediaHydrator: passthroughHydrator{}, MenuTargets: noopMenuTargets{}, Members: memberadapter.PageMembers{},
		ShareLinks: sharelinkadapter.PageLinks{},
	})
	require.NoError(t, err)
	pages, err := page.NewService(page.Deps{Units: pageUnits, SpiceDB: spiceDB, Fence: identitystate.PostgresFence{}, Audit: audit, Files: noFeaturedImages{}})
	require.NoError(t, err)

	workPlanner := ogpostgres.NewPlanner("https://cdn.example.com", renderConfig{}, workadapter.NewProjection())
	workUnits, err := workpostgres.NewUnits(db, workpostgres.Ports{
		ContentBlocks: contents, Signals: noopPublisher{}, Checkpoints: testcollaboration.NewCheckpoints(db, spiceDB),
		Runtime:    workadapter.NewRuntime(db, "https://cdn.example.com", ogpostgres.NewRefresher(workPlanner, ogpostgres.NewResolver(workadapter.NewRequests()))),
		Media:      passthroughWorkHydrator{},
		ShareLinks: sharelinkadapter.EntityLinks{Type: sharelink.EntityWork},
	})
	require.NoError(t, err)
	works, err := work.NewService(work.Deps{
		Units: workUnits, SpiceDB: spiceDB, Fence: identitystate.PostgresFence{}, Audit: audit, Members: workadapter.NewMemberSummaries(db, ""),
	})
	require.NoError(t, err)

	posts := postintegration.NewPostDomainService(t, db, "https://cdn.example.com", spiceDB, contenttest.NewPostContentBlockStore(t))

	pageRegistration, err := aidocumentadapter.NewPageRegistration(pages)
	require.NoError(t, err)
	postRegistration, err := aidocumentadapter.NewPostRegistration(posts.Service)
	require.NoError(t, err)
	workRegistration, err := aidocumentadapter.NewWorkRegistration(works)
	require.NoError(t, err)
	application, err := core.NewService(searchDomains{
		core.DomainPage: pageRegistration.Port, core.DomainPost: postRegistration.Port, core.DomainWork: workRegistration.Port,
	})
	require.NoError(t, err)
	relay, err := aidocumentadapter.NewInteractiveMutationRelay(&countingRelayClient{})
	require.NoError(t, err)
	interactive, err := aidocumentadapter.NewInteractiveMutationApplication(application, relay, noopPublisher{}, intrav1.InteractiveAIDocumentMutationOrigin_INTERACTIVE_AI_DOCUMENT_MUTATION_ORIGIN_MCP)
	require.NoError(t, err)
	tools, err := mcpadapter.NewAIDocumentTools(interactive)
	require.NoError(t, err)

	index, err := searchpostgres.NewIndex(db)
	require.NoError(t, err)
	reader, err := mcpadapter.NewDocumentSearchReader(application)
	require.NoError(t, err)
	roles, err := searchpostgres.NewCallerRoles(db, identitystate.PostgresFence{}, spiceDB)
	require.NoError(t, err)
	scopes, err := documentsearch.NewOwnershipScopes(roles, map[core.Domain]documentsearch.OwnedDocuments{core.DomainPost: posts.Service})
	require.NoError(t, err)
	oneByOne, err := documentsearch.NewService(noDocumentLists{}, readOneByOne{reader}, index, everyDocument{})
	require.NoError(t, err)
	batched, err := documentsearch.NewService(noDocumentLists{}, reader, index, scopes)
	require.NoError(t, err)

	result := &searchStack{
		t: t, db: stack.DB, statements: statements, spiceDB: spiceDB, tools: tools, posts: posts,
		pages: page.NewConnectHandler(pages), works: work.NewConnectHandler(works), oneByOne: oneByOne, batched: batched,
	}
	result.admin = result.actor("search admin", policyv1.Role.Admin())
	return result
}

func (s *searchStack) actor(name string, role policyv1.RoleID) searchActor {
	s.t.Helper()
	identityID := orytest.IntegrationUUID()
	memberID := orytest.SeedPostIntegrationIdentity(s.t, s.db, identityID, name)
	orytest.GrantIntegrationGlobalRole(s.t, s.spiceDB, identityID, role)
	return searchActor{identityID: identityID, memberID: memberID, ctx: orytest.NewAuditContext(s.t, identityID, memberID)}
}

func (s *searchStack) call(actor searchActor, name string, values map[string]any) map[string]any {
	s.t.Helper()
	result, err := s.tools.CallTool(actor.ctx, mcpserver.Principal{}, name, toolArguments(s.t, values))
	require.NoError(s.t, err, name)
	require.False(s.t, result.IsError, "%s: %+v", name, result.Content)
	return result.StructuredContent
}

// searchDocument is one created document.
type searchDocument struct {
	kind              string
	id                string
	sourceLocale      string
	contentDocumentID string
}

func uniqueSlug() string { return "search-" + strings.ReplaceAll(uuid.NewString(), "-", "") }

func (s *searchStack) created(kind, id string) searchDocument {
	s.t.Helper()
	document := searchDocument{kind: kind, id: id}
	require.NoError(s.t, s.db.Raw(fmt.Sprintf(`SELECT source_locale::text FROM public.%s WHERE id = ?::uuid`, kind), id).Scan(&document.sourceLocale).Error)
	require.NoError(s.t, s.db.Raw(fmt.Sprintf(`SELECT content_document_id::text FROM public.%s WHERE id = ?::uuid`, kind), id).Scan(&document.contentDocumentID).Error)
	return document
}

func (s *searchStack) newDocument(actor searchActor, kind, title string) searchDocument {
	s.t.Helper()
	switch kind {
	case "page":
		summary, slug := "summary", uniqueSlug()
		created, err := s.pages.CreatePage(actor.ctx, connect.NewRequest(&managev1.CreatePageRequest{Title: title, Summary: &summary, Slug: &slug}))
		require.NoError(s.t, err)
		return s.created(kind, created.Msg.Id)
	case "post":
		summary, slug := "summary", uniqueSlug()
		created, err := s.posts.CreatePost(actor.ctx, connect.NewRequest(&managev1.CreatePostRequest{
			Title: title, Summary: &summary, Slug: &slug, Document: contenttest.EmptyPostDocument("en"),
		}))
		require.NoError(s.t, err)
		return s.created(kind, created.Msg.Id)
	default:
		present := true
		created, err := s.works.CreateWork(actor.ctx, connect.NewRequest(&managev1.CreateWorkRequest{
			Title: title, Type: managev1.WorkType_WORK_TYPE_ARTICLE, Year: 2026, Month: 8, IsPresent: &present,
			Document: &contentv1.RichTextDocument{
				BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Profile: contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK,
				SourceLocale: "en", Base: &contentv1.RichTextBlockGraph{}, LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{Locale: "en"}},
			},
		}))
		require.NoError(s.t, err)
		return s.created("work", created.Msg.Id)
	}
}

func richText(parts ...map[string]any) map[string]any {
	content := make([]any, len(parts))
	for index, part := range parts {
		content[index] = part
	}
	return map[string]any{"type": "rich_text", "content": content}
}

func plainRun(text string) map[string]any { return map[string]any{"type": "text", "text": text} }

// fill writes paragraphs through the real edit tool. On a Page they sit in a section.
func (s *searchStack) fill(actor searchActor, document searchDocument, values ...map[string]any) {
	s.t.Helper()
	const operationsPerCall = 45
	if len(values) > operationsPerCall {
		s.fill(actor, document, values[:operationsPerCall]...)
		s.fill(actor, document, values[operationsPerCall:]...)
		return
	}
	revision := s.call(actor, mcpadapter.ToolDocumentRead, map[string]any{
		"document_type": document.kind, "document_id": document.id, "mode": "metadata", "locale": document.sourceLocale,
	})["document_revision"]
	var operations []map[string]any
	parent := ""
	if document.kind == "page" {
		parent = uuid.NewString()
		operations = append(operations,
			map[string]any{"op": "insert_block", "block_id": parent, "kind": "rich-text"},
			map[string]any{"op": "set_field", "block_id": parent, "field": "settings", "value": map[string]any{"type": "object", "fields": []any{}}},
		)
	}
	previous := ""
	for _, value := range values {
		block := uuid.NewString()
		insert := map[string]any{"op": "insert_block", "block_id": block, "kind": "paragraph"}
		if parent != "" {
			insert["parent_block_id"] = parent
		}
		if previous != "" {
			insert["after_block_id"] = previous
		}
		operations = append(operations, insert, map[string]any{"op": "set_field", "block_id": block, "field": "content", "value": value})
		previous = block
	}
	s.call(actor, mcpadapter.ToolDocumentApply, map[string]any{
		"document_type": document.kind, "document_id": document.id, "locale": document.sourceLocale, "expected_document_revision": revision, "operations": operations,
	})
}

// translate gives a document's first paragraph a translation, and its title one.
func (s *searchStack) translate(actor searchActor, document searchDocument) {
	s.t.Helper()
	target := "ko"
	if document.sourceLocale == "ko" {
		target = "en"
	}
	read := s.call(actor, mcpadapter.ToolDocumentRead, map[string]any{"document_type": document.kind, "document_id": document.id, "mode": "outline", "locale": target})
	var paragraph string
	for _, block := range read["blocks"].([]any) {
		if entry := block.(map[string]any); entry["kind"] == "paragraph" {
			paragraph = entry["block_id"].(string)
			break
		}
	}
	require.NotEmpty(s.t, paragraph)
	s.call(actor, mcpadapter.ToolDocumentApply, map[string]any{
		"document_type": document.kind, "document_id": document.id, "locale": target, "expected_document_revision": read["document_revision"],
		"operations": []map[string]any{
			{"op": "set_field", "block_id": paragraph, "field": "content", "value": richText(plainRun("오늘 가입하세요 marker"))},
			{"op": "set_field", "block_id": "document", "field": "title", "value": map[string]any{"type": "text", "text": "번역 제목 TitleWord"}},
		},
	})
}

// seedEveryReadOutcome creates documents of kind for each way a read can end: a block
// match, a name match, more blocks than a limit or an outline page holds, and each way
// a document can be broken.
func (s *searchStack) seedEveryReadOutcome(kind string) {
	s.t.Helper()
	admin := s.admin
	document := s.newDocument(admin, kind, "TitleWord marker")
	s.fill(admin, document,
		richText(plainRun("Sign up today marker "+kind)),
		richText(plainRun("Hel"), map[string]any{"type": "bold", "content": []any{plainRun("lo World")}}, plainRun(" see "),
			map[string]any{"type": "link", "url": "https://old.example.test/docs", "content": []any{plainRun("the docs")}}),
		richText(plainRun("Two  spaces   here and\tmore")),
		richText(plainRun("Company "), map[string]any{"type": "site_variable", "key": "site.companyName"}),
		richText(plainRun("İstanbul Straße ÉCOLE ｆｕｌｌｗｉｄｔｈ")),
		richText(plainRun("한글 띄어쓰기 테스트 100% under_score back\\slash")),
	)
	s.translate(admin, document)

	s.newDocument(admin, kind, "TitleWord only a title")

	paged := s.newDocument(admin, kind, "Paged")
	rows := make([]map[string]any, 90)
	for index := range rows {
		rows[index] = richText(plainRun(fmt.Sprintf("pagedmarker row %d", index)))
	}
	for range 4 {
		s.fill(admin, paged, rows...)
	}

	unknownKind := s.newDocument(admin, kind, "Broken")
	s.fill(admin, unknownKind, richText(plainRun("brokenmarker body")))
	require.NoError(s.t, s.db.Exec(`UPDATE public.content_block SET kind = 'not-a-kind' WHERE document_id = ?::uuid AND kind = 'paragraph'`, unknownKind.contentDocumentID).Error)
	noSource := s.newDocument(admin, kind, "BrokenTitle")
	s.fill(admin, noSource, richText(plainRun("brokenmarker no translation row")))
	require.NoError(s.t, s.db.Exec(fmt.Sprintf(`DELETE FROM public.%s_translation WHERE entity_id = ?::uuid`, kind), noSource.id).Error)
	badData := s.newDocument(admin, kind, "BadData")
	s.fill(admin, badData, richText(plainRun("brokenmarker bad data")))
	require.NoError(s.t, s.db.Exec(`UPDATE public.content_block_locale SET localized_data = '{"content":{"nonsense":1}}' WHERE block_id IN (SELECT id FROM public.content_block WHERE document_id = ?::uuid AND kind = 'paragraph')`, badData.contentDocumentID).Error)
}

// searchCoverage counts what a set of search results exercised.
type searchCoverage map[string]int

func (coverage searchCoverage) add(result documentsearch.Result) {
	if len(result.Matches) != 0 {
		coverage["matches"]++
	}
	if result.Unreadable != 0 {
		coverage["broken documents"]++
	}
	for _, match := range result.Matches {
		if len(match.Blocks) == 0 {
			coverage["title matches"]++
		} else {
			coverage["block matches"]++
		}
		if match.MoreBlocks != 0 {
			coverage["more blocks"]++
		}
	}
}

// A batched search answers what reading each document does, in every way a read can end.
func TestABatchedSearchFindsWhatReadingEachDocumentDoesIntegration(t *testing.T) {
	t.Parallel()
	s := newSearchStack(t)
	for _, kind := range []string{"page", "post", "work"} {
		s.seedEveryReadOutcome(kind)
	}

	queries := []string{
		"Sign up", "sign UP today", "Sign  up", "Hello World", "lo Wor", "HelloWorld", "the docs", "old.example.test/docs", "site.companyName", "{site.companyName}",
		"İstanbul", "istanbul", "한글", "한글띄어쓰기", "오늘 가입", "100%", "under_score", `back\slash`, "marker", "  marker  ", "rich_text", "TitleWord", "titleword",
		"search-", "nothing-matches-this", "spaces here", "pagedmarker", "brokenmarker", "fullwidth", "STRASSE", "école",
	}
	documentTypes := []core.Domain{core.DomainPage, core.DomainPost, core.DomainWork}
	coverage := searchCoverage{}
	for _, text := range queries {
		for _, locales := range [][]core.Locale{nil, {"en"}, {"ko"}, {"en", "ko"}, {"ja"}} {
			for _, blockLimit := range []int{3, 50} {
				query := documentsearch.Query{Text: text, DocumentTypes: documentTypes, Locales: locales, BlockLimit: blockLimit}
				oneByOne, err := s.oneByOne.Search(s.admin.ctx, query)
				require.NoError(t, err)
				batched, err := s.batched.Search(s.admin.ctx, query)
				require.NoError(t, err)
				require.Equal(t, oneByOne, batched, "%q %v %d", text, locales, blockLimit)
				coverage.add(oneByOne)
			}
		}
	}
	for _, name := range []string{"matches", "block matches", "title matches", "broken documents", "more blocks"} {
		require.NotZero(t, coverage[name], "no search exercised %s", name)
	}
}

// Who may read and find which document. Posts stay with their writers in every
// lifecycle, and everything else is a Site Admin's.
func TestASearchFindsExactlyTheDocumentsTheCallerOwnsIntegration(t *testing.T) {
	t.Parallel()
	s := newSearchStack(t)
	admin := s.admin
	owner := s.actor("owner author", policyv1.Role.Author())
	collaborator := s.actor("collaborator author", policyv1.Role.Author())
	userCollaborator := s.actor("collaborator user", policyv1.Role.User())
	formerAuthor := s.actor("former author", policyv1.Role.Author())
	otherAuthor := s.actor("other author", policyv1.Role.Author())
	plainUser := s.actor("plain user", policyv1.Role.User())

	posts := map[string]searchDocument{}
	for _, status := range []string{"DRAFT", "SCHEDULED", "PUBLISHED", "ARCHIVED"} {
		document := s.newDocument(owner, "post", "scopemarker "+status)
		s.fill(owner, document, richText(plainRun("scopemarker body")))
		_, err := s.posts.AddPostCollaborator(admin.ctx, connect.NewRequest(&managev1.AddPostCollaboratorRequest{PostId: document.id, MemberId: collaborator.memberID}))
		require.NoError(t, err)
		_, err = s.posts.AddPostCollaborator(admin.ctx, connect.NewRequest(&managev1.AddPostCollaboratorRequest{PostId: document.id, MemberId: userCollaborator.memberID}))
		require.NoError(t, err)
		_, err = s.posts.AddPostAuthor(admin.ctx, connect.NewRequest(&managev1.AddPostAuthorRequest{PostId: document.id, MemberId: formerAuthor.memberID}))
		require.NoError(t, err)
		extra := ""
		switch status {
		case "PUBLISHED", "ARCHIVED":
			extra = ", published_at = now()"
		case "SCHEDULED":
			extra = ", scheduled_at = now() + interval '1 day', scheduled_time_zone = 'UTC'"
		}
		require.NoError(t, s.db.Exec(fmt.Sprintf(`UPDATE public.post SET status = ?%s WHERE id = ?::uuid`, extra), "POST_STATUS_"+status, document.id).Error)
		posts[status] = document
	}
	orytest.GrantIntegrationGlobalRole(t, s.spiceDB, formerAuthor.identityID, policyv1.Role.User())

	pageDocument := s.newDocument(admin, "page", "scopemarker page")
	s.fill(admin, pageDocument, richText(plainRun("scopemarker body")))
	draftWork := s.newDocument(admin, "work", "scopemarker draft work")
	s.fill(admin, draftWork, richText(plainRun("scopemarker body")))
	archivedWork := s.newDocument(admin, "work", "scopemarker archived work")
	s.fill(admin, archivedWork, richText(plainRun("scopemarker body")))
	require.NoError(t, s.db.Exec(`UPDATE public.work SET status = 'WORK_STATUS_ARCHIVED' WHERE id = ?::uuid`, archivedWork.id).Error)

	everything := []searchDocument{posts["DRAFT"], posts["SCHEDULED"], posts["PUBLISHED"], posts["ARCHIVED"], pageDocument, draftWork, archivedWork}
	ownPosts := []searchDocument{posts["DRAFT"], posts["SCHEDULED"], posts["PUBLISHED"], posts["ARCHIVED"]}
	for _, test := range []struct {
		name   string
		actor  searchActor
		reads  []searchDocument
		finds  []searchDocument
		listed []searchDocument
	}{
		{name: "admin", actor: admin, reads: everything, finds: everything, listed: ownPosts},
		{name: "owner", actor: owner, reads: ownPosts, finds: ownPosts, listed: ownPosts},
		{name: "collaborating author", actor: collaborator, reads: ownPosts, finds: ownPosts, listed: ownPosts},
		// Admission refuses this caller before any tool. A search still finds nothing,
		// because its scope cannot tell when one of these Posts is archived.
		{name: "collaborator without the role", actor: userCollaborator, reads: []searchDocument{posts["DRAFT"], posts["SCHEDULED"], posts["PUBLISHED"]}, listed: []searchDocument{posts["DRAFT"], posts["SCHEDULED"], posts["PUBLISHED"]}},
		{name: "former author", actor: formerAuthor},
		{name: "other author", actor: otherAuthor},
		{name: "plain user", actor: plainUser},
	} {
		t.Run(test.name, func(t *testing.T) {
			found, err := s.batched.Search(test.actor.ctx, documentsearch.Query{
				Text: "scopemarker", DocumentTypes: []core.Domain{core.DomainPage, core.DomainPost, core.DomainWork}, BlockLimit: 5,
			})
			require.NoError(t, err)
			listed, err := s.posts.Service.ListAIDocuments(test.actor.ctx, post.AIDocumentListInput{Query: "scopemarker", Limit: 20})
			require.NoError(t, err)
			for _, document := range everything {
				result, err := s.tools.CallTool(test.actor.ctx, mcpserver.Principal{}, mcpadapter.ToolDocumentRead, toolArguments(t, map[string]any{
					"document_type": document.kind, "document_id": document.id, "mode": "outline", "locale": document.sourceLocale,
				}))
				require.Equal(t, slices.Contains(test.reads, document), err == nil && !result.IsError, "read %s", document.kind)
				require.Equal(t, slices.Contains(test.finds, document), slices.ContainsFunc(found.Matches, func(match documentsearch.Match) bool {
					return string(match.Document.DocumentID) == document.id
				}), "find %s", document.kind)
				if document.kind == "post" {
					require.Equal(t, slices.Contains(test.listed, document), slices.ContainsFunc(listed.Items, func(item post.AIDocumentListItem) bool {
						return item.ID == document.id
					}), "list %s", document.kind)
				}
			}
		})
	}
}

// next_offset used to show how many candidates a caller could not read, and an owner
// stepped through other writers' documents to reach their own.
func TestASearchPagesOnlyThroughTheCallersOwnDocumentsIntegration(t *testing.T) {
	t.Parallel()
	s := newSearchStack(t)
	owner := s.actor("owner author", policyv1.Role.Author())
	otherAuthor := s.actor("other author", policyv1.Role.Author())
	query := documentsearch.Query{Text: "leakmarker", DocumentTypes: []core.Domain{core.DomainPost}, BlockLimit: 5}
	var own []string
	for index := range documentsearch.ReadLimit + 5 {
		creator := s.admin
		if index%10 == 0 {
			creator = owner
		}
		created := s.newDocument(creator, "post", "leakmarker draft")
		if index%10 == 0 {
			own = append(own, created.id)
		}
	}

	nothing, err := s.batched.Search(otherAuthor.ctx, query)
	require.NoError(t, err)
	require.Empty(t, nothing.Matches)
	require.Nil(t, nothing.NextOffset)

	found, err := s.batched.Search(owner.ctx, query)
	require.NoError(t, err)
	require.Len(t, found.Matches, len(own))
	require.Nil(t, found.NextOffset)
	require.Zero(t, found.Unreadable)
}

// The cost of a search must not grow by statements per document.
func TestABatchedSearchReadsManyDocumentsInTheSameStatementsIntegration(t *testing.T) {
	t.Parallel()
	s := newSearchStack(t)
	statementsFinding := func(word string, documents int) int64 {
		for _, kind := range []string{"page", "post", "work"} {
			for range documents {
				s.fill(s.admin, s.newDocument(s.admin, kind, "Statements"), richText(plainRun(word+" body")))
			}
		}
		query := documentsearch.Query{Text: word, DocumentTypes: []core.Domain{core.DomainPage, core.DomainPost, core.DomainWork}, BlockLimit: 5}
		s.statements.Store(0)
		result, err := s.batched.Search(s.admin.ctx, query)
		require.NoError(t, err)
		require.Len(t, result.Matches, 3*documents)
		return s.statements.Load()
	}

	require.Equal(t, statementsFinding("fewstatementsword", 1), statementsFinding("manystatementsword", 4))
}
