//go:build integration

package post_test

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/postintegration"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPostTargetLocaleCollaborationCASAndSourceSeedIntegration(t *testing.T) {
	db := testutil.NewPostIntegrationDB(t)
	adminIdentityID := testutil.PostIntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, adminIdentityID, "Post target locale admin")
	spiceDB := testutil.PostIntegrationSpiceDB(t)
	testutil.GrantPostIntegrationRole(t, spiceDB, adminIdentityID, policyv1.Role.Admin())
	ctx := testutil.PostIntegrationContext(adminIdentityID)
	memberID := testutil.PostIntegrationMemberID(adminIdentityID)
	store := testutil.NewPostContentBlockStore(t)
	service := postintegration.NewPostDomainService(
		t, db, "", spiceDB,
		testutil.NewPostIdentityManager(testutil.PostIntegrationIdentity(adminIdentityID, "en")),
		store,
	)
	internal := postintegration.NewInternalPostDomainService(t, db, "", spiceDB, store)

	slug := "post-target-locale-" + testutil.PostIntegrationUUID()
	created, err := service.CreatePost(ctx, connect.NewRequest(&managev1.CreatePostRequest{
		Title: "Post target locale", Slug: &slug, Document: testutil.EmptyPostDocument("en"),
	}))
	require.NoError(t, err)
	blockIDs := []string{testutil.PostIntegrationUUID(), testutil.PostIntegrationUUID(), testutil.PostIntegrationUUID()}
	sourceWrite, err := internal.ApplyPostBlockBatch(t.Context(), connect.NewRequest(&intrav1.ApplyPostBlockBatchRequest{
		PostId: created.Msg.Id, Locale: "en",
		Batch:                postParagraphBatch(created.Msg.Revision, memberID, "en", blockIDs, []string{"first", "", "third"}, true),
		AffectedLocaleValues: postParagraphContentTargets(blockIDs...),
	}))
	require.NoError(t, err)
	require.True(t, sourceWrite.Msg.Changed)
	require.NotEqual(t, created.Msg.Revision, sourceWrite.Msg.DocumentRevision)

	adminSessionID := testutil.InsertPostIntegrationSession(t, db, adminIdentityID)
	sourceLoaded, err := internal.LoadPostBlockDocument(t.Context(), connect.NewRequest(&intrav1.LoadPostBlockDocumentRequest{
		PostId: created.Msg.Id, Locale: "en",
		Principal: &intrav1.CollaborationPrincipal{SessionId: adminSessionID},
	}))
	require.NoError(t, err)
	requirePostParagraphContentTargets(t, sourceLoaded.Msg.PresentLocaleValues, blockIDs...)
	requirePostExplicitEmptyParagraphStorage(t, db, created.Msg.Id, "en", blockIDs[1])

	noOpEmptyWrite, err := internal.ApplyPostBlockBatch(t.Context(), connect.NewRequest(&intrav1.ApplyPostBlockBatchRequest{
		PostId: created.Msg.Id, Locale: "en",
		Batch: postParagraphBatch(
			sourceWrite.Msg.DocumentRevision, memberID, "en", blockIDs[1:2], []string{""}, false,
		),
		AffectedLocaleValues: postParagraphContentTargets(blockIDs[1]),
	}))
	require.NoError(t, err)
	require.False(t, noOpEmptyWrite.Msg.Changed)
	require.Equal(t, sourceWrite.Msg.DocumentRevision, noOpEmptyWrite.Msg.DocumentRevision)
	requirePostExplicitEmptyParagraphStorage(t, db, created.Msg.Id, "en", blockIDs[1])

	missingKO, err := internal.LoadPostBlockDocument(t.Context(), connect.NewRequest(&intrav1.LoadPostBlockDocumentRequest{
		PostId: created.Msg.Id, Locale: "ko",
		Principal: &intrav1.CollaborationPrincipal{SessionId: adminSessionID},
	}))
	require.NoError(t, err)
	require.False(t, missingKO.Msg.LocaleExists)
	require.Nil(t, missingKO.Msg.TargetRevision)
	require.Equal(t, sourceWrite.Msg.DocumentRevision, missingKO.Msg.DocumentRevision)
	require.Equal(t, "ko", missingKO.Msg.Document.Locale)

	_, err = internal.ApplyPostBlockBatch(t.Context(), connect.NewRequest(&intrav1.ApplyPostBlockBatchRequest{
		PostId: created.Msg.Id, Locale: "ko",
		Batch: postParagraphBatch(
			sourceWrite.Msg.DocumentRevision, memberID, "ko", blockIDs[:1], []string{"blocked"}, false,
		),
	}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "missing target room cannot create itself")

	koCreated := executePostTargetLifecycle(t, service, ctx, created.Msg.Id, "ko", false)
	require.True(t, koCreated.Result.Changed)
	require.Equal(t, sourceWrite.Msg.DocumentRevision, koCreated.Result.DocumentRevision.String())
	require.NotNil(t, koCreated.TargetRevision)
	requirePostTargetSeededFromSource(t, db, created.Msg.Id, "ko", len(blockIDs), len(blockIDs), blockIDs[1])
	jaCreated := executePostTargetLifecycle(t, service, ctx, created.Msg.Id, "ja", false)
	require.NotNil(t, jaCreated.TargetRevision)
	jaRevisionBeforeKOEdit := *jaCreated.TargetRevision

	koLoaded, err := internal.LoadPostBlockDocument(t.Context(), connect.NewRequest(&intrav1.LoadPostBlockDocumentRequest{
		PostId: created.Msg.Id, Locale: "ko",
		Principal: &intrav1.CollaborationPrincipal{SessionId: adminSessionID},
	}))
	require.NoError(t, err)
	require.True(t, koLoaded.Msg.LocaleExists)
	require.NotNil(t, koLoaded.Msg.TargetRevision)
	koRevisionBeforeEdit := *koLoaded.Msg.TargetRevision
	koEdit, err := internal.ApplyPostBlockBatch(t.Context(), connect.NewRequest(&intrav1.ApplyPostBlockBatchRequest{
		PostId: created.Msg.Id, Locale: "ko", ExpectedTargetRevision: koLoaded.Msg.TargetRevision,
		Batch: postParagraphBatch(
			koLoaded.Msg.DocumentRevision, memberID, "ko", blockIDs[:1], []string{"edited-ko"}, false,
		),
	}))
	require.NoError(t, err)
	require.True(t, koEdit.Msg.Changed)
	require.Equal(t, sourceWrite.Msg.DocumentRevision, koEdit.Msg.DocumentRevision)
	require.NotNil(t, koEdit.Msg.TargetRevision)
	require.NotEqual(t, koRevisionBeforeEdit, *koEdit.Msg.TargetRevision)
	_, err = internal.ApplyPostBlockBatch(t.Context(), connect.NewRequest(&intrav1.ApplyPostBlockBatchRequest{
		PostId: created.Msg.Id, Locale: "ko", ExpectedTargetRevision: koLoaded.Msg.TargetRevision,
		Batch: postParagraphBatch(
			koLoaded.Msg.DocumentRevision, memberID, "ko", blockIDs[:1], []string{"stale-ko"}, false,
		),
	}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	requirePostConflict(
		t,
		err,
		intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
	)
	jaAfterKOEdit := loadPostTargetRoom(t, internal, adminSessionID, created.Msg.Id, "ja")
	require.Equal(t, jaRevisionBeforeKOEdit, *jaAfterKOEdit.Msg.TargetRevision)
	require.Equal(t, sourceWrite.Msg.DocumentRevision, jaAfterKOEdit.Msg.DocumentRevision)

	insertSparsePostTargetMetadata(t, db, created.Msg.Id, "fr")
	frLoaded := loadPostTargetRoom(t, internal, adminSessionID, created.Msg.Id, "fr")
	frEdit, err := internal.ApplyPostBlockBatch(t.Context(), connect.NewRequest(&intrav1.ApplyPostBlockBatchRequest{
		PostId: created.Msg.Id, Locale: "fr", ExpectedTargetRevision: frLoaded.Msg.TargetRevision,
		Batch: postParagraphBatch(
			frLoaded.Msg.DocumentRevision, memberID, "fr", blockIDs[:1], []string{"edited-fr"}, false,
		),
	}))
	require.NoError(t, err)
	require.NotNil(t, frEdit.Msg.TargetRevision)
	requirePostTargetOverlayCount(t, db, created.Msg.Id, "fr", 1)
	requirePostTargetBlockAbsent(t, db, created.Msg.Id, "fr", blockIDs[1])

	structural := postParagraphBatch(
		sourceWrite.Msg.DocumentRevision, memberID, "ko", []string{testutil.PostIntegrationUUID()}, []string{"forbidden"}, true,
	)
	_, err = internal.ApplyPostBlockBatch(t.Context(), connect.NewRequest(&intrav1.ApplyPostBlockBatchRequest{
		PostId: created.Msg.Id, Locale: "ko", ExpectedTargetRevision: koEdit.Msg.TargetRevision, Batch: structural,
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	requirePostMutationRejection(
		t,
		err,
		intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
	)
	_, err = internal.ApplyPostBlockBatch(t.Context(), connect.NewRequest(&intrav1.ApplyPostBlockBatchRequest{
		PostId: created.Msg.Id, Locale: "ko", ExpectedTargetRevision: koEdit.Msg.TargetRevision,
		Batch: postFileStructureBatch(sourceWrite.Msg.DocumentRevision, memberID),
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	requirePostMutationRejection(
		t,
		err,
		intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
	)

	sourceSecondWrite, err := internal.ApplyPostBlockBatch(t.Context(), connect.NewRequest(&intrav1.ApplyPostBlockBatchRequest{
		PostId: created.Msg.Id, Locale: "en",
		Batch: postParagraphBatch(
			sourceWrite.Msg.DocumentRevision, memberID, "en", blockIDs[1:2], []string{"source-fallback-changed"}, false,
		),
	}))
	require.NoError(t, err)
	require.NotEqual(t, sourceWrite.Msg.DocumentRevision, sourceSecondWrite.Msg.DocumentRevision)
	koAfterSource := loadPostTargetRoom(t, internal, adminSessionID, created.Msg.Id, "ko")
	jaAfterSource := loadPostTargetRoom(t, internal, adminSessionID, created.Msg.Id, "ja")
	require.NotEqual(t, *koEdit.Msg.TargetRevision, *koAfterSource.Msg.TargetRevision)
	require.NotEqual(t, jaRevisionBeforeKOEdit, *jaAfterSource.Msg.TargetRevision)
	require.Equal(t, sourceSecondWrite.Msg.DocumentRevision, koAfterSource.Msg.DocumentRevision)
	require.Equal(t, sourceSecondWrite.Msg.DocumentRevision, jaAfterSource.Msg.DocumentRevision)
	frAfterSource := loadPostTargetRoom(t, internal, adminSessionID, created.Msg.Id, "fr")
	require.Equal(t, "edited-fr", postLocalizedParagraphText(t, frAfterSource.Msg.Document, blockIDs[0]))
	require.Equal(t, "source-fallback-changed", postLocalizedParagraphText(t, frAfterSource.Msg.Document, blockIDs[1]))
	requirePostTargetBlockAbsent(t, db, created.Msg.Id, "fr", blockIDs[1])
	publicFR, publicRevision, err := postdomain.LoadLocalizedPostContentProjectionForPublic(
		t.Context(), db, store, created.Msg.Id, "fr",
	)
	require.NoError(t, err)
	require.Equal(t, sourceSecondWrite.Msg.DocumentRevision, publicRevision)
	require.Equal(t, "edited-fr", postLocalizedParagraphText(t, publicFR, blockIDs[0]))
	require.Equal(t, "source-fallback-changed", postLocalizedParagraphText(t, publicFR, blockIDs[1]))

	jaRevisionBeforeDelete := *jaAfterSource.Msg.TargetRevision
	koDeleted := executePostTargetLifecycle(t, service, ctx, created.Msg.Id, "ko", true)
	require.True(t, koDeleted.Result.Changed)
	require.Nil(t, koDeleted.TargetRevision)
	require.Equal(t, sourceSecondWrite.Msg.DocumentRevision, koDeleted.Result.DocumentRevision.String())
	requirePostTargetDeleted(t, db, created.Msg.Id, "ko")
	jaAfterDelete := loadPostTargetRoom(t, internal, adminSessionID, created.Msg.Id, "ja")
	require.Equal(t, jaRevisionBeforeDelete, *jaAfterDelete.Msg.TargetRevision)
	require.Equal(t, sourceSecondWrite.Msg.DocumentRevision, jaAfterDelete.Msg.DocumentRevision)
}

func executePostTargetLifecycle(
	t *testing.T,
	service *postdomain.PostService,
	ctx context.Context,
	postID string,
	locale string,
	deleteTranslation bool,
) postdomain.AIDocumentMutationResult {
	t.Helper()
	result, err := service.ExecuteAIDocumentMutation(
		ctx,
		postID,
		locale,
		postdomain.AIDocumentExecutionApply,
		func(state postdomain.AIDocumentState) (postdomain.AIDocumentMutation, error) {
			documentID, parseErr := uuid.Parse(state.ContentDocumentID)
			if parseErr != nil {
				return postdomain.AIDocumentMutation{}, parseErr
			}
			revision, parseErr := uuid.Parse(state.DocumentRevision)
			if parseErr != nil {
				return postdomain.AIDocumentMutation{}, parseErr
			}
			contributorID, parseErr := uuid.Parse(state.ViewerMemberID)
			if parseErr != nil {
				return postdomain.AIDocumentMutation{}, parseErr
			}
			mutation := postdomain.AIDocumentMutation{
				PostID: state.PostID, Locale: state.RequestedLocale,
				ObservedSourceLocale: state.SourceLocale, ObservedLocaleExists: state.LocaleExists,
				ExpectedRevision: revision, ExpectedTargetRevision: state.TargetRevision,
				ContributorMemberID: contributorID,
				Batch:               contentblockBatch(documentID, revision, contributorID),
			}
			if deleteTranslation {
				mutation.DeleteTranslation = true
			} else {
				mutation.Metadata.EnsureLocale = true
			}
			return mutation, nil
		},
	)
	require.NoError(t, err)
	return result
}

func contentblockBatch(documentID, revision, contributorID uuid.UUID) contentblock.Batch {
	return contentblock.Batch{
		DocumentID: documentID, ExpectedRevision: revision,
		ContributorMemberIDs: []uuid.UUID{contributorID},
	}
}

func postParagraphBatch(
	expectedRevision string,
	contributorMemberID string,
	locale string,
	blockIDs []string,
	values []string,
	includeStructure bool,
) *contentv1.RichTextBlockMutationBatch {
	mutations := make([]*contentv1.RichTextBlockMutation, 0, len(blockIDs))
	localeMutations := make([]*contentv1.RichTextBlockLocaleMutation, 0, len(blockIDs))
	for index, blockID := range blockIDs {
		if includeStructure {
			mutations = append(mutations, &contentv1.RichTextBlockMutation{
				Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{
					Node: &contentv1.RichTextBlockNode{
						Block: &contentv1.RichTextBlock{
							Id: blockID,
							Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{
								Props: &contentv1.ParagraphProps{},
							}},
						},
						Placement: &contentv1.ContentBlockPlacement{Index: uint32(index)},
					},
				}},
			})
		}
		content := []*contentv1.RichTextInline(nil)
		if values[index] != "" {
			content = []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
				Text: &contentv1.RichTextStyledText{Text: values[index]},
			}}}
		}
		localeMutations = append(localeMutations, &contentv1.RichTextBlockLocaleMutation{
			Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{
				Block: &contentv1.RichTextBlockLocale{
					BlockId: blockID,
					Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
						Props: &contentv1.ParagraphLocaleProps{}, Content: content,
					}},
				},
			}},
		})
	}
	return &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		ExpectedRevision:        expectedRevision,
		ContributorMemberIds:    []string{contributorMemberID},
		BaseMutations:           mutations,
		LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
			Locale: locale, Mutations: localeMutations,
		}},
	}
}

