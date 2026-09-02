//go:build integration

package public_test

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	postruntime "github.com/echovisionlab/geul-api/internal/adapters/post/runtime"
	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	postpublic "github.com/echovisionlab/geul-api/internal/post/public"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/postintegration"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestPostPublicReadSurfacesEnforcePublicStatusFenceIntegration(t *testing.T) {
	stack, err := testutil.StartBackendIntegrationStack(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stack.Close()) })
	db := stack.Postgres.DB
	spiceDB := stack.SpiceDBClient

	adminID := uuid.NewString()
	testutil.SeedPostIntegrationIdentity(t, db, adminID, "Public Reference Post Admin")
	testutil.GrantPostIntegrationRole(t, spiceDB, adminID, policyv1.Role.Admin())
	ctx := testutil.PostIntegrationContext(adminID)
	store := testutil.NewPostContentBlockStore(t)
	managePosts := postintegration.NewPostDomainService(
		t, db, "", spiceDB,
		testutil.NewPostIdentityManager(testutil.PostIntegrationIdentity(adminID, "en")),
		store,
	)
	publicPosts := postpublic.NewPostService(
		db, "", spiceDB, referenceSearchFileService{}, postadapter.NewLocalization(),
		referencecatalogadapter.PublicMapPlaces{}, postadapter.NewMemberSummaries(db, ""),
		postruntime.ShareLinks{}, postpublic.WithPostContentBlockStore(store),
	)
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	mapPlaceID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO map_place (id, name, address, lat, lng, created_at, updated_at)
		VALUES (?::uuid, ?, ?, 37.539639, 126.9904063, NOW(), NOW())
	`, mapPlaceID, "Public Post Place "+suffix, "Public Post Road").Error)
	categoryID := testutil.SeedPostIntegrationCategory(t, db, "Public Ref Category "+suffix, "public-ref-category-"+suffix)
	tagID := testutil.SeedPostIntegrationTag(t, db, "Public Ref Tag "+suffix, "public-ref-tag-"+suffix)
	otherCategoryID := testutil.SeedPostIntegrationCategory(t, db, "Other Public Ref Category "+suffix, "other-public-ref-category-"+suffix)
	otherTagID := testutil.SeedPostIntegrationTag(t, db, "Other Ref Tag "+suffix, "other-ref-tag-"+suffix)

	draft := createReferenceSearchPost(t, managePosts, ctx, "Draft Reference Post "+suffix, categoryID, tagID, mapPlaceID, false)
	firstPublished := createReferenceSearchPost(t, managePosts, ctx, "First Published Reference Post "+suffix, categoryID, tagID, mapPlaceID, true)
	archived := createReferenceSearchPost(t, managePosts, ctx, "Archived Reference Post "+suffix, categoryID, tagID, mapPlaceID, true)
	archivedResponse, err := managePosts.ArchivePost(ctx, connect.NewRequest(&managev1.ArchivePostRequest{Id: archived.Id}))
	require.NoError(t, err)
	require.Equal(t, managev1.PostStatus_POST_STATUS_ARCHIVED, archivedResponse.Msg.Status)
	otherPublished := createReferenceSearchPost(t, managePosts, ctx, "Other Published Reference Post "+suffix, otherCategoryID, otherTagID, mapPlaceID, true)

	response, err := publicPosts.Search(context.Background(), connect.NewRequest(&openv1.SearchPostsRequest{
		Query: "Reference Post " + suffix,
		Limit: 10,
	}))
	require.NoError(t, err)
	require.Len(t, response.Msg.Posts, 3)
	titles := make([]string, 0, len(response.Msg.Posts))
	for _, post := range response.Msg.Posts {
		titles = append(titles, post.GetTitle())
	}
	require.NotContains(t, titles, "Draft Reference Post "+suffix)
	require.Contains(t, titles, "First Published Reference Post "+suffix)
	require.Contains(t, titles, "Archived Reference Post "+suffix)
	require.NotContains(t, postIDs(response.Msg.Posts), draft.Id)

	searchFilter := &commonv1.FilterSpec{
		Field: "search",
		Op:    commonv1.FilterOp_FILTER_OP_ILIKE,
		Value: "Reference Post " + suffix,
	}
	publicIDs := []string{firstPublished.Id, archived.Id, otherPublished.Id}
	for _, test := range []struct {
		name     string
		filter   *commonv1.FilterSpec
		expected []string
	}{
		{name: "no status filter", expected: publicIDs},
		{
			name: "published equality",
			filter: &commonv1.FilterSpec{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ,
				Value: managev1.PostStatus_POST_STATUS_PUBLISHED.String()},
			expected: []string{firstPublished.Id, otherPublished.Id},
		},
		{
			name: "public status inclusion",
			filter: &commonv1.FilterSpec{Field: "status", Op: commonv1.FilterOp_FILTER_OP_IN,
				Values: []string{
					managev1.PostStatus_POST_STATUS_PUBLISHED.String(),
					managev1.PostStatus_POST_STATUS_ARCHIVED.String(),
				}},
			expected: publicIDs,
		},
		{
			name: "archived inequality",
			filter: &commonv1.FilterSpec{Field: "status", Op: commonv1.FilterOp_FILTER_OP_NEQ,
				Value: managev1.PostStatus_POST_STATUS_ARCHIVED.String()},
			expected: []string{firstPublished.Id, otherPublished.Id},
		},
		{
			name: "archived exclusion",
			filter: &commonv1.FilterSpec{Field: "status", Op: commonv1.FilterOp_FILTER_OP_NOT_IN,
				Values: []string{managev1.PostStatus_POST_STATUS_ARCHIVED.String()}},
			expected: []string{firstPublished.Id, otherPublished.Id},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			filters := []*commonv1.FilterSpec{searchFilter}
			if test.filter != nil {
				filters = append(filters, test.filter)
			}

			listResponse, listErr := publicPosts.List(context.Background(), connect.NewRequest(&openv1.ListPostsRequest{
				Filters: filters,
			}))
			require.NoError(t, listErr)
			require.ElementsMatch(t, test.expected, postIDs(listResponse.Msg.Posts))
			require.NotContains(t, postIDs(listResponse.Msg.Posts), draft.Id)

			mapResponse, mapErr := publicPosts.ListMapFeatures(context.Background(), connect.NewRequest(&openv1.ListPostMapFeaturesRequest{
				Viewport: publicPostStatusViewport(),
				Filters:  filters,
			}))
			require.NoError(t, mapErr)
			require.Empty(t, mapResponse.Msg.Clusters)
			if len(test.expected) == 0 {
				require.Empty(t, mapResponse.Msg.Items)
				return
			}
			require.Len(t, mapResponse.Msg.Items, 1)
			require.Equal(t, mapPlaceID, mapResponse.Msg.Items[0].PlaceId)
			require.EqualValues(t, len(test.expected), mapResponse.Msg.Items[0].PostCount)
		})
	}

	for _, test := range []struct {
		name   string
		filter *commonv1.FilterSpec
	}{
		{
			name: "draft inequality",
			filter: &commonv1.FilterSpec{Field: "status", Op: commonv1.FilterOp_FILTER_OP_NEQ,
				Value: managev1.PostStatus_POST_STATUS_DRAFT.String()},
		},
		{
			name: "draft exclusion",
			filter: &commonv1.FilterSpec{Field: "status", Op: commonv1.FilterOp_FILTER_OP_NOT_IN,
				Values: []string{managev1.PostStatus_POST_STATUS_DRAFT.String()}},
		},
	} {
		t.Run(test.name+" is rejected", func(t *testing.T) {
			filters := []*commonv1.FilterSpec{searchFilter, test.filter}
			_, listErr := publicPosts.List(context.Background(), connect.NewRequest(&openv1.ListPostsRequest{Filters: filters}))
			require.Error(t, listErr)
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(listErr))

			_, mapErr := publicPosts.ListMapFeatures(context.Background(), connect.NewRequest(&openv1.ListPostMapFeaturesRequest{
				Viewport: publicPostStatusViewport(),
				Filters:  filters,
			}))
			require.Error(t, mapErr)
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(mapErr))
		})
	}
}

func postIDs(posts []*openv1.PostSummary) []string {
	ids := make([]string, 0, len(posts))
	for _, post := range posts {
		ids = append(ids, post.GetId())
	}
	return ids
}

func publicPostStatusViewport() *openv1.PostMapViewport {
	return &openv1.PostMapViewport{
		Bounds: &openv1.MapBounds{West: 126, South: 37, East: 128, North: 38},
		Zoom:   12, WidthPx: 1280, HeightPx: 720,
		ClusterRadiusPx: 56, MinClusterPoints: 2,
	}
}

func createReferenceSearchPost(
	t *testing.T,
	service *postdomain.PostService,
	ctx context.Context,
	title string,
	categoryID string,
	tagID string,
	mapPlaceID string,
	publish bool,
) *managev1.Post {
	t.Helper()
	response, err := service.CreatePost(ctx, connect.NewRequest(&managev1.CreatePostRequest{
		Title: title, CommentsEnabled: true,
		CategoryIds: []string{categoryID}, TagIds: []string{tagID},
		MapPlaceId: &mapPlaceID,
		Document:   testutil.EmptyPostDocument("en"),
	}))
	require.NoError(t, err)
	if !publish {
		return response.Msg
	}
	published, err := service.PublishPost(ctx, connect.NewRequest(&managev1.PublishPostRequest{Id: response.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, managev1.PostStatus_POST_STATUS_PUBLISHED, published.Msg.Status)
	return response.Msg
}

type referenceSearchFileService struct{}

func (referenceSearchFileService) ResolvePublicDisplayMedia(context.Context, []string) (map[string]*commonv1.MediaDelivery, error) {
	return map[string]*commonv1.MediaDelivery{}, nil
}

func (referenceSearchFileService) ResolveAuthorizedContentBlockMedia(
	context.Context,
	uuid.UUID,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return nil, nil
}

var _ postpublic.FileService = referenceSearchFileService{}
