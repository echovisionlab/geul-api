//go:build integration

package post_test

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/postintegration"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestPostManageReadQueryBudgetsStayConstant(t *testing.T) {
	db := testutil.NewPostIntegrationDB(t)
	adminID := testutil.PostIntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, adminID, "Post query admin")
	spiceDB := testutil.PostIntegrationSpiceDB(t)
	testutil.GrantPostIntegrationRole(t, spiceDB, adminID, policyv1.Role.Admin())
	ctx := testutil.PostIntegrationContext(adminID)
	identityManager := testutil.NewPostIdentityManager(testutil.PostIntegrationIdentity(adminID, "en"))
	contentBlocks := testutil.NewPostContentBlockStore(t)
	seedService := postintegration.NewPostDomainService(
		t, db, "", spiceDB, identityManager, contentBlocks,
	)

	postIDs := make([]string, 0, 12)
	for index := 0; index < 12; index++ {
		slug := fmt.Sprintf("pq-%s-%02d", strings.ReplaceAll(testutil.PostIntegrationUUID(), "-", ""), index)
		created, err := seedService.CreatePost(ctx, connect.NewRequest(&managev1.CreatePostRequest{
			Title:    fmt.Sprintf("Post query budget %02d", index),
			Slug:     &slug,
			Document: testutil.EmptyPostDocument("en"),
		}))
		require.NoError(t, err)
		postIDs = append(postIDs, created.Msg.Id)
	}

	var queryCount atomic.Int64
	countedDB := db.Session(&gorm.Session{Logger: testutil.QueryCounter{
		Interface: db.Config.Logger,
		Count:     &queryCount,
	}})
	service := postintegration.NewPostDomainService(
		t, countedDB, "", spiceDB, identityManager, contentBlocks,
	)
	listCount := func(limit int32) int64 {
		queryCount.Store(0)
		response, err := service.ListPostsAdmin(ctx, connect.NewRequest(&managev1.ListPostsAdminRequest{
			Pagination: &commonv1.PaginationRequest{Limit: limit},
		}))
		require.NoError(t, err)
		require.NotEmpty(t, response.Msg.Posts)
		return queryCount.Load()
	}
	onePostQueries := listCount(1)
	twelvePostQueries := listCount(12)
	require.Equal(t, onePostQueries, twelvePostQueries)
	require.LessOrEqual(t, twelvePostQueries, int64(12))

	queryCount.Store(0)
	_, err := service.GetPost(ctx, connect.NewRequest(&managev1.GetPostRequest{Id: postIDs[0]}))
	require.NoError(t, err)
	require.LessOrEqual(t, queryCount.Load(), int64(12))

	participantCount := func() int64 {
		queryCount.Store(0)
		response, err := service.ListPostParticipants(ctx, connect.NewRequest(&managev1.ListPostParticipantsRequest{PostId: postIDs[0]}))
		require.NoError(t, err)
		require.NotEmpty(t, response.Msg.Participants)
		return queryCount.Load()
	}
	oneParticipantQueries := participantCount()
	for index := 0; index < 8; index++ {
		identityID := testutil.PostIntegrationUUID()
		testutil.SeedPostIntegrationIdentity(t, db, identityID, fmt.Sprintf("Post collaborator %02d", index))
		require.NoError(t, db.Exec(
			`INSERT INTO post_collaborator (post_id, member_id, created_at) VALUES (?::uuid, ?::uuid, NOW())`,
			postIDs[0], testutil.PostIntegrationMemberID(identityID),
		).Error)
	}
	nineParticipantQueries := participantCount()
	require.Equal(t, oneParticipantQueries, nineParticipantQueries)
	require.LessOrEqual(t, nineParticipantQueries, int64(5))
}

