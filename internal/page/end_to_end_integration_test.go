//go:build integration

package page_test

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/page"
	pagepostgres "github.com/echovisionlab/geul-api/internal/page/postgres"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
)

// These tests keep what only the whole stack shows: SpiceDB relationship
// semantics and the cascades of a real deletion, the identity fence over
// real Kratos rows, and the lock order of a collaboration bootstrap against
// an account's session deletion. Every other Page scenario runs on the fake
// Store, and the page tables on the Store contract.

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start Page integration suite: %v\n", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close Page integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

// stackPorts are the production Page ports over db; hydrate, when set,
// stands in for the File service's Block media hydration.
func stackPorts(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient, hydrate pagepostgres.ContentBlockMediaHydrator) pagepostgres.Ports {
	t.Helper()
	contents, err := contentblock.NewGeneratedStore(adminFileReuse{})
	require.NoError(t, err)
	if hydrate == nil {
		hydrate = passthroughHydrator{}
	}
	planner := og.NewPlanner(db, "https://cdn.example.com", renderConfig{}, pageadapter.NewProjection())
	return pagepostgres.Ports{
		ContentBlocks: contents, Signals: noopPublisher{},
		Runtime:       pageadapter.NewRuntime("https://cdn.example.com", og.NewRefresher(planner, og.NewResolver(pageadapter.NewRequests()))),
		MediaHydrator: hydrate, MenuTargets: noopMenuTargets{}, Members: memberadapter.PageMembers{},
		ShareLinks: sharelinkadapter.PageLinks{},
	}
}

func newStackService(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient, ports pagepostgres.Ports) *page.Service {
	t.Helper()
	units, err := pagepostgres.NewUnits(db, ports)
	require.NoError(t, err)
	service, err := page.NewService(page.Deps{
		Units: units, SpiceDB: spiceDB, Fence: identitystate.PostgresFence{},
		Audit: apitelemetry.NewDurableWriter(db), Files: stackFeaturedImages{},
	})
	require.NoError(t, err)
	return service
}