func postFileStructureBatch(expectedRevision string, contributorMemberID string) *contentv1.RichTextBlockMutationBatch {
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
						Value: &contentv1.RichTextBlock_File{File: &contentv1.FileBlock{Props: &contentv1.FileProps{
							Attachment: &contentv1.FileAttachment{State: &contentv1.FileAttachment_ActiveFileId{
								ActiveFileId: testutil.PostIntegrationUUID(),
							}},
						}}},
					},
					Placement: &contentv1.ContentBlockPlacement{},
				},
			}},
		}},
	}
}

func loadPostTargetRoom(
	t *testing.T,
	internal *postdomain.InternalPostService,
	sessionID string,
	postID string,
	locale string,
) *connect.Response[intrav1.LoadPostBlockDocumentResponse] {
	t.Helper()
	loaded, err := internal.LoadPostBlockDocument(t.Context(), connect.NewRequest(&intrav1.LoadPostBlockDocumentRequest{
		PostId: postID, Locale: locale,
		Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
	}))
	require.NoError(t, err)
	require.True(t, loaded.Msg.LocaleExists)
	require.NotNil(t, loaded.Msg.TargetRevision)
	return loaded
}

func requirePostTargetSeededFromSource(
	t *testing.T,
	db *gorm.DB,
	postID string,
	locale string,
	wantBlocks int,
	wantEqualBlocks int,
	explicitEmptyBlockID string,
) {
	t.Helper()
	var row struct {
		SourceBlocks int64 `gorm:"column:source_blocks"`
		TargetBlocks int64 `gorm:"column:target_blocks"`
		EqualBlocks  int64 `gorm:"column:equal_blocks"`
	}
	require.NoError(t, db.Raw(`
		SELECT count(source.block_id) AS source_blocks,
		       count(target.block_id) AS target_blocks,
		       count(*) FILTER (WHERE source.localized_data = target.localized_data) AS equal_blocks
		FROM post AS root
		JOIN content_block AS block ON block.document_id = root.content_document_id
		LEFT JOIN content_block_locale AS source
		  ON source.block_id = block.id AND source.locale = root.source_locale
		LEFT JOIN content_block_locale AS target
		  ON target.block_id = block.id AND target.locale = ?
		WHERE root.id = ?::uuid`, locale, postID).Scan(&row).Error)
	require.EqualValues(t, wantBlocks, row.SourceBlocks)
	require.EqualValues(t, wantBlocks, row.TargetBlocks)
	require.EqualValues(t, wantEqualBlocks, row.EqualBlocks)
	var explicitEmptyEqual bool
	require.NoError(t, db.Raw(`
		SELECT source.localized_data = target.localized_data
		FROM content_block_locale AS source
		JOIN content_block_locale AS target ON target.block_id = source.block_id
		WHERE source.block_id = ?::uuid
		  AND source.locale = (SELECT root.source_locale FROM post AS root WHERE root.id = ?::uuid)
		  AND target.locale = ?`, explicitEmptyBlockID, postID, locale).Scan(&explicitEmptyEqual).Error)
	require.True(t, explicitEmptyEqual, "explicit-empty source Block must also be seeded")
}

