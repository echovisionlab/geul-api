//go:build integration

package post_test

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"

	filemediapostgres "github.com/echovisionlab/geul-api/internal/filemedia/postgres"
	"github.com/echovisionlab/geul-api/internal/identitystate"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	filemediaadapter "github.com/echovisionlab/geul-api/internal/adapters/filemedia"
	"github.com/echovisionlab/geul-api/internal/adapters/filemediapolicy"
	"github.com/echovisionlab/geul-api/internal/adapters/filemediaqueue"
	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/postintegration"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// The File signer rechecks the featured image against live authority and
// the stored Post after the domain read it; who may enter the read and
// which File it names are unit tests.
func TestManagePostFeaturedImageUsesExactFinalOwnerBoundaryIntegration(t *testing.T) {
	t.Parallel()
	t.Run("permission revoked after pre-read cannot issue", func(t *testing.T) {
		fixture := newManagePostFeaturedDeliveryFixture(t)
		fixture.files.beforeExact = func() {
			_, err := fixture.posts.RemovePostCollaborator(fixture.adminCtx, connect.NewRequest(&managev1.RemovePostCollaboratorRequest{
				PostId: fixture.postID, MemberId: testutil.PostIntegrationMemberID(fixture.editorID),
			}))
			require.NoError(t, err)
		}

		response, err := fixture.posts.GetPost(fixture.editorCtx, connect.NewRequest(&managev1.GetPostRequest{Id: fixture.postID}))
		require.Nil(t, response)
		require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	})

	t.Run("featured replacement after pre-read cannot sign stale file", func(t *testing.T) {
		fixture := newManagePostFeaturedDeliveryFixture(t)
		replacementID := testutil.PostIntegrationUUID()
		seedManagePostFeaturedFile(t, fixture.db, replacementID)
		fixture.files.beforeExact = func() {
			require.NoError(t, fixture.db.Table("post").Where("id = ?::uuid", fixture.postID).
				Update("featured_image_file_id", replacementID).Error)
		}

		response, err := fixture.posts.GetPost(fixture.adminCtx, connect.NewRequest(&managev1.GetPostRequest{Id: fixture.postID}))
		require.NoError(t, err)
		require.Nil(t, response.Msg.GetFeaturedImageDelivery())
		require.Equal(t, fixture.fileID, fixture.files.expectedFileID)
	})

	t.Run("featured detach after pre-read cannot sign stale file", func(t *testing.T) {
		fixture := newManagePostFeaturedDeliveryFixture(t)
		fixture.files.beforeExact = func() {
			require.NoError(t, fixture.db.Table("post").Where("id = ?::uuid", fixture.postID).
				Update("featured_image_file_id", nil).Error)
		}

		response, err := fixture.posts.GetPost(fixture.adminCtx, connect.NewRequest(&managev1.GetPostRequest{Id: fixture.postID}))
		require.NoError(t, err)
		require.Nil(t, response.Msg.GetFeaturedImageDelivery())
	})
}

type managePostFeaturedDeliveryFixture struct {
	db        *gorm.DB
	posts     postintegration.Posts
	files     *postFeaturedManageFiles
	adminCtx  context.Context
	editorCtx context.Context
	editorID  string
	postID    string
	fileID    string
}

