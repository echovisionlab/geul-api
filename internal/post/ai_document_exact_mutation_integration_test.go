//go:build integration

package post_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"

	"connectrpc.com/connect"
	authzedv1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	"google.golang.org/grpc"

	"github.com/echovisionlab/geul-api/internal/auth"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/postintegration"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
)

func TestPostAIDocumentExactMutationAuthorizesLockedLifecycleOnceIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewPostIntegrationDB(t)
	identityID := testutil.PostIntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, identityID, "AI document exact mutation")
	realSpiceDB := testutil.PostIntegrationSpiceDB(t)
	testutil.GrantPostIntegrationRole(t, realSpiceDB, identityID, policyv1.Role.Admin())
	ctx := testutil.PostIntegrationContext(identityID)
	store := testutil.NewPostContentBlockStore(t)
	creator := postintegration.NewPostDomainService(
		t, db, "", realSpiceDB,
		store,
	)
	slug := "ai-document-exact-mutation-" + testutil.PostIntegrationUUID()
	created, err := creator.CreatePost(ctx, connect.NewRequest(&managev1.CreatePostRequest{
		Title: "AI document exact mutation", Slug: &slug, Document: testutil.EmptyPostDocument("en"),
	}))
	require.NoError(t, err)

	assertAuthorizedCompiler := func(wantAction func(string) (policyv1.Can, error)) {
		t.Helper()
		want, err := wantAction(created.Msg.Id)
		require.NoError(t, err)
		spiceDB, recorder := newCountingPostPermissionServer(t, true)
		service := postintegration.NewPostDomainService(
			t, db, "", spiceDB,
			store,
		)
		compilerFailure := errors.New("stop after authorized compiler")
		compilerCalls := 0
		_, err = service.Service.ExecuteAIDocumentMutation(
			ctx,
			created.Msg.Id,
			"en",
			postdomain.AIDocumentExecutionValidate,
			func(state postdomain.AIDocumentState) (postdomain.AIDocumentMutation, error) {
				compilerCalls++
				require.Equal(t, created.Msg.Id, state.PostID)
				return postdomain.AIDocumentMutation{}, compilerFailure
			},
		)
		require.ErrorIs(t, err, compilerFailure)
		require.Equal(t, 1, compilerCalls)
		requests := recorder.snapshot()
		require.Len(t, requests, 1, "one logical mutation must make one exact authorization decision")
		require.Equal(t, want.Action().Permission(), requests[0].GetPermission())
		require.Equal(t, want.Resource().Type(), requests[0].GetResource().GetObjectType())
		require.Equal(t, want.Resource().ID(), requests[0].GetResource().GetObjectId())
	}

	assertAuthorizedCompiler(policyv1.Post.Edit)
	_, err = creator.PublishPost(ctx, connect.NewRequest(&managev1.PublishPostRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	_, err = creator.ArchivePost(ctx, connect.NewRequest(&managev1.ArchivePostRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	assertAuthorizedCompiler(policyv1.Post.EditArchived)

	denyingSpiceDB, recorder := newCountingPostPermissionServer(t, false)
	deniedService := postintegration.NewPostDomainService(
		t, db, "", denyingSpiceDB,
		store,
	)
	compilerCalls := 0
	_, err = deniedService.Service.ExecuteAIDocumentMutation(
		ctx,
		created.Msg.Id,
		"en",
		postdomain.AIDocumentExecutionApply,
		func(postdomain.AIDocumentState) (postdomain.AIDocumentMutation, error) {
			compilerCalls++
			return postdomain.AIDocumentMutation{}, nil
		},
	)
	require.Equal(t, connect.CodeNotFound, errs.CodeOf(err))
	require.Zero(t, compilerCalls, "authorization denial must precede document projection and compilation")
	requests := recorder.snapshot()
	require.Len(t, requests, 1)
	wantArchived, err := policyv1.Post.EditArchived(created.Msg.Id)
	require.NoError(t, err)
	require.Equal(t, wantArchived.Action().Permission(), requests[0].GetPermission())
}

type countingPostPermissionServer struct {
	authzedv1.UnimplementedPermissionsServiceServer

	mu       sync.Mutex
	allowed  bool
	requests []*authzedv1.CheckPermissionRequest
}

func (s *countingPostPermissionServer) CheckPermission(
	_ context.Context,
	request *authzedv1.CheckPermissionRequest,
) (*authzedv1.CheckPermissionResponse, error) {
	s.mu.Lock()
	s.requests = append(s.requests, request)
	s.mu.Unlock()
	permissionship := authzedv1.CheckPermissionResponse_PERMISSIONSHIP_NO_PERMISSION
	if s.allowed {
		permissionship = authzedv1.CheckPermissionResponse_PERMISSIONSHIP_HAS_PERMISSION
	}
	return &authzedv1.CheckPermissionResponse{Permissionship: permissionship}, nil
}

func (s *countingPostPermissionServer) snapshot() []*authzedv1.CheckPermissionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*authzedv1.CheckPermissionRequest(nil), s.requests...)
}

func newCountingPostPermissionServer(
	t *testing.T,
	allowed bool,
) (*auth.SpiceDBClient, *countingPostPermissionServer) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer()
	recorder := &countingPostPermissionServer{allowed: allowed}
	authzedv1.RegisterPermissionsServiceServer(server, recorder)
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	client, err := auth.NewSpiceDBClient(listener.Addr().String(), "integration-test-token", true)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, client.Close())
		server.GracefulStop()
		if closeErr := listener.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			require.NoError(t, closeErr)
		}
		serveErr := <-serveErrors
		if !errors.Is(serveErr, grpc.ErrServerStopped) {
			require.NoError(t, serveErr)
		}
	})
	return client, recorder
}