func postParagraphContentTargets(blockIDs ...string) []*managev1.AIDocumentFieldTarget {
	targets := make([]*managev1.AIDocumentFieldTarget, 0, len(blockIDs))
	for _, blockID := range blockIDs {
		targets = append(targets, &managev1.AIDocumentFieldTarget{
			Owner:       &managev1.AIDocumentFieldTarget_BlockHandle{BlockHandle: blockID},
			FieldHandle: "content",
		})
	}
	sort.Slice(targets, func(left, right int) bool {
		return targets[left].GetBlockHandle() < targets[right].GetBlockHandle()
	})
	return targets
}

func requirePostParagraphContentTargets(
	t *testing.T,
	got []*managev1.AIDocumentFieldTarget,
	blockIDs ...string,
) {
	t.Helper()
	want := postParagraphContentTargets(blockIDs...)
	require.Len(t, got, len(want))
	for index := range want {
		require.Equal(t, want[index].GetBlockHandle(), got[index].GetBlockHandle())
		require.Equal(t, want[index].GetFieldHandle(), got[index].GetFieldHandle())
		require.Empty(t, got[index].GetPath())
	}
}

func requirePostExplicitEmptyParagraphStorage(
	t *testing.T,
	db *gorm.DB,
	postID string,
	locale string,
	blockID string,
) {
	t.Helper()
	var stored string
	require.NoError(t, db.Raw(`
		SELECT locale_row.localized_data::text
		FROM content_block_locale AS locale_row
		JOIN content_block AS block ON block.id = locale_row.block_id
		JOIN post AS root ON root.content_document_id = block.document_id
		WHERE root.id = ?::uuid
		  AND locale_row.locale = ?
		  AND block.id = ?::uuid`, postID, locale, blockID).Scan(&stored).Error)
	require.JSONEq(t, `{"paragraph":{"props":{},"content":[]}}`, stored)
}