func TestPageManagementEndToEndIntegration(t *testing.T) {
	t.Parallel()
	stack := testutil.PrepareOryIntegrationTest(t)
	db, spiceDB := stack.DB, stack.SpiceDBClient
	identityID := testutil.IntegrationUUID()
	memberID := testutil.SeedPostIntegrationIdentity(t, db, identityID, "Page integration admin")
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	ctx := testutil.NewAuditContext(t, identityID, memberID)
	pages := page.NewConnectHandler(newStackService(t, db, spiceDB, stackPorts(t, db, spiceDB, nil)))
	suffix := strings.ReplaceAll(testutil.IntegrationUUID(), "-", "")
	slug := "page-manager-" + suffix
	summary := "Page manager workflow summary"

	created, err := pages.CreatePage(ctx, connect.NewRequest(&managev1.CreatePageRequest{
		Title: "Page Manager Workflow " + suffix, Slug: &slug, Summary: &summary,
	}))
	require.NoError(t, err)
	require.Empty(t, created.Msg.Document.GetBase().GetNodes())
	require.NotEmpty(t, created.Msg.Revision)
	requireManageable(t, spiceDB, created.Msg.Id, identityID, true)
	other, err := pages.CreatePage(ctx, connect.NewRequest(&managev1.CreatePageRequest{Title: "Page Manager Workflow Other " + suffix}))
	require.NoError(t, err)

	listed, err := pages.ListPagesAdmin(ctx, connect.NewRequest(&managev1.ListPagesAdminRequest{
		Pagination: &commonv1.PaginationRequest{Limit: 10},
		Filters: []*commonv1.FilterSpec{
			{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: managev1.PageStatus_PAGE_STATUS_DRAFT.String()},
			{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "Manager Workflow " + suffix},
		},
		Sorts: []*commonv1.SortSpec{{Field: "title", Order: commonv1.SortOrder_SORT_ORDER_ASC}},
	}))
	require.NoError(t, err)
	require.Equal(t, int32(2), listed.Msg.Pagination.Total)
	require.ElementsMatch(t, []string{created.Msg.Id, other.Msg.Id}, []string{listed.Msg.Pages[0].Id, listed.Msg.Pages[1].Id})

	// Republishing keeps the first publication time and stays on the
	// indexed readiness path.
	_, err = pages.PublishPage(ctx, connect.NewRequest(&managev1.PublishPageRequest{Id: other.Msg.Id}))
	require.NoError(t, err)
	var first model.Page
	require.NoError(t, db.Select("published_at").First(&first, "id = ?", other.Msg.Id).Error)
	_, err = pages.UnpublishPage(ctx, connect.NewRequest(&managev1.UnpublishPageRequest{Id: other.Msg.Id}))
	require.NoError(t, err)
	statements := &statementRecorder{Interface: db.Config.Logger}
	counted := db.Session(&gorm.Session{Logger: statements})
	republished, err := page.NewConnectHandler(newStackService(t, counted, spiceDB, stackPorts(t, counted, spiceDB, nil))).
		PublishPage(ctx, connect.NewRequest(&managev1.PublishPageRequest{Id: other.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, first.PublishedAt.UTC(), republished.Msg.PublishedAt.AsTime().UTC())
	publishSQL := statements.snapshot()
	// Eight statements and the audit record every Page mutation appends.
	require.LessOrEqual(t, len(publishSQL), 9, "Page publish must stay on the indexed readiness path")
	publishSQLText := strings.ToLower(strings.Join(publishSQL, "\n"))
	require.Contains(t, publishSQLText, "content_block_attachment")
	require.NotContains(t, publishSQLText, "content_block_locale")

	// Deletion removes the policy relationships and every row that points
	// at the Page, and keeps the Files.
	fileID := seedReadyImage(t, db)
	_, err = pages.SetPageFeaturedImage(ctx, connect.NewRequest(&managev1.SetPageFeaturedImageRequest{PageId: created.Msg.Id, FileId: fileID}))
	require.NoError(t, err)
	var asset model.PublicAsset
	require.NoError(t, db.Where("source_file_id = ? AND status = ?", fileID, model.PublicAssetStatusReady).Take(&asset).Error)
	require.NoError(t, mediaasset.NewLifecycle(db, "https://cdn.example.com").BindPublicAsset(ctx, mediaasset.Binding{
		AssetID: asset.ID, OwnerType: "page", OwnerID: created.Msg.Id, BindingKey: "featured_image", SourceFileID: &fileID,
	}))
	require.NoError(t, db.Model(&model.SiteSettings{}).Where("id = ?", 1).Update("homepage_page_id", created.Msg.Id).Error)
	shareLinkID := testutil.IntegrationUUID()
	expiry := time.Now().UTC().Add(time.Hour)
	require.NoError(t, db.Create(&model.ShareLink{
		ID: shareLinkID, Token: "page-delete-" + shareLinkID, EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE.String(),
		EntityID: created.Msg.Id, ExpiresAt: &expiry, CreatedAt: time.Now().UTC(),
	}).Error)
	var documentID string
	require.NoError(t, db.Table("page").Select("content_document_id").Where("id = ?", created.Msg.Id).Scan(&documentID).Error)

	_, err = pages.DeletePage(ctx, connect.NewRequest(&managev1.DeletePageRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	requireManageable(t, spiceDB, created.Msg.Id, identityID, false)
	for table, where := range map[string][]any{
		"public_asset_binding": {"owner_type = ? AND owner_id = ?", "page", created.Msg.Id},
		"content_document":     {"id = ?", documentID},
		"share_link":           {"id = ?", shareLinkID},
	} {
		var count int64
		require.NoError(t, db.Table(table).Where(where[0], where[1:]...).Count(&count).Error)
		require.Zero(t, count, table)
	}
	var settings model.SiteSettings
	require.NoError(t, db.First(&settings, "id = ?", 1).Error)
	require.Nil(t, settings.HomepagePageID)
	var files int64
	require.NoError(t, db.Table("file").Where("id = ?", fileID).Count(&files).Error)
	require.Equal(t, int64(1), files)
}

func TestPolicyAuthorityLocksPageRootAndFencesCurrentPrincipalIntegration(t *testing.T) {
	t.Parallel()
	stack := testutil.PrepareOryIntegrationTest(t)
	db, spiceDB := stack.DB, stack.SpiceDBClient
	user := stack.CreateUser(t, policyv1.Role.Admin().ID())
	ctx := auth.WithUser(t.Context(), user.AuthUserInfo())
	pageID := seedPageRoot(t, db, spiceDB)
	authority := pageadapter.NewPolicyAccess(page.NewPolicyAuthority(spiceDB))
	inTransaction := func(require func(*gorm.DB) error) error {
		tx := db.Begin()
		defer tx.Rollback()
		return require(tx)
	}

	err := inTransaction(func(tx *gorm.DB) error { return authority.RequireLockedView(ctx, tx, uuid.NewString()) })
	require.Equal(t, connect.CodeNotFound, errs.CodeOf(err))
	require.NoError(t, inTransaction(func(tx *gorm.DB) error { return authority.RequireLockedView(ctx, tx, pageID) }))
	require.NoError(t, inTransaction(func(tx *gorm.DB) error { return authority.RequireLockedEdit(ctx, tx, pageID) }))

	// The fence reads the Member row, not the request principal.
	require.NoError(t, db.Table("member").Where("id = ?", user.MemberID).Update("onboarded", false).Error)
	err = inTransaction(func(tx *gorm.DB) error { return authority.RequireLockedEdit(ctx, tx, pageID) })
	require.Equal(t, connect.CodeNotFound, errs.CodeOf(err))
}

func TestPageCollaborationSignFirstDoesNotInvertAccountSessionDeletionIntegration(t *testing.T) {
	t.Parallel()
	stack := testutil.PrepareOryIntegrationTest(t)
	user := stack.CreateUser(t, policyv1.Role.Admin().ID())
	pageID := seedPageRoot(t, stack.DB, stack.SpiceDBClient)
	signer := newBlockingHydrator()
	service := newStackService(t, stack.DB, stack.SpiceDBClient, stackPorts(t, stack.DB, stack.SpiceDBClient, signer))

	signed := make(chan error, 1)
	go func() {
		_, err := page.NewInternalConnectHandler(service).LoadPageBlockDocument(t.Context(), connect.NewRequest(&intrav1.LoadPageBlockDocumentRequest{
			PageId: pageID, Locale: "en", Principal: &intrav1.CollaborationPrincipal{SessionId: user.SessionID},
		}))
		signed <- err
	}()
	signer.awaitHeld(t, signed)

	// The bootstrap holds the Identity fence: an account session deletion
	// waits for it instead of deleting the session under the signer.
	deleted := make(chan error, 1)
	go func() {
		deleted <- identitystate.WithMutation(t.Context(), stack.DB, user.IdentityID, func(ctx context.Context, _ *gorm.DB) error {
			return stack.KratosClient.DeleteIdentitySessions(ctx, user.IdentityID)
		})
	}()
	testutil.RequireLockWaiters(t, stack.DB, 1)
	signer.release()
	require.NoError(t, <-signed)
	require.NoError(t, <-deleted)
}

func TestPageCollaborationAccountSessionDeletionFirstDoesNotWaitOnSignerIntegration(t *testing.T) {
	t.Parallel()
	// Local kratos tables keep production READ COMMITTED fences on sessions.
	stack := testutil.SetupOryStackWithLocalKratos(t)
	user := stack.CreateUser(t, policyv1.Role.Admin().ID())
	pageID := seedPageRoot(t, stack.DB, stack.SpiceDBClient)
	service := newStackService(t, stack.DB, stack.SpiceDBClient, stackPorts(t, stack.DB, stack.SpiceDBClient, nil))

	acquired, releaseDeletion := make(chan struct{}), make(chan struct{})
	deleted := make(chan error, 1)
	go func() {
		deleted <- identitystate.WithMutation(t.Context(), stack.DB, user.IdentityID, func(ctx context.Context, _ *gorm.DB) error {
			close(acquired)
			<-releaseDeletion
			// The Kratos API cannot see the local kratos copy, so delete the
			// sessions on a separate connection as Kratos would.
			return stack.DB.WithContext(ctx).Exec(`DELETE FROM kratos.sessions WHERE identity_id = ?::uuid`, user.IdentityID).Error
		})
	}()
	<-acquired

	signed := make(chan error, 1)
	go func() {
		_, err := service.LoadPageBlockDocument(t.Context(), page.LoadPageBlockDocumentQuery{PageID: pageID, Locale: "en", SessionID: user.SessionID})
		signed <- err
	}()
	// The signer resolves the session without a lock and waits only at the
	// Identity fence the deletion holds.
	testutil.RequireLockWaiters(t, stack.DB, 1)
	close(releaseDeletion)
	require.NoError(t, <-deleted)
	require.Equal(t, connect.CodeUnauthenticated, errs.CodeOf(<-signed), "the signer denies the deleted session")
}

// seedPageRoot stores a Page whose source locale and policy exist, without
// the Page use cases.
func seedPageRoot(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient) string {
	t.Helper()
	pageID, documentID := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile, revision) VALUES (?::uuid, 'page', ?::uuid)`, documentID, uuid.NewString()).Error)
	require.NoError(t, db.Exec(`INSERT INTO page (id, content_document_id) VALUES (?::uuid, ?::uuid)`, pageID, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO page_translation (entity_id, locale, title, created_at, updated_at) VALUES (?::uuid, 'en', 'Root', NOW(), NOW())`, pageID).Error)
	policy, err := policyv1.Page.TouchPolicy(pageID)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), policy)
	require.NoError(t, err)
	return pageID
}