func newManagePostFeaturedDeliveryFixture(t *testing.T) managePostFeaturedDeliveryFixture {
	t.Helper()
	db := testutil.NewPostIntegrationDB(t)
	adminID := testutil.PostIntegrationUUID()
	editorID := testutil.PostIntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, adminID, "Manage Post featured admin")
	testutil.SeedPostIntegrationIdentity(t, db, editorID, "Manage Post featured collaborator")
	spiceDB := testutil.PostIntegrationSpiceDB(t)
	testutil.GrantPostIntegrationRole(t, spiceDB, adminID, policyv1.Role.Admin())
	testutil.GrantPostIntegrationRole(t, spiceDB, editorID, policyv1.Role.User())
	adminCtx := testutil.PostIntegrationContext(adminID)
	editorCtx := testutil.PostIntegrationContext(editorID)
	store := testutil.NewPostContentBlockStore(t)

	filePostAccess := filemediaadapter.NewPostAccess(spiceDB)
	filePorts := filemediapostgres.Ports{PolicyOwners: filemediapolicy.NewPolicyOwners(filemediapolicy.PolicyOwnerPorts{Posts: filePostAccess, SpiceDB: spiceDB}), PolicyAudiences: filemediapolicy.Audiences{}, PolicySignals: filemediaqueue.Policy(postFeaturedAsyncPublisher{})}
	fileService, err := filemedia.NewService(filemedia.Deps{
		IngestLocks:     filemediapostgres.NewIngestLocks(db, filePorts),
		FaviconCommits:  filemediapostgres.NewFaviconCommits(db, filePorts),
		Units:           filemediapostgres.NewUnits(db, filePorts),
		Fence:           identitystate.PostgresFence{},
		S3:              s3.New(s3.Options{Region: "integration"}),
		AsyncPublisher:  postFeaturedAsyncPublisher{},
		S3Bucket:        "integration",
		CDNDomain:       "https://cdn.example.test",
		MediaDomain:     "https://media.example.test",
		MediaSecret:     "post-featured-integration-secret",
		SpiceDB:         spiceDB,
		Audit:           &audittest.Recorder{},
		LegalRoutes:     postFeaturedUnusedLegalRoutes{},
		MemberSummaries: postFeaturedUnusedMemberSummaries{},
	})
	require.NoError(t, err)
	manageFiles := &postFeaturedManageFiles{delegate: fileService}
	postFiles := postadapter.NewFiles(manageFiles, postFeaturedPublicDisplayFiles{})
	posts := postintegration.NewPostDomainServiceWithFileService(
		t,
		db,
		"https://cdn.example.test",
		spiceDB,
		store,
		postFiles,
	)
	slug := "manage-featured-" + testutil.PostIntegrationUUID()
	created, err := posts.CreatePost(adminCtx, connect.NewRequest(&managev1.CreatePostRequest{
		Title: "Manage featured delivery", Slug: &slug,
	}))
	require.NoError(t, err)
	_, err = posts.AddPostCollaborator(adminCtx, connect.NewRequest(&managev1.AddPostCollaboratorRequest{
		PostId: created.Msg.Id, MemberId: testutil.PostIntegrationMemberID(editorID),
	}))
	require.NoError(t, err)
	_, err = posts.PublishPost(adminCtx, connect.NewRequest(&managev1.PublishPostRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	fileID := testutil.PostIntegrationUUID()
	seedManagePostFeaturedFile(t, db, fileID)
	require.NoError(t, db.Table("post").Where("id = ?::uuid", created.Msg.Id).
		Update("featured_image_file_id", fileID).Error)

	return managePostFeaturedDeliveryFixture{
		db: db, posts: posts, files: manageFiles,
		adminCtx: adminCtx, editorCtx: editorCtx, editorID: editorID,
		postID: created.Msg.Id, fileID: fileID,
	}
}

func seedManagePostFeaturedFile(t *testing.T, db *gorm.DB, fileID string) {
	t.Helper()
	require.NoError(t, db.Exec(`
		INSERT INTO file (id, file_name, extension, mime_type, file_size)
		VALUES (?::uuid, 'featured', 'png', 'image/png', 1024)
	`, fileID).Error)
}

type postFeaturedManageFiles struct {
	delegate       *filemedia.Service
	beforeExact    func()
	expectedFileID string
}

func (f *postFeaturedManageFiles) DeleteFileByID(ctx context.Context, fileID string) error {
	return f.delegate.DeleteFileByID(ctx, fileID)
}

func (f *postFeaturedManageFiles) ResolveAuthorizedPostFeaturedImage(
	ctx context.Context,
	postID string,
	expectedFileID string,
) (*filemedia.Delivery, error) {
	f.expectedFileID = expectedFileID
	if f.beforeExact != nil {
		before := f.beforeExact
		f.beforeExact = nil
		before()
	}
	return f.delegate.ResolveAuthorizedPostFeaturedImage(ctx, postID, expectedFileID)
}

type postFeaturedPublicDisplayFiles struct{}

func (postFeaturedPublicDisplayFiles) ResolvePublicDisplayMedia(
	context.Context,
	[]string,
) (map[string]*commonv1.MediaDelivery, error) {
	return map[string]*commonv1.MediaDelivery{}, nil
}

type postFeaturedAsyncPublisher struct{}

func (postFeaturedAsyncPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}
func (postFeaturedAsyncPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}

func (publisher postFeaturedAsyncPublisher) NotifyProtobufWithExecutor(
	ctx context.Context,
	_ eventpkg.DBTX,
	signal string,
	message proto.Message,
) error {
	return publisher.NotifyProtobuf(ctx, signal, message)
}

// The ports this test does not reach panic if called.
type postFeaturedUnusedLegalRoutes struct{ filemedia.LegalRouteIdentity }

type postFeaturedUnusedMemberSummaries struct{ filemedia.MemberSummaries }

func (publisher postFeaturedAsyncPublisher) PublishIngest(ctx context.Context, event domain.IngestEvent) error {
	return filemediaqueue.Lifecycle(publisher).PublishIngest(ctx, event)
}
func (publisher postFeaturedAsyncPublisher) PublishProcessing(ctx context.Context, event domain.ProcessingEvent) error {
	return filemediaqueue.Lifecycle(publisher).PublishProcessing(ctx, event)
}