// An AI edit is a Member's locale content change like a collaboration save,
// so it leaves the same audit record; a validation dry run leaves none.
func TestPostAIDocumentEditIsAuditedIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewPostIntegrationDB(t)
	identityID := testutil.PostIntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, identityID, "Post AI audit")
	spiceDB := testutil.PostIntegrationSpiceDB(t)
	testutil.GrantPostIntegrationRole(t, spiceDB, identityID, policyv1.Role.Admin())
	ctx := testutil.PostIntegrationContext(identityID)
	posts := postintegration.NewPostDomainService(t, db, "", spiceDB, testutil.NewPostContentBlockStore(t))
	created, err := posts.CreatePost(ctx, connect.NewRequest(&managev1.CreatePostRequest{
		Title: "Post AI audit", Document: testutil.EmptyPostDocument("en"),
	}))
	require.NoError(t, err)
	contributor := testutil.PostIntegrationMemberID(identityID)
	retitle := func(mode postdomain.AIDocumentExecutionMode, title string) {
		t.Helper()
		_, err := posts.Service.ExecuteAIDocumentMutation(ctx, created.Msg.Id, "en", mode,
			func(state postdomain.AIDocumentState) (postdomain.AIDocumentMutation, error) {
				revision, err := uuid.Parse(state.DocumentRevision)
				if err != nil {
					return postdomain.AIDocumentMutation{}, err
				}
				documentID, err := uuid.Parse(state.ContentDocumentID)
				if err != nil {
					return postdomain.AIDocumentMutation{}, err
				}
				contributor := uuid.MustParse(state.ViewerMemberID)
				return postdomain.AIDocumentMutation{
					PostID: state.PostID, Locale: state.RequestedLocale,
					ObservedSourceLocale: state.SourceLocale, ObservedLocaleExists: state.LocaleExists,
					ExpectedRevision: revision, ContributorMemberID: contributor,
					Batch: contentblock.Batch{
						DocumentID: documentID, ExpectedRevision: revision, ContributorMemberIDs: []uuid.UUID{contributor},
					},
					Metadata: postdomain.AIDocumentMetadataPatch{SetTitle: true, Title: &title},
				}, nil
			})
		require.NoError(t, err)
	}
	records := func() int64 {
		t.Helper()
		var count int64
		require.NoError(t, db.Table("public.domain_audit").
			Where("target_type = 'post' AND target_id = ? AND action = ?", created.Msg.Id, "post.updated").
			Where("actor_member_id = ?::uuid AND attributes->'changed_fields' ? 'locale_content'", contributor).
			Count(&count).Error)
		return count
	}

	retitle(postdomain.AIDocumentExecutionValidate, "Validated AI title")
	require.Zero(t, records())
	retitle(postdomain.AIDocumentExecutionApply, "Audited AI title")
	require.EqualValues(t, 1, records())
	retitle(postdomain.AIDocumentExecutionApply, "Audited AI title")
	require.EqualValues(t, 1, records(), "an edit that changes nothing leaves no record")
}