func seedReadyImage(t *testing.T, db *gorm.DB) string {
	t.Helper()
	id := uuid.NewString()
	digest := sha256.Sum256([]byte(id))
	require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256) VALUES (?, ?, 'image/webp', 1024, 'webp', ?)`, id, id, digest[:]).Error)
	lifecycle := mediaasset.NewLifecycle(db, "https://cdn.example.com")
	asset, _, err := lifecycle.AllocatePublicAsset(t.Context(), mediaasset.Allocation{
		SourceFileID: &id, Kind: "image", Extension: "webp", MimeType: "image/webp",
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)
	assetDigest := sha256.Sum256([]byte(asset.ID))
	_, err = lifecycle.CompletePublicAsset(t.Context(), &commonv1.AssetWriteResult{AssetId: asset.ID, FileSize: 1024, Sha256: assetDigest[:]})
	require.NoError(t, err)
	return id
}

func requireManageable(t *testing.T, spiceDB *auth.SpiceDBClient, pageID, identityID string, want bool) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	resources, err := spiceDB.LookupResources(t.Context(), policyv1.Page.LookupManage(), actor)
	require.NoError(t, err)
	require.Equal(t, want, slices.Contains(resources, pageID))
}

// blockingHydrator holds a collaboration bootstrap's snapshot open, after
// it authorized and locked the session, until released.
type blockingHydrator struct {
	held     chan struct{}
	releaseC chan struct{}
	once     sync.Once
}

func newBlockingHydrator() *blockingHydrator {
	return &blockingHydrator{held: make(chan struct{}), releaseC: make(chan struct{})}
}

func (h *blockingHydrator) HydrateAuthorizedPageBlockMediaWithDB(_ context.Context, _ *gorm.DB, _ string, _ uuid.UUID, _ *auth.UserInfo, items []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error) {
	close(h.held)
	<-h.releaseC
	return items, nil
}

func (h *blockingHydrator) awaitHeld(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case <-h.held:
	case err := <-done:
		t.Fatalf("the bootstrap returned before it held its fence: %v", err)
	}
}

func (h *blockingHydrator) release() { h.once.Do(func() { close(h.releaseC) }) }

type passthroughHydrator struct{}

func (passthroughHydrator) HydrateAuthorizedPageBlockMediaWithDB(_ context.Context, _ *gorm.DB, _ string, _ uuid.UUID, _ *auth.UserInfo, items []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error) {
	return items, nil
}

type stackFeaturedImages struct{}

func (stackFeaturedImages) ResolveAuthorizedPageFeaturedImage(_ context.Context, _ string, fileID string) (*commonv1.MediaDelivery, error) {
	if fileID == "" {
		return nil, nil
	}
	return &commonv1.MediaDelivery{FileId: fileID}, nil
}

// adminFileReuse lets the administrator attach any File.
type adminFileReuse struct{}

func (adminFileReuse) AuthorizeFileReuse(context.Context, *gorm.DB, contentblock.Document, contentblock.FullBlock, contentblock.FileReference, contentblock.File) error {
	return nil
}

type noopMenuTargets struct{}

func (noopMenuTargets) UpdateSlug(context.Context, *gorm.DB, string, string, string, string) error {
	return nil
}

func (noopMenuTargets) Remove(context.Context, *gorm.DB, string, string, string) error { return nil }

type noopPublisher struct{}

func (noopPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error { return nil }
func (noopPublisher) NotifyProtobuf(context.Context, string, proto.Message) error       { return nil }
func (noopPublisher) NotifyProtobufWithExecutor(context.Context, eventpkg.DBTX, string, proto.Message) error {
	return nil
}

type renderConfig struct{}

func (renderConfig) Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error) {
	payload := []byte(`{"site_title":"","primary_color":"#b02d23"}`)
	return payload, fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}

type statementRecorder struct {
	logger.Interface
	mu         sync.Mutex
	statements []string
}

func (recorder *statementRecorder) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	sql, rows := fc()
	recorder.mu.Lock()
	recorder.statements = append(recorder.statements, sql)
	recorder.mu.Unlock()
	recorder.Interface.Trace(ctx, begin, func() (string, int64) { return sql, rows }, err)
}

func (recorder *statementRecorder) snapshot() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return slices.Clone(recorder.statements)
}
