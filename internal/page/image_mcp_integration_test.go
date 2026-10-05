//go:build integration

package page

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Page cannot import the MCP adapter in an internal test without an import
// cycle. Exercise the same native exact mutation and generated storage path.
func TestPageImageFileCaptionLifecycleIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, identityID, "Page image File Block")
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	ctx := workIntegrationAdminCtx(identityID)
	store := newPageIntegrationContentBlockStore(t, spiceDB)
	runtime := newPageRuntimeForTest(db, "https://cdn.example.com")
	pages := NewPageService(db, runtime, &recordingPageDeleteFileDeleter{}, noopAsyncPublisher{}, &fakeIdentityManager{identity: postIntegrationIdentity(identityID, "en")}, spiceDB,
		WithPageContentBlockStore(store), WithPageContentBlockMediaHydrator(passthroughPageContentBlockMediaHydrator{}))
	created, err := pages.CreatePage(ctx, connect.NewRequest(&managev1.CreatePageRequest{Title: "Page image File Block"}))
	require.NoError(t, err)
	checker := &countingPageAIDocumentPermissionChecker{allowed: true}
	internal := NewInternalPageService(db, noopAsyncPublisher{}, checker, runtime, WithInternalPageContentBlockStore(store), WithInternalPageDomainAuditWriter(apitelemetry.NewDurableWriter(db)))
	application, err := NewAIDocumentService(internal)
	require.NoError(t, err)
	first := seedImageBindingUploadedFileFixture(t, db, "page/"+created.Msg.Id+"/image-mcp-first.webp")
	second := seedImageBindingUploadedFileFixture(t, db, "page/"+created.Msg.Id+"/image-mcp-second.webp")
	sectionID, blockID := uuid.NewString(), uuid.NewString()
	captionTarget := []*managev1.AIDocumentFieldTarget{{Owner: &managev1.AIDocumentFieldTarget_BlockHandle{BlockHandle: blockID}, FieldHandle: "caption"}}
	compile := func(state AIDocumentState, action, caption, file string) (AIDocumentMutation, error) {
		input := &contentv1.PageSectionMutationBatch{BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, ExpectedRevision: state.Revision, ContributorMemberIds: []string{state.ViewerMemberID}}
		affected := []*managev1.AIDocumentFieldTarget(nil)
		switch action {
		case "add":
			input = pageMissingFileBatch(state.Revision, sectionID, blockID, file, state.ViewerMemberID)
			input.BaseMutations[1].GetMutateRichTextBlock().Mutation.GetUpsert().Node.Block.GetFile().Props.Attachment = &contentv1.FileAttachment{State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: file}}
			input.LocaleMutationGroups[0].Mutations[0].GetMutateRichTextBlock().Mutation.GetUpsert().Block.GetFile().Props.Caption = &caption
			// The fixture creates a rich-text Section as well as its File child;
			// both new base Blocks need their own source locale overlay.
			input.LocaleMutationGroups[0].Mutations = append([]*contentv1.PageSectionLocaleMutation{
				pageSectionLocaleUpsert(&contentv1.PageSectionLocale{
					SectionId: sectionID,
					Value: &contentv1.PageSectionLocale_RichText{RichText: &contentv1.RichTextSectionLocale{
						Props: &contentv1.RichTextSectionLocaleProps{}, Blocks: &contentv1.RichTextLocaleOverlay{Locale: state.Locale},
					}},
				}),
			}, input.LocaleMutationGroups[0].Mutations...)
			affected = captionTarget
		case "replace":
			input.BaseMutations = []*contentv1.PageSectionMutation{pageRichTextBaseUpsert(sectionID, &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_File{File: &contentv1.FileBlock{Props: &contentv1.FileProps{Attachment: &contentv1.FileAttachment{State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: file}}}}}})}
		case "caption":
			localized := pageRichTextLocaleFileUpsert(sectionID, blockID)
			localized.GetMutateRichTextBlock().Mutation.GetUpsert().Block.GetFile().Props.Caption = &caption
			input.LocaleMutationGroups = []*contentv1.PageLocaleMutationGroup{{Locale: state.Locale, Mutations: []*contentv1.PageSectionLocaleMutation{localized}}}
			affected = captionTarget
		case "remove":
			input.BaseMutations = []*contentv1.PageSectionMutation{{Operation: &contentv1.PageSectionMutation_MutateRichTextBlock{MutateRichTextBlock: &contentv1.MutatePageRichTextBlock{SectionId: sectionID, Mutation: &contentv1.RichTextBlockMutation{Operation: &contentv1.RichTextBlockMutation_Delete{Delete: &contentv1.DeleteRichTextBlock{BlockId: blockID}}}}}}}
		}
		batch, err := contentblock.BatchFromPageProtoWithAffectedLocaleValues(state.DocumentID, input, state.Locale, affected)
		if err != nil {
			return AIDocumentMutation{}, err
		}
		return AIDocumentMutation{PageID: state.Page.ID, Locale: state.Locale, ObservedSourceLocale: state.SourceLocale, ExpectedRevision: state.Snapshot.Document.Revision, ExpectedTargetRevision: state.TargetRevision, ObservedLocaleExists: state.LocaleExists, ContributorMemberID: state.ViewerMemberID, Batch: batch, Metadata: AIDocumentMetadataPatch{EnsureLocale: action == "create target"}}, nil
	}
	apply := func(locale, action, caption, file string) AIDocumentMutationResult {
		t.Helper()
		result, err := application.ExecuteAIDocumentMutation(ctx, created.Msg.Id, locale, AIDocumentExecutionApply, func(state AIDocumentState) (AIDocumentMutation, error) { return compile(state, action, caption, file) })
		require.NoError(t, err)
		return result
	}
	before, err := application.Load(ctx, created.Msg.Id, "en")
	require.NoError(t, err)
	apply("en", "add", "source image caption", first)
	source, err := application.Load(ctx, created.Msg.Id, "en")
	require.NoError(t, err)
	require.Equal(t, first, source.Document.Base.Nodes[0].Section.GetRichText().Blocks.Nodes[0].Block.GetFile().Props.Attachment.GetActiveFileId())
	require.Equal(t, "source image caption", source.Document.LocaleOverlay.Sections[0].GetRichText().Blocks.Blocks[0].GetFile().Props.GetCaption())
	_, err = application.ExecuteAIDocumentMutation(ctx, created.Msg.Id, "en", AIDocumentExecutionApply, func(state AIDocumentState) (AIDocumentMutation, error) {
		mutation, err := compile(state, "replace", "", second)
		mutation.ExpectedRevision = before.Snapshot.Document.Revision
		mutation.Batch.ExpectedRevision = mutation.ExpectedRevision
		return mutation, err
	})
	var conflict *AIDocumentRevisionConflictError
	require.ErrorAs(t, err, &conflict)
	apply("ko", "create target", "", "")
	apply("ko", "caption", "번역 이미지", "")
	apply("ko", "caption", "", "")
	target, err := application.Load(ctx, created.Msg.Id, "ko")
	require.NoError(t, err)
	require.Equal(t, source.Revision, target.Revision)
	localizedCaption := target.Document.LocaleOverlay.Sections[0].GetRichText().Blocks.Blocks[0].GetFile().Props.Caption
	require.NotNil(t, localizedCaption)
	require.Empty(t, *localizedCaption)
	_, err = application.ExecuteAIDocumentMutation(ctx, created.Msg.Id, "ko", AIDocumentExecutionApply, func(state AIDocumentState) (AIDocumentMutation, error) { return compile(state, "replace", "", second) })
	require.Error(t, err, "target locale cannot replace shared attachment")
	apply("en", "replace", "", second)
	source, err = application.Load(ctx, created.Msg.Id, "en")
	require.NoError(t, err)
	require.Equal(t, second, source.Document.Base.Nodes[0].Section.GetRichText().Blocks.Nodes[0].Block.GetFile().Props.Attachment.GetActiveFileId())
	require.Equal(t, "source image caption", source.Document.LocaleOverlay.Sections[0].GetRichText().Blocks.Blocks[0].GetFile().Props.GetCaption())
	target, err = application.Load(ctx, created.Msg.Id, "ko")
	require.NoError(t, err)
	localizedCaption = target.Document.LocaleOverlay.Sections[0].GetRichText().Blocks.Blocks[0].GetFile().Props.Caption
	require.NotNil(t, localizedCaption)
	require.Empty(t, *localizedCaption)
	checker.allowed = false
	compilerCalled := false
	_, err = application.ExecuteAIDocumentMutation(ctx, created.Msg.Id, "en", AIDocumentExecutionApply, func(state AIDocumentState) (AIDocumentMutation, error) {
		compilerCalled = true
		return compile(state, "caption", "unauthorized", "")
	})
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	require.False(t, compilerCalled)
	checker.allowed = true
	apply("en", "remove", "", "")
	source, err = application.Load(ctx, created.Msg.Id, "en")
	require.NoError(t, err)
	require.Empty(t, source.Document.Base.Nodes[0].Section.GetRichText().Blocks.Nodes)
	var count int64
	require.NoError(t, db.Table("content_block_attachment").Where("block_id = ?::uuid", blockID).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, db.Table("file").Where("id IN ? AND delete_requested_at IS NULL", []string{first, second}).Count(&count).Error)
	require.EqualValues(t, 2, count, "placement deletion must retain reusable File records")
}
