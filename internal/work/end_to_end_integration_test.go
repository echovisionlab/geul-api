//go:build integration

package work_test

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"slices"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/work"
	workpostgres "github.com/echovisionlab/geul-api/internal/work/postgres"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

// These tests keep what only the whole stack shows: the SpiceDB schema's
// answers for the author role and the policy edges a creation and deletion
// write, the File, binding and attachment rows a Work's images and Blocks
// hold and release, and the Work root and Member rows the policy authority
// locks over real Kratos rows. Every other Work scenario runs on the fake
// Store, and the work tables on the Store contract.

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start Work integration suite: %v\n", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close Work integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

// newStackService serves the Work use cases over the PostgreSQL store and
// the production content, OG, media and share link adapters.
func newStackService(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient) *work.Service {
	t.Helper()
	contents, err := contentblock.NewGeneratedStore(adminFileReuse{})
	require.NoError(t, err)
	planner := og.NewPlanner(db, "https://cdn.example.com", renderConfig{}, workadapter.NewProjection())
	units, err := workpostgres.NewUnits(db, workpostgres.Ports{
		ContentBlocks: contents, Signals: noopPublisher{}, Checkpoints: testcollaboration.NewCheckpoints(db, spiceDB),
		Runtime:    workadapter.NewRuntime(db, "https://cdn.example.com", og.NewRefresher(planner, og.NewResolver(workadapter.NewRequests()))),
		Media:      passthroughHydrator{},
		ShareLinks: sharelinkadapter.EntityLinks{Type: sharelink.EntityWork},
	})
	require.NoError(t, err)
	service, err := work.NewService(work.Deps{
		Units: units, SpiceDB: spiceDB, Fence: identitystate.PostgresFence{}, Audit: &audittest.Recorder{},
		Members: workadapter.NewMemberSummaries(db, ""),
	})
	require.NoError(t, err)
	return service
}

// newStackAdmin is a Member whose Identity holds the administrator role.
func newStackAdmin(t *testing.T, stack *testutil.OryStack) (identityID string, ctx context.Context) {
	t.Helper()
	identityID = testutil.IntegrationUUID()
	memberID := testutil.SeedPostIntegrationIdentity(t, stack.DB, identityID, "Work integration admin")
	testutil.GrantIntegrationGlobalRole(t, stack.SpiceDBClient, identityID, policyv1.Role.Admin())
	return identityID, testutil.NewAuditContext(t, identityID, memberID)
}

func createStackWork(t *testing.T, ctx context.Context, works *work.ConnectHandler, title string) *managev1.Work {
	t.Helper()
	present := true
	created, err := works.CreateWork(ctx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title: title + " " + testutil.IntegrationUUID(), Type: managev1.WorkType_WORK_TYPE_ARTICLE,
		Year: 2026, Month: 8, IsPresent: &present,
		Document: &contentv1.RichTextDocument{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Profile: contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK,
			SourceLocale: "en", Base: &contentv1.RichTextBlockGraph{}, LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{Locale: "en"}},
		},
	}))
	require.NoError(t, err)
	return created.Msg
}