func TestPostServiceParticipantWorkflowIntegration(t *testing.T) {
	db := testutil.NewPostIntegrationDB(t)
	adminID := testutil.PostIntegrationUUID()
	authorID := testutil.PostIntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, adminID, "Post Admin")
	testutil.SeedPostIntegrationIdentity(t, db, authorID, "Post Author")
	spiceDB := testutil.PostIntegrationSpiceDB(t)
	testutil.GrantPostIntegrationRole(t, spiceDB, adminID, policyv1.Role.Admin())
	testutil.GrantPostIntegrationRole(t, spiceDB, authorID, policyv1.Role.Author())
	adminMemberID := testutil.PostIntegrationMemberID(adminID)
	authorMemberID := testutil.PostIntegrationMemberID(authorID)

	postSvc := postintegration.NewPostDomainService(
		t, db, "https://cdn.example.com", spiceDB,
		testutil.NewPostIdentityManager(
			testutil.PostIntegrationIdentity(adminID, "en"),
			testutil.PostIntegrationIdentity(authorID, "en"),
		),
		testutil.NewPostContentBlockStore(t),
	)

	adminCtx := testutil.PostIntegrationContext(adminID)
	authorCtx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(authorID),
		MemberID:      auth.MemberID(authorMemberID),
		SessionID:     auth.SessionID(testutil.PostIntegrationUUID()),
		Authenticated: true,
	})
	suffix := strings.ReplaceAll(testutil.PostIntegrationUUID(), "-", "")
	categoryID := testutil.SeedPostIntegrationCategory(t, db, "Post Category "+suffix, "pc-"+suffix)
	tagID := testutil.SeedPostIntegrationTag(t, db, "Post Tag "+suffix, "pt-"+suffix)
	slug := "post-" + suffix

	created, err := postSvc.CreatePost(adminCtx, connect.NewRequest(&managev1.CreatePostRequest{
		Title:           "Post Participant Workflow " + suffix,
		Slug:            &slug,
		Summary:         stringPointer("Post participant workflow summary"),
		CommentsEnabled: true,
		CategoryIds:     []string{categoryID},
		TagIds:          []string{tagID},
		Document:        testutil.EmptyPostDocument("en"),
	}))
	require.NoError(t, err)
	testutil.RequirePostMemberRelation(t, db, "post_author", created.Msg.Id, adminMemberID, true)

	adminList, err := postSvc.ListPostsAdmin(adminCtx, connect.NewRequest(&managev1.ListPostsAdminRequest{
		Pagination: &commonv1.PaginationRequest{Limit: 10},
		Filters: []*commonv1.FilterSpec{
			{Field: "category_id", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: categoryID},
			{Field: "tag_id", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: tagID},
			{Field: "author_id", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: adminMemberID},
		},
	}))
	require.NoError(t, err)
	requirePostWithStatsIDs(t, adminList.Msg.Posts, created.Msg.Id)

	initial, err := postSvc.ListPostParticipants(adminCtx, connect.NewRequest(&managev1.ListPostParticipantsRequest{PostId: created.Msg.Id}))
	require.NoError(t, err)
	requirePostParticipant(t, initial.Msg.Participants, adminMemberID, managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_AUTHOR, true)

	added, err := postSvc.AddPostAuthor(adminCtx, connect.NewRequest(&managev1.AddPostAuthorRequest{
		PostId: created.Msg.Id, MemberId: authorMemberID,
	}))
	require.NoError(t, err)
	require.Equal(t, authorMemberID, added.Msg.GetMember().GetId())
	require.Equal(t, managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_AUTHOR, added.Msg.GetRole())
	testutil.RequirePostMemberRelation(t, db, "post_author", created.Msg.Id, authorMemberID, true)
	testutil.RequirePostAuthorization(t, spiceDB, created.Msg.Id, authorID, policyv1.Post.Edit, true)
	testutil.RequirePostAuthorization(t, spiceDB, created.Msg.Id, authorID, policyv1.Post.Manage, true)

	idempotentAuthor, err := postSvc.AddPostAuthor(adminCtx, connect.NewRequest(&managev1.AddPostAuthorRequest{
		PostId: created.Msg.Id, MemberId: authorMemberID,
	}))
	require.NoError(t, err)
	require.Equal(t, authorMemberID, idempotentAuthor.Msg.GetMember().GetId())
	testutil.RequirePostMemberRelation(t, db, "post_author", created.Msg.Id, authorMemberID, true)
	testutil.RequirePostMemberRelation(t, db, "post_collaborator", created.Msg.Id, authorMemberID, false)

	myPosts, err := postSvc.ListMyPosts(authorCtx, connect.NewRequest(&managev1.ListMyPostsRequest{
		Pagination: &commonv1.PaginationRequest{Limit: 10},
	}))
	require.NoError(t, err)
	requirePostIDs(t, myPosts.Msg.Posts, created.Msg.Id)

	discovered, err := postSvc.ListAIDocuments(authorCtx, postdomain.AIDocumentListInput{
		Query: "Participant Workflow", Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), discovered.Total)
	require.Len(t, discovered.Items, 1)
	require.Equal(t, created.Msg.Id, discovered.Items[0].ID)
	require.Equal(t, "Post Participant Workflow "+suffix, discovered.Items[0].Title)
	require.Equal(t, slug, *discovered.Items[0].Slug)
	require.Equal(t, "en", discovered.Items[0].SourceLocale)

	literalWildcard, err := postSvc.ListAIDocuments(authorCtx, postdomain.AIDocumentListInput{
		Query: "%" + suffix, Limit: 10,
	})
	require.NoError(t, err)
	require.Zero(t, literalWildcard.Total)
	require.Empty(t, literalWildcard.Items)

	// A Post Author cannot reassign another Author. Only site Admin can perform
	// the atomic peer-role replacement.
	_, err = postSvc.RemovePostAuthor(authorCtx, connect.NewRequest(&managev1.RemovePostAuthorRequest{
		PostId: created.Msg.Id, MemberId: adminMemberID,
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	testutil.RequirePostMemberRelation(t, db, "post_author", created.Msg.Id, adminMemberID, true)

	_, err = postSvc.AddPostCollaborator(authorCtx, connect.NewRequest(&managev1.AddPostCollaboratorRequest{
		PostId: created.Msg.Id, MemberId: adminMemberID,
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	reassigned, err := postSvc.AddPostCollaborator(adminCtx, connect.NewRequest(&managev1.AddPostCollaboratorRequest{
		PostId: created.Msg.Id, MemberId: authorMemberID,
	}))
	require.NoError(t, err)
	require.Equal(t, managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_COLLABORATOR, reassigned.Msg.GetRole())
	testutil.RequirePostMemberRelation(t, db, "post_author", created.Msg.Id, authorMemberID, false)
	testutil.RequirePostMemberRelation(t, db, "post_collaborator", created.Msg.Id, authorMemberID, true)
	testutil.RequirePostAuthorization(t, spiceDB, created.Msg.Id, authorID, policyv1.Post.Edit, true)
	testutil.RequirePostAuthorization(t, spiceDB, created.Msg.Id, authorID, policyv1.Post.Manage, false)

	// Reassigning the last durable Author is rejected by the Post transaction
	// and rolls back, preserving the Author relation.
	_, err = postSvc.AddPostCollaborator(adminCtx, connect.NewRequest(&managev1.AddPostCollaboratorRequest{
		PostId: created.Msg.Id, MemberId: adminMemberID,
	}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	testutil.RequirePostMemberRelation(t, db, "post_author", created.Msg.Id, adminMemberID, true)
	testutil.RequirePostMemberRelation(t, db, "post_collaborator", created.Msg.Id, adminMemberID, false)

	removed, err := postSvc.RemovePostCollaborator(adminCtx, connect.NewRequest(&managev1.RemovePostCollaboratorRequest{
		PostId: created.Msg.Id, MemberId: authorMemberID,
	}))
	require.NoError(t, err)
	require.True(t, removed.Msg.Success)
	testutil.RequirePostMemberRelation(t, db, "post_collaborator", created.Msg.Id, authorMemberID, false)
	testutil.RequirePostAuthorization(t, spiceDB, created.Msg.Id, authorID, policyv1.Post.Edit, false)

	emptyMine, err := postSvc.ListMyPosts(authorCtx, connect.NewRequest(&managev1.ListMyPostsRequest{}))
	require.NoError(t, err)
	require.Empty(t, emptyMine.Msg.Posts)
}

func stringPointer(value string) *string { return &value }

func requirePostWithStatsIDs(t *testing.T, posts []*managev1.PostWithStats, want ...string) {
	t.Helper()
	got := make([]string, 0, len(posts))
	for _, post := range posts {
		if post.GetPost() != nil {
			got = append(got, post.GetPost().GetId())
		}
	}
	require.ElementsMatch(t, want, got)
}

func requirePostIDs(t *testing.T, posts []*managev1.Post, want ...string) {
	t.Helper()
	got := make([]string, 0, len(posts))
	for _, post := range posts {
		got = append(got, post.GetId())
	}
	require.ElementsMatch(t, want, got)
}

func requirePostParticipant(
	t *testing.T,
	participants []*managev1.PostParticipant,
	memberID string,
	role managev1.PostParticipantRole,
	effective bool,
) {
	t.Helper()
	for _, participant := range participants {
		if participant.GetMember().GetId() == memberID {
			require.Equal(t, role, participant.GetRole())
			require.Equal(t, effective, participant.GetHasEffectiveAuthority())
			return
		}
	}
	t.Fatalf("Post participant %s was not found", memberID)
}
