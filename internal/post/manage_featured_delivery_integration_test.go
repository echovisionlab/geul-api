//go:build integration

package post_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	filemediaadapter "github.com/echovisionlab/geul-api/internal/adapters/filemedia"
	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/postintegration"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func TestManagePostFeaturedImageUsesExactFinalOwnerBoundaryIntegration(t *testing.T) {
	t.Run("public reader cannot enter manage private delivery", func(t *testing.T) {
		fixture := newManagePostFeaturedDeliveryFixture(t)
		fixture.files.beforeExact = func() { t.Fatal("anonymous manage read reached the private File signer") }

		_, err := fixture.posts.GetPost(context.Background(), connect.NewRequest(&managev1.GetPostRequest{Id: fixture.postID}))
		require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	})

	t.Run("current collaborator receives inline without original download", func(t *testing.T) {
		fixture := newManagePostFeaturedDeliveryFixture(t)

		response, err := fixture.posts.GetPost(fixture.editorCtx, connect.NewRequest(&managev1.GetPostRequest{Id: fixture.postID}))
		require.NoError(t, err)
		require.NotEmpty(t, response.Msg.GetFeaturedImageDelivery().GetInline().GetUrl())
		require.Nil(t, response.Msg.GetFeaturedImageDelivery().GetDownload())
		require.Equal(t, fixture.fileID, fixture.files.expectedFileID)
	})

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
	posts     *postdomain.PostService
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

	fileService := filemedia.NewFileService(
		db,
		s3.New(s3.Options{Region: "integration"}),
		postFeaturedAsyncPublisher{},
		"integration",
		"https://cdn.example.test",
		"https://media.example.test",
		"post-featured-integration-secret",
		postFeaturedTranscoderPublisher{},
		spiceDB,
		filemedia.WithPostAccess(filemediaadapter.NewPostAccess(db, spiceDB)),
	)
	manageFiles := &postFeaturedManageFiles{delegate: fileService}
	postFiles := postadapter.NewFiles(manageFiles, postFeaturedPublicDisplayFiles{})
	posts := postintegration.NewPostDomainServiceWithFileService(
		t,
		db,
		"https://cdn.example.test",
		spiceDB,
		testutil.NewPostIdentityManager(
			testutil.PostIntegrationIdentity(adminID, "en"),
			testutil.PostIntegrationIdentity(editorID, "en"),
		),
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
	delegate       *filemedia.FileService
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
) (*commonv1.MediaDelivery, error) {
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

type postFeaturedTranscoderPublisher struct{}

func (postFeaturedTranscoderPublisher) PublishTranscodeAudio(context.Context, *managev1.TranscodeAudioEvent) error {
	return nil
}
func (postFeaturedTranscoderPublisher) PublishTranscodeVideo(context.Context, *managev1.TranscodeVideoEvent) error {
	return nil
}
func (postFeaturedTranscoderPublisher) PublishWaveformCancel(context.Context, *managev1.WaveformCancelEvent) error {
	return nil
}
