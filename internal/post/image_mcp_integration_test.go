//go:build integration

package post_test

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	aidocumentadapter "github.com/echovisionlab/geul-api/internal/adapters/aidocument"
	mcpadapter "github.com/echovisionlab/geul-api/internal/adapters/mcp"
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/postintegration"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
)

func TestPostImageMCPFileCaptionLifecycleIntegration(t *testing.T) {
	db := testutil.NewPostIntegrationDB(t)
	identityID := testutil.PostIntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, identityID, "Image File Block MCP")
	spiceDB := testutil.PostIntegrationSpiceDB(t)
	testutil.GrantPostIntegrationRole(t, spiceDB, identityID, policyv1.Role.Admin())
	ctx := testutil.PostIntegrationContext(identityID)
	store, err := contentblock.NewGeneratedStore(filemedia.NewContentBlockFileReuseAuthorizer(spiceDB))
	require.NoError(t, err)
	posts := postintegration.NewPostDomainService(t, db, "", spiceDB, testutil.NewPostIdentityManager(testutil.PostIntegrationIdentity(identityID, "en")), store)
	slug := "image-mcp-" + testutil.PostIntegrationUUID()
	created, err := posts.CreatePost(ctx, connect.NewRequest(&managev1.CreatePostRequest{Title: "Image File Block MCP", Slug: &slug, Document: testutil.EmptyPostDocument("en")}))
	require.NoError(t, err)
	first, second := testutil.PostIntegrationUUID(), testutil.PostIntegrationUUID()
	seedManagePostFeaturedFile(t, db, first)
	seedManagePostFeaturedFile(t, db, second)
	registration, err := aidocumentadapter.NewPostRegistration(posts)
	require.NoError(t, err)
	application, err := core.NewService(registration.Port)
	require.NoError(t, err)
	tools, err := mcpadapter.NewFileBlockTools(application, &filemedia.FileService{})
	require.NoError(t, err)
	arguments := func(locale string, extra map[string]any) mcpserver.ToolArguments {
		t.Helper()
		state, err := posts.LoadAIDocumentState(ctx, created.Msg.Id, locale)
		require.NoError(t, err)
		values := map[string]any{"document_type": "post", "document_id": created.Msg.Id, "locale": locale, "expected_document_revision": state.DocumentRevision}
		if state.TargetRevision != nil {
			values["expected_target_revision"] = *state.TargetRevision
		}
		for key, value := range extra {
			values[key] = value
		}
		encoded, err := json.Marshal(values)
		require.NoError(t, err)
		var result mcpserver.ToolArguments
		require.NoError(t, json.Unmarshal(encoded, &result))
		return result
	}
	call := func(name string, args mcpserver.ToolArguments) mcpserver.ToolResult {
		t.Helper()
		result, err := tools.CallTool(ctx, mcpserver.Principal{}, name, args)
		require.NoError(t, err)
		require.False(t, result.IsError)
		return result
	}
	addArguments := arguments("en", map[string]any{"file_id": first, "caption": "source image caption"})
	added := call(mcpadapter.ToolDocumentFileAdd, addArguments)
	blockID := added.StructuredContent["block_id"].(string)
	source, err := posts.LoadAIDocumentState(ctx, created.Msg.Id, "en")
	require.NoError(t, err)
	require.Equal(t, first, source.LocalizedDocument.Base.Nodes[0].Block.GetFile().Props.Attachment.GetActiveFileId())
	require.Equal(t, "source image caption", source.LocalizedDocument.LocaleOverlay.Blocks[0].GetFile().Props.GetCaption())
	stale := call(mcpadapter.ToolDocumentFileAdd, addArguments)
	require.Equal(t, "sync_required", stale.StructuredContent["status"])
	var count int64
	require.NoError(t, db.Table("content_block").Where("document_id = ?::uuid", source.ContentDocumentID).Count(&count).Error)
	require.EqualValues(t, 1, count)

	executePostTargetLifecycle(t, posts, ctx, created.Msg.Id, "ko", false)
	call(mcpadapter.ToolDocumentFileCaptionUpdate, arguments("ko", map[string]any{"block_id": blockID, "caption": "번역 이미지"}))
	targetBeforeClear, err := posts.LoadAIDocumentState(ctx, created.Msg.Id, "ko")
	require.NoError(t, err)
	call(mcpadapter.ToolDocumentFileCaptionUpdate, arguments("ko", map[string]any{"block_id": blockID, "caption": ""}))
	target, err := posts.LoadAIDocumentState(ctx, created.Msg.Id, "ko")
	require.NoError(t, err)
	require.Equal(t, source.DocumentRevision, target.DocumentRevision)
	require.NotEqual(t, *targetBeforeClear.TargetRevision, *target.TargetRevision)
	require.NotNil(t, target.LocalizedDocument.LocaleOverlay.Blocks[0].GetFile().Props.Caption)
	require.Empty(t, *target.LocalizedDocument.LocaleOverlay.Blocks[0].GetFile().Props.Caption)
	rejected, err := tools.CallTool(ctx, mcpserver.Principal{}, mcpadapter.ToolDocumentFileReplace, arguments("ko", map[string]any{"block_id": blockID, "file_id": second}))
	require.NoError(t, err)
	require.True(t, rejected.IsError, "target locale cannot replace shared attachment")
	call(mcpadapter.ToolDocumentFileReplace, arguments("en", map[string]any{"block_id": blockID, "file_id": second}))
	source, err = posts.LoadAIDocumentState(ctx, created.Msg.Id, "en")
	require.NoError(t, err)
	require.Equal(t, second, source.LocalizedDocument.Base.Nodes[0].Block.GetFile().Props.Attachment.GetActiveFileId())
	require.Equal(t, "source image caption", source.LocalizedDocument.LocaleOverlay.Blocks[0].GetFile().Props.GetCaption())
	target, err = posts.LoadAIDocumentState(ctx, created.Msg.Id, "ko")
	require.NoError(t, err)
	require.NotNil(t, target.LocalizedDocument.LocaleOverlay.Blocks[0].GetFile().Props.Caption)
	require.Empty(t, *target.LocalizedDocument.LocaleOverlay.Blocks[0].GetFile().Props.Caption)
	_, err = tools.CallTool(context.Background(), mcpserver.Principal{}, mcpadapter.ToolDocumentFileCaptionUpdate, arguments("en", map[string]any{"block_id": blockID, "caption": "unauthorized"}))
	require.Error(t, err)
	call(mcpadapter.ToolDocumentFileRemove, arguments("en", map[string]any{"block_id": blockID}))
	source, err = posts.LoadAIDocumentState(ctx, created.Msg.Id, "en")
	require.NoError(t, err)
	require.Empty(t, source.LocalizedDocument.Base.Nodes)
	require.NoError(t, db.Table("content_block_attachment").Where("block_id = ?::uuid", blockID).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, db.Table("file").Where("id IN ? AND delete_requested_at IS NULL", []string{first, second}).Count(&count).Error)
	require.EqualValues(t, 2, count, "placement deletion must retain reusable File records")
}