// The SpiceDB schema lets the author role read an archived Work and nothing
// else of it: a draft is hidden, edits answer not found and slug checks are
// refused.
func TestWorkAuthorRoleReadsOnlyArchivedWorksIntegration(t *testing.T) {
	t.Parallel()
	stack := testutil.PrepareOryIntegrationTest(t)
	db := stack.DB
	_, adminCtx := newStackAdmin(t, stack)
	authorID := testutil.IntegrationUUID()
	authorMemberID := testutil.SeedPostIntegrationIdentity(t, db, authorID, "Work author")
	testutil.GrantIntegrationGlobalRole(t, stack.SpiceDBClient, authorID, policyv1.Role.Author())
	authorCtx := testutil.NewAuditContext(t, authorID, authorMemberID)
	works := work.NewConnectHandler(newStackService(t, db, stack.SpiceDBClient))
	created := createStackWork(t, adminCtx, works, "Work author access")

	_, err := works.GetWork(authorCtx, connect.NewRequest(&managev1.GetWorkRequest{Id: created.Id}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	require.NoError(t, db.Model(&model.Work{}).Where("id = ?", created.Id).
		Update("status", managev1.WorkStatus_WORK_STATUS_ARCHIVED.String()).Error)
	archived, err := works.GetWork(authorCtx, connect.NewRequest(&managev1.GetWorkRequest{Id: created.Id}))
	require.NoError(t, err)
	require.Equal(t, managev1.WorkStatus_WORK_STATUS_ARCHIVED, archived.Msg.Status)

	slug := "work-author-" + uuid.NewString()
	_, err = works.UpdateWork(authorCtx, connect.NewRequest(&managev1.UpdateWorkRequest{Id: created.Id, Slug: &slug}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = works.CheckWorkSlugAvailable(authorCtx, connect.NewRequest(&managev1.CheckWorkSlugAvailableRequest{Slug: slug}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// A Work's featured images and File Blocks reference library Files and
// never delete them; deletion releases the bindings, attachments and policy
// edge and leaves the Files and their ready assets.
func TestWorkMediaReleaseKeepsLibraryFilesAndDeletionRemovesThePolicyIntegration(t *testing.T) {
	t.Parallel()
	stack := testutil.PrepareOryIntegrationTest(t)
	db, spiceDB := stack.DB, stack.SpiceDBClient
	identityID, ctx := newStackAdmin(t, stack)
	service := newStackService(t, db, spiceDB)
	works := work.NewConnectHandler(service)
	created := createStackWork(t, ctx, works, "Work file retention")
	requireManageable(t, spiceDB, created.Id, identityID, true)

	first, second := seedReadyImage(t, db), seedReadyImage(t, db)
	for _, fileID := range []string{first, second} {
		_, err := works.SetWorkFeaturedImage(ctx, connect.NewRequest(&managev1.SetWorkFeaturedImageRequest{WorkId: created.Id, FileId: fileID}))
		require.NoError(t, err)
	}
	_, err := works.DeleteWorkFeaturedImage(ctx, connect.NewRequest(&managev1.DeleteWorkFeaturedImageRequest{WorkId: created.Id}))
	require.NoError(t, err)
	requireCount(t, db, 2, "file", "id IN ?", []string{first, second})

	editorFile, featuredFile := seedReadyImage(t, db), seedReadyImage(t, db)
	blockID := testutil.IntegrationUUID()
	applied, err := work.NewInternalConnectHandler(service).ApplyWorkBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyWorkBlockBatchRequest{
		WorkId: created.Id, Locale: "en",
		Batch: &contentv1.RichTextBlockMutationBatch{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Profile: contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK,
			ExpectedRevision: created.Revision, ContributorMemberIds: []string{testutil.PostIntegrationMemberID(identityID)},
			BaseMutations: []*contentv1.RichTextBlockMutation{{Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{
				Node: &contentv1.RichTextBlockNode{
					Block: &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_File{File: &contentv1.FileBlock{Props: &contentv1.FileProps{
						Attachment: &contentv1.FileAttachment{State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: editorFile}},
					}}}},
					Placement: &contentv1.ContentBlockPlacement{Index: 0},
				},
			}}}},
			LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{Locale: "en", Mutations: []*contentv1.RichTextBlockLocaleMutation{{
				Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{Block: &contentv1.RichTextBlockLocale{
					BlockId: blockID, Value: &contentv1.RichTextBlockLocale_File{File: &contentv1.FileBlockLocale{Props: &contentv1.FileLocaleProps{}}},
				}}},
			}}}},
		},
	}))
	require.NoError(t, err)
	require.True(t, applied.Msg.Changed)
	requireCount(t, db, 1, "content_block_attachment", "block_id = ? AND reference_path = 'file' AND file_id = ?", blockID, editorFile)
	_, err = works.SetWorkFeaturedImage(ctx, connect.NewRequest(&managev1.SetWorkFeaturedImageRequest{WorkId: created.Id, FileId: featuredFile}))
	require.NoError(t, err)
	var binding model.PublicAssetBinding
	require.NoError(t, db.Where("owner_type = 'work' AND owner_id = ? AND binding_key = 'featured_image'", created.Id).Take(&binding).Error)

	deleted, err := works.DeleteWork(ctx, connect.NewRequest(&managev1.DeleteWorkRequest{Id: created.Id}))
	require.NoError(t, err)
	require.True(t, deleted.Msg.Success)
	requireManageable(t, spiceDB, created.Id, identityID, false)
	requireCount(t, db, 2, "file", "id IN ?", []string{editorFile, featuredFile})
	requireCount(t, db, 0, "public_asset_binding", "owner_type = 'work' AND owner_id = ?", created.Id)
	requireCount(t, db, 1, "public_asset", "id = ? AND status = ?", binding.AssetID, model.PublicAssetStatusReady)
	requireCount(t, db, 0, "content_block_attachment", "block_id = ?", blockID)
}

// blockingChecker holds the policy authority inside its permission check
// until released.
type blockingChecker struct {
	entered chan policyv1.AuthorizationDecision
	release chan struct{}
}

func (c *blockingChecker) Can(_ context.Context, decision policyv1.AuthorizationDecision) (bool, error) {
	c.entered <- decision
	<-c.release
	return true, nil
}

