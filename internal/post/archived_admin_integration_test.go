//go:build integration

package post_test

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil/contenttest"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	postpostgres "github.com/echovisionlab/geul-api/internal/post/postgres"

	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/echovisionlab/geul-api/internal/testutil/postintegration"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestArchivedPostAllowsOnlyAdminMutationsAndAuthorReadOnlyEditorIntegration(t *testing.T) {
	t.Parallel()
	db := orytest.NewPostIntegrationDB(t)
	adminID := orytest.PostIntegrationUUID()
	authorID := orytest.PostIntegrationUUID()
	collaboratorID := orytest.PostIntegrationUUID()
	orytest.SeedPostIntegrationIdentity(t, db, adminID, "Archived Post Admin")
	orytest.SeedPostIntegrationIdentity(t, db, authorID, "Archived Post Author")
	orytest.SeedPostIntegrationIdentity(t, db, collaboratorID, "Archived Post Collaborator")
	spiceDB := orytest.PostIntegrationSpiceDB(t)
	orytest.GrantPostIntegrationRole(t, spiceDB, adminID, policyv1.Role.Admin())
	orytest.GrantPostIntegrationRole(t, spiceDB, authorID, policyv1.Role.Author())
	adminCtx := orytest.PostIntegrationContext(adminID)
	authorCtx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID: auth.IdentityID(authorID), MemberID: auth.MemberID(orytest.PostIntegrationMemberID(authorID)),
		SessionID: auth.SessionID(orytest.PostIntegrationUUID()), Authenticated: true,
	})
	store := contenttest.NewPostContentBlockStore(t)
	service := postintegration.NewPostDomainService(
		t, db, "", spiceDB,
		store,
	)

	slug := "archived-post-admin-edit-" + orytest.PostIntegrationUUID()
	created, err := service.CreatePost(adminCtx, connect.NewRequest(&managev1.CreatePostRequest{
		Title: "Archived Post Admin Edit", Slug: &slug,
	}))
	require.NoError(t, err)
	require.NotNil(t, created.Msg.Document)
	require.NotEmpty(t, created.Msg.Revision)
	require.NotEmpty(t, created.Msg.ConfigurationRevision)
	_, err = service.AddPostAuthor(adminCtx, connect.NewRequest(&managev1.AddPostAuthorRequest{
		PostId: created.Msg.Id, MemberId: orytest.PostIntegrationMemberID(authorID),
	}))
	require.NoError(t, err)
	_, err = service.PublishPost(adminCtx, connect.NewRequest(&managev1.PublishPostRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	archived, err := service.ArchivePost(adminCtx, connect.NewRequest(&managev1.ArchivePostRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, managev1.PostStatus_POST_STATUS_ARCHIVED, archived.Msg.Status)
	require.Contains(t, archived.Msg.AllowedActions, managev1.PostAction_POST_ACTION_EDIT)
	require.Contains(t, archived.Msg.AllowedActions, managev1.PostAction_POST_ACTION_RESTORE_VERSION)
	_, err = service.GetPost(context.Background(), connect.NewRequest(&managev1.GetPostRequest{Id: created.Msg.Id}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"public archived reads use open.v1 and cannot enter the manage editor projection")
	authorPost, err := service.GetPost(authorCtx, connect.NewRequest(&managev1.GetPostRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.NotNil(t, authorPost.Msg.Document, "an archived Post Author can render the canonical document read-only")
	require.NotContains(t, authorPost.Msg.AllowedActions, managev1.PostAction_POST_ACTION_EDIT)

	commentsEnabled := true
	_, err = service.UpdatePost(authorCtx, connect.NewRequest(&managev1.UpdatePostRequest{
		Id: created.Msg.Id, ExpectedConfigurationRevision: authorPost.Msg.ConfigurationRevision, CommentsEnabled: &commentsEnabled,
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	updated, err := service.UpdatePost(adminCtx, connect.NewRequest(&managev1.UpdatePostRequest{
		Id: created.Msg.Id, ExpectedConfigurationRevision: authorPost.Msg.ConfigurationRevision, CommentsEnabled: &commentsEnabled,
	}))
	require.NoError(t, err)
	require.True(t, updated.Msg.CommentsEnabled)
	require.NotEqual(t, authorPost.Msg.ConfigurationRevision, updated.Msg.ConfigurationRevision)

	_, err = service.AddPostCollaborator(authorCtx, connect.NewRequest(&managev1.AddPostCollaboratorRequest{
		PostId: created.Msg.Id, MemberId: orytest.PostIntegrationMemberID(collaboratorID),
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	_, err = service.AddPostCollaborator(adminCtx, connect.NewRequest(&managev1.AddPostCollaboratorRequest{
		PostId: created.Msg.Id, MemberId: orytest.PostIntegrationMemberID(collaboratorID),
	}))
	require.NoError(t, err)
	collaboratorCtx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID: auth.IdentityID(collaboratorID), MemberID: auth.MemberID(orytest.PostIntegrationMemberID(collaboratorID)),
		SessionID: auth.SessionID(orytest.PostIntegrationUUID()), Authenticated: true,
	})
	collaboratorEditorList, err := service.ListMyPosts(
		collaboratorCtx,
		connect.NewRequest(&managev1.ListMyPostsRequest{}),
	)
	require.NoError(t, err)
	require.Empty(t, collaboratorEditorList.Msg.Posts, "an archived Post must not leak through ordinary collaborator view authority")

	internal := postintegration.NewInternalPostDomainService(t, db, "", spiceDB, store)
	authorSessionID := orytest.InsertPostIntegrationSession(t, db, authorID)
	authorBootstrap, err := internal.LoadPostBlockDocument(context.Background(), connect.NewRequest(&intrav1.LoadPostBlockDocumentRequest{
		PostId: created.Msg.Id, Locale: "en",
		Principal: &intrav1.CollaborationPrincipal{SessionId: authorSessionID},
	}))
	require.NoError(t, err, "archived Post Authors enter Collab as read-only viewers")
	require.NotNil(t, authorBootstrap.Msg.Document)
	adminSessionID := orytest.InsertPostIntegrationSession(t, db, adminID)
	adminBootstrap, err := internal.LoadPostBlockDocument(context.Background(), connect.NewRequest(&intrav1.LoadPostBlockDocumentRequest{
		PostId: created.Msg.Id, Locale: "en",
		Principal: &intrav1.CollaborationPrincipal{SessionId: adminSessionID},
	}))
	require.NoError(t, err)
	require.NotNil(t, adminBootstrap.Msg.Document)
	for _, actor := range []struct {
		ctx      context.Context
		session  string
		editable bool
	}{
		{authorCtx, authorSessionID, false}, {adminCtx, adminSessionID, true},
	} {
		user := *auth.GetUser(actor.ctx)
		user.SessionID = auth.SessionID(actor.session)
		firstPaint, readErr := service.GetPostEditorDocument(auth.WithUser(actor.ctx, &user), connect.NewRequest(&managev1.GetPostEditorDocumentRequest{PostId: created.Msg.Id, Locale: "en"}))
		require.NoError(t, readErr)
		require.Equal(t, actor.editable, firstPaint.Msg.Editable)
		require.Equal(t, authorBootstrap.Msg.DocumentRevision, firstPaint.Msg.DocumentRevision)
	}
	collaboratorSession := orytest.InsertPostIntegrationSession(t, db, collaboratorID)
	collaborator := *auth.GetUser(collaboratorCtx)
	collaborator.SessionID = auth.SessionID(collaboratorSession)
	_, deniedErr := service.GetPostEditorDocument(auth.WithUser(collaboratorCtx, &collaborator), connect.NewRequest(&managev1.GetPostEditorDocumentRequest{PostId: created.Msg.Id, Locale: "en"}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(deniedErr))

	authorBatch := archivedPostParagraphMutationBatch(authorPost.Msg.Revision, orytest.PostIntegrationMemberID(authorID))
	_, err = internal.ApplyPostBlockBatch(context.Background(), connect.NewRequest(&intrav1.ApplyPostBlockBatchRequest{
		PostId: created.Msg.Id, Batch: authorBatch, Locale: "en",
	}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	adminBatch := archivedPostParagraphMutationBatch(authorPost.Msg.Revision, orytest.PostIntegrationMemberID(adminID))
	accepted, err := internal.ApplyPostBlockBatch(context.Background(), connect.NewRequest(&intrav1.ApplyPostBlockBatchRequest{
		PostId: created.Msg.Id, Batch: adminBatch, Locale: "en",
	}))
	require.NoError(t, err)
	require.True(t, accepted.Msg.Changed)
	var sourceBlockID string
	require.NoError(t, db.Raw(`
		SELECT block.id
		FROM content_block AS block
		JOIN post AS root ON root.content_document_id = block.document_id
		WHERE root.id = ?
		ORDER BY block.id
		LIMIT 1`, created.Msg.Id).Scan(&sourceBlockID).Error)
	require.NotEmpty(t, sourceBlockID)
	// Translation target overlays are durable projections on the same aggregate.
	// A source collaboration bootstrap must not expose them to the room codec.
	require.NoError(t, db.Exec(`
		INSERT INTO content_block_locale (block_id, locale, localized_data)
		SELECT block_id, 'ko', localized_data
		FROM content_block_locale
		WHERE block_id = ? AND locale = 'en'
		ON CONFLICT (block_id, locale) DO NOTHING`, sourceBlockID).Error)
	postWithTargetOverlay, err := internal.LoadPostBlockDocument(context.Background(), connect.NewRequest(&intrav1.LoadPostBlockDocumentRequest{
		PostId: created.Msg.Id, Locale: "en",
		Principal: &intrav1.CollaborationPrincipal{SessionId: adminSessionID},
	}))
	require.NoError(t, err)
	require.Equal(t, "en", postWithTargetOverlay.Msg.Document.Locale)
	require.Equal(t, "en", postWithTargetOverlay.Msg.Document.LocaleOverlay.Locale)

	err = db.WithContext(authorCtx).Transaction(func(tx *gorm.DB) error {
		return postpostgres.RequireLockedSourceLocaleEdit(authorCtx, tx, spiceDB, created.Msg.Id)
	})
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.NoError(t, db.WithContext(adminCtx).Transaction(func(tx *gorm.DB) error {
		return postpostgres.RequireLockedSourceLocaleEdit(adminCtx, tx, spiceDB, created.Msg.Id)
	}))
}

func archivedPostParagraphMutationBatch(
	expectedRevision string,
	contributorMemberID string,
) *contentv1.RichTextBlockMutationBatch {
	blockID := orytest.PostIntegrationUUID()
	return &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		ExpectedRevision:        expectedRevision,
		ContributorMemberIds:    []string{contributorMemberID},
		BaseMutations: []*contentv1.RichTextBlockMutation{{
			Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{
				Node: &contentv1.RichTextBlockNode{
					Block: &contentv1.RichTextBlock{
						Id: blockID,
						Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{
							Props: &contentv1.ParagraphProps{},
						}},
					},
					Placement: &contentv1.ContentBlockPlacement{Index: 0},
				},
			}},
		}},
		LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
			Locale: "en",
			Mutations: []*contentv1.RichTextBlockLocaleMutation{{
				Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{
					Block: &contentv1.RichTextBlockLocale{
						BlockId: blockID,
						Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
							Props: &contentv1.ParagraphLocaleProps{},
							Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
								Text: &contentv1.RichTextStyledText{Text: "Archived Post Admin edit"},
							}}},
						}},
					},
				}},
			}},
		}},
	}
}