func requirePostTargetDeleted(t *testing.T, db *gorm.DB, postID string, locale string) {
	t.Helper()
	var metadataCount int64
	require.NoError(t, db.Table("post_translation").
		Where("entity_id = ?::uuid AND locale = ?", postID, locale).
		Count(&metadataCount).Error)
	require.Zero(t, metadataCount)
	var overlayCount int64
	require.NoError(t, db.Raw(`
		SELECT count(*)
		FROM content_block_locale AS locale_row
		JOIN content_block AS block ON block.id = locale_row.block_id
		JOIN post AS root ON root.content_document_id = block.document_id
		WHERE root.id = ?::uuid AND locale_row.locale = ?`, postID, locale).Scan(&overlayCount).Error)
	require.Zero(t, overlayCount)
}

func insertSparsePostTargetMetadata(t *testing.T, db *gorm.DB, postID string, locale string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, db.Exec(`
		INSERT INTO post_translation (entity_id, locale, created_at, updated_at)
		VALUES (?::uuid, ?, ?, ?)`, postID, locale, now, now).Error)
}

func requirePostTargetOverlayCount(t *testing.T, db *gorm.DB, postID string, locale string, want int64) {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(`
		SELECT count(*)
		FROM content_block_locale AS locale_row
		JOIN content_block AS block ON block.id = locale_row.block_id
		JOIN post AS root ON root.content_document_id = block.document_id
		WHERE root.id = ?::uuid AND locale_row.locale = ?`, postID, locale).Scan(&count).Error)
	require.Equal(t, want, count)
}