// The policy authority checks an archived Work's exact lifecycle
// permission while it holds the Work root and the caller's Member row, so a
// status change or a deactivation waits for the decision.
func TestWorkPolicyAuthorityLocksArchivedRootAndActivePrincipalIntegration(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		require  func(context.Context, work.Joined, *blockingChecker, string) error
		expected func(string) (policyv1.Can, error)
		mutate   func(db *gorm.DB, workID, memberID string) error
	}{
		"view locks the Work root": {
			require: func(ctx context.Context, joined work.Joined, checker *blockingChecker, workID string) error {
				return work.RequireLockedView(ctx, joined, identitystate.PostgresFence{}, checker, workID)
			},
			expected: policyv1.Work.ViewArchived,
			mutate: func(db *gorm.DB, workID, _ string) error {
				return db.Exec(`UPDATE work SET status = ? WHERE id = ?::uuid`, managev1.WorkStatus_WORK_STATUS_DRAFT.String(), workID).Error
			},
		},
		"edit locks the active principal": {
			require: func(ctx context.Context, joined work.Joined, checker *blockingChecker, workID string) error {
				return work.RequireLockedEdit(ctx, joined, identitystate.PostgresFence{}, checker, workID)
			},
			expected: policyv1.Work.EditArchived,
			mutate: func(db *gorm.DB, _, memberID string) error {
				return db.Exec(`UPDATE member SET onboarded = FALSE WHERE id = ?::uuid`, memberID).Error
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db := testutil.PrepareOryIntegrationConcurrentTest(t).DB
			identityID := testutil.IntegrationUUID()
			memberID := testutil.SeedPostIntegrationIdentity(t, db, identityID, "Work policy authority")
			documentID, workID := testutil.IntegrationUUID(), testutil.IntegrationUUID()
			require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile, revision) VALUES (?::uuid, 'work', ?::uuid)`, documentID, testutil.IntegrationUUID()).Error)
			require.NoError(t, db.Exec(`INSERT INTO work (id, type, status, year, month, is_present, content_document_id) VALUES (?::uuid, ?::work_type, ?, 2026, 8, TRUE, ?::uuid)`,
				workID, managev1.WorkType_WORK_TYPE_ARTICLE.String(), managev1.WorkStatus_WORK_STATUS_ARCHIVED.String(), documentID).Error)
			checker := &blockingChecker{entered: make(chan policyv1.AuthorizationDecision, 1), release: make(chan struct{})}
			ctx := testutil.NewAuditContext(t, identityID, memberID)

			authorized := make(chan error, 1)
			go func() {
				authorized <- db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					return test.require(ctx, workpostgres.Bind(tx, workpostgres.Ports{}, nil), checker, workID)
				})
			}()
			var decision policyv1.AuthorizationDecision
			select {
			case decision = <-checker.entered:
			case err := <-authorized:
				t.Fatalf("the policy authority returned before its permission check: %v", err)
			}
			expected, err := test.expected(workID)
			require.NoError(t, err)
			require.Equal(t, expected.Action().Permission(), decision.Action().Permission())

			mutated := make(chan error, 1)
			go func() { mutated <- test.mutate(db, workID, memberID) }()
			testutil.RequireLockWaiters(t, db, 1)
			close(checker.release)
			require.NoError(t, <-authorized)
			require.NoError(t, <-mutated)
		})
	}
}

func requireManageable(t *testing.T, spiceDB *auth.SpiceDBClient, workID, identityID string, want bool) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	resources, err := spiceDB.LookupResources(t.Context(), policyv1.Work.LookupManage(), actor)
	require.NoError(t, err)
	require.Equal(t, want, slices.Contains(resources, workID))
}

func requireCount(t *testing.T, db *gorm.DB, want int64, table, where string, args ...any) {
	t.Helper()
	var count int64
	require.NoError(t, db.Table(table).Where(where, args...).Count(&count).Error)
	require.Equal(t, want, count, "%s where %s", table, where)
}

// seedReadyImage stores a library File with a ready image asset.
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

// adminFileReuse lets the administrator attach any File.
type adminFileReuse struct{}

func (adminFileReuse) AuthorizeFileReuse(context.Context, *gorm.DB, contentblock.Document, contentblock.FullBlock, contentblock.FileReference, contentblock.File) error {
	return nil
}

type passthroughHydrator struct{}

func (passthroughHydrator) HydrateAuthorizedWorkBlockMediaWithDB(_ context.Context, _ *gorm.DB, _ string, _ uuid.UUID, _ *auth.UserInfo, items []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error) {
	return items, nil
}

type noopPublisher struct{}

func (noopPublisher) NotifyProtobufWithExecutor(context.Context, eventpkg.DBTX, string, proto.Message) error {
	return nil
}

type renderConfig struct{}

func (renderConfig) Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error) {
	payload := []byte(`{"site_title":"","primary_color":"#b02d23"}`)
	return payload, fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}
