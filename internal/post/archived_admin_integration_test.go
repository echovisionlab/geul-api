//go:build integration

package post_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/postintegration"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestArchivedPostAllowsOnlyAdminMutationsAndAuthorReadOnlyEditorIntegration(t *testing.T) {
	db := testutil.NewPostIntegrationDB(t)
	adminID := testutil.PostIntegrationUUID()
	authorID := testutil.PostIntegrationUUID()
	collaboratorID := testutil.PostIntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, adminID, "Archived Post Admin")
	testutil.SeedPostIntegrationIdentity(t, db, authorID, "Archived Post Author")
	testutil.SeedPostIntegrationIdentity(t, db, collaboratorID, "Archived Post Collaborator")
	spiceDB := testutil.PostIntegrationSpiceDB(t)
	testutil.GrantPostIntegrationRole(t, spiceDB, adminID, policyv1.Role.Admin())
	testutil.GrantPostIntegrationRole(t, spiceDB, authorID, policyv1.Role.Author())
	adminCtx := testutil.PostIntegrationContext(adminID)
	authorCtx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID: auth.IdentityID(authorID), MemberID: auth.MemberID(testutil.PostIntegrationMemberID(authorID)),
		SessionID: auth.SessionID(testutil.PostIntegrationUUID()), Authenticated: true,
	})
	store := testutil.NewPostContentBlockStore(t)
	service := postintegration.NewPostDomainService(
		t, db, "", spiceDB,
		testutil.NewPostIdentityManager(testutil.PostIntegrationIdentity(adminID, "en")),
		store,
	)

	slug := "archived-post-admin-edit-" + testutil.PostIntegrationUUID()
	created, err := service.CreatePost(adminCtx, connect.NewRequest(&managev1.CreatePostRequest{
		Title: "Archived Post Admin Edit", Slug: &slug,
	}))
	require.NoError(t, err)
	require.NotNil(t, created.Msg.Document)
	require.NotEmpty(t, created.Msg.Revision)
	_, err = service.AddPostAuthor(adminCtx, connect.NewRequest(&managev1.AddPostAuthorRequest{
		PostId: created.Msg.Id, MemberId: testutil.PostIntegrationMemberID(authorID),
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

	commentsEnabled := false
	_, err = service.UpdatePost(authorCtx, connect.NewRequest(&managev1.UpdatePostRequest{
		Id: created.Msg.Id, CommentsEnabled: &commentsEnabled,
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	updated, err := service.UpdatePost(adminCtx, connect.NewRequest(&managev1.UpdatePostRequest{
		Id: created.Msg.Id, CommentsEnabled: &commentsEnabled,
	}))
	require.NoError(t, err)
	require.False(t, updated.Msg.CommentsEnabled)

	_, err = service.AddPostCollaborator(authorCtx, connect.NewRequest(&managev1.AddPostCollaboratorRequest{
		PostId: created.Msg.Id, MemberId: testutil.PostIntegrationMemberID(collaboratorID),
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	_, err = service.AddPostCollaborator(adminCtx, connect.NewRequest(&managev1.AddPostCollaboratorRequest{
		PostId: created.Msg.Id, MemberId: testutil.PostIntegrationMemberID(collaboratorID),
	}))
	require.NoError(t, err)
	collaboratorCtx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID: auth.IdentityID(collaboratorID), MemberID: auth.MemberID(testutil.PostIntegrationMemberID(collaboratorID)),
		SessionID: auth.SessionID(testutil.PostIntegrationUUID()), Authenticated: true,
	})
	collaboratorEditorList, err := service.ListMyPosts(
		collaboratorCtx,
		connect.NewRequest(&managev1.ListMyPostsRequest{}),
	)
	require.NoError(t, err)
	require.Empty(t, collaboratorEditorList.Msg.Posts, "an archived Post must not leak through ordinary collaborator view authority")

	internal := postintegration.NewInternalPostDomainService(t, db, "", spiceDB, store)
	authorSessionID := testutil.InsertPostIntegrationSession(t, db, authorID)
	authorBootstrap, err := internal.LoadPostBlockDocument(context.Background(), connect.NewRequest(&intrav1.LoadPostBlockDocumentRequest{
		PostId: created.Msg.Id, Locale: "en",
		Principal: &intrav1.CollaborationPrincipal{SessionId: authorSessionID},
	}))
	require.NoError(t, err, "archived Post Authors enter Collab as read-only viewers")
	require.NotNil(t, authorBootstrap.Msg.Document)
	adminSessionID := testutil.InsertPostIntegrationSession(t, db, adminID)
	adminBootstrap, err := internal.LoadPostBlockDocument(context.Background(), connect.NewRequest(&intrav1.LoadPostBlockDocumentRequest{
		PostId: created.Msg.Id, Locale: "en",
		Principal: &intrav1.CollaborationPrincipal{SessionId: adminSessionID},
	}))
	require.NoError(t, err)
	require.NotNil(t, adminBootstrap.Msg.Document)

	authorBatch := archivedPostParagraphMutationBatch(authorPost.Msg.Revision, testutil.PostIntegrationMemberID(authorID))
	_, err = internal.ApplyPostBlockBatch(context.Background(), connect.NewRequest(&intrav1.ApplyPostBlockBatchRequest{
		PostId: created.Msg.Id, Batch: authorBatch, Locale: "en",
	}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	adminBatch := archivedPostParagraphMutationBatch(authorPost.Msg.Revision, testutil.PostIntegrationMemberID(adminID))
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
		return postdomain.RequireLockedSourceLocaleEdit(authorCtx, tx, spiceDB, created.Msg.Id)
	})
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.NoError(t, db.WithContext(adminCtx).Transaction(func(tx *gorm.DB) error {
		return postdomain.RequireLockedSourceLocaleEdit(adminCtx, tx, spiceDB, created.Msg.Id)
	}))
}

func archivedPostParagraphMutationBatch(
	expectedRevision string,
	contributorMemberID string,
) *contentv1.RichTextBlockMutationBatch {
	blockID := testutil.PostIntegrationUUID()
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