func requirePostTargetBlockAbsent(
	t *testing.T,
	db *gorm.DB,
	postID string,
	locale string,
	blockID string,
) {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(`
		SELECT count(*)
		FROM content_block_locale AS locale_row
		JOIN content_block AS block ON block.id = locale_row.block_id
		JOIN post AS root ON root.content_document_id = block.document_id
		WHERE root.id = ?::uuid AND locale_row.locale = ? AND block.id = ?::uuid`,
		postID, locale, blockID,
	).Scan(&count).Error)
	require.Zero(t, count)
}

func postLocalizedParagraphText(
	t *testing.T,
	document *contentv1.LocalizedRichTextDocument,
	blockID string,
) string {
	t.Helper()
	for _, block := range document.GetLocaleOverlay().GetBlocks() {
		if block.GetBlockId() != blockID {
			continue
		}
		content := block.GetParagraph().GetContent()
		if len(content) == 0 {
			return ""
		}
		return content[0].GetText().GetText()
	}
	t.Fatalf("localized paragraph %s is missing", blockID)
	return ""
}

func requirePostMutationRejection(
	t *testing.T,
	err error,
	want intrav1.CollaborationMutationRejectionReason,
) {
	t.Helper()
	var connectErr *connect.Error
	require.True(t, errors.As(err, &connectErr))
	for _, detail := range connectErr.Details() {
		value, detailErr := detail.Value()
		require.NoError(t, detailErr)
		if rejection, ok := value.(*intrav1.CollaborationMutationRejectionDetail); ok {
			require.Equal(t, want, rejection.Reason)
			return
		}
	}
	t.Fatal("missing CollaborationMutationRejectionDetail")
}

func requirePostConflict(
	t *testing.T,
	err error,
	want intrav1.CollaborationConflictReason,
) {
	t.Helper()
	var connectErr *connect.Error
	require.True(t, errors.As(err, &connectErr))
	for _, detail := range connectErr.Details() {
		value, detailErr := detail.Value()
		require.NoError(t, detailErr)
		if conflict, ok := value.(*intrav1.CollaborationConflictDetail); ok {
			require.Equal(t, want, conflict.Reason)
			return
		}
	}
	t.Fatal("missing CollaborationConflictDetail")
}
