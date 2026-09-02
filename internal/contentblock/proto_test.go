package contentblock

import (
	"encoding/json"
	"testing"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestRichTextProtoAdaptersRoundTripAggregate(t *testing.T) {
	documentID := uuid.New()
	revision := uuid.New()
	blockID := uuid.New()
	document := paragraphDocument(blockID, "hello")

	replace, err := ReplaceFromRichTextProto(documentID, revision, document)
	require.NoError(t, err)
	require.Len(t, replace.Blocks, 1)
	require.Equal(t, blockID, replace.Blocks[0].ID)
	require.Len(t, replace.LocaleOverlays, 1)

	snapshot := Snapshot{
		Document:       Document{ID: documentID, Profile: "post", Revision: revision},
		SourceLocale:   "en",
		Blocks:         replace.Blocks,
		LocaleOverlays: replace.LocaleOverlays,
	}
	materialized, err := SnapshotToRichTextDocument(snapshot)
	require.NoError(t, err)
	require.Equal(t, contentv1.ContentBlockCatalogFingerprint, materialized.GetBlockCatalogFingerprint())
	require.Equal(t, blockID.String(), materialized.GetBase().GetNodes()[0].GetBlock().GetId())
	require.Equal(t, "hello", materialized.GetLocaleOverlays()[0].GetBlocks()[0].GetParagraph().GetContent()[0].GetText().GetText())

	localized, err := SnapshotToLocalizedRichTextDocument(snapshot, "en")
	require.NoError(t, err)
	restored, err := ReplaceFromLocalizedRichTextProtoWithUnavailableAttachments(documentID, revision, localized, nil)
	require.NoError(t, err)
	require.Equal(t, replace.Blocks, restored.Blocks)
	require.Equal(t, replace.LocaleOverlays, restored.LocaleOverlays)
}

func TestLocalizedRichTextProtoAdapterKeepsTargetPresenceSparse(t *testing.T) {
	firstID := uuid.New()
	secondID := uuid.New()
	first := paragraphDocument(firstID, "source first")
	second := paragraphDocument(secondID, "source second")
	second.GetBase().GetNodes()[0].GetPlacement().Index = 1
	first.GetBase().Nodes = append(first.GetBase().Nodes, second.GetBase().GetNodes()[0])
	first.GetLocaleOverlays()[0].Blocks = append(
		first.GetLocaleOverlays()[0].Blocks,
		second.GetLocaleOverlays()[0].GetBlocks()[0],
	)
	snapshot := snapshotFromRichDocument(t, first)

	target := paragraphDocument(firstID, "translated first")
	targetReplace, err := ReplaceFromRichTextProto(uuid.New(), uuid.New(), target)
	require.NoError(t, err)
	snapshot.LocaleOverlays = append(snapshot.LocaleOverlays, LocaleOverlay{
		Locale: "ko",
		Blocks: []LocaleBlockUpdate{targetReplace.LocaleOverlays[0].Blocks[0]},
	})

	localized, err := SnapshotToLocalizedRichTextDocument(snapshot, "ko")
	require.NoError(t, err)
	require.Equal(t, "ko", localized.GetLocale())
	require.Len(t, localized.GetBase().GetNodes(), 2, "the complete current source graph remains available")
	require.Len(t, localized.GetLocaleOverlay().GetBlocks(), 1, "a newly added source Block must stay missing in target storage")
	require.Equal(t, "translated first", localized.GetLocaleOverlay().GetBlocks()[0].GetParagraph().GetContent()[0].GetText().GetText())

	absent, err := SnapshotToLocalizedRichTextDocument(snapshot, "fr")
	require.NoError(t, err)
	require.Equal(t, "fr", absent.GetLocale())
	require.Empty(t, absent.GetLocaleOverlay().GetBlocks(), "an absent target locale must not be synthesized from source")
}

func TestLocalizedRichTextProtoAdapterPreservesExplicitEmptyTargetBlock(t *testing.T) {
	blockID := uuid.New()
	snapshot := snapshotFromRichDocument(t, paragraphDocument(blockID, "source"))
	emptyTarget := paragraphDocument(blockID, "")
	targetReplace, err := ReplaceFromRichTextProto(uuid.New(), uuid.New(), emptyTarget)
	require.NoError(t, err)
	snapshot.LocaleOverlays = append(snapshot.LocaleOverlays, LocaleOverlay{
		Locale: "ko",
		Blocks: []LocaleBlockUpdate{targetReplace.LocaleOverlays[0].Blocks[0]},
	})

	localized, err := SnapshotToLocalizedRichTextDocument(snapshot, "ko")
	require.NoError(t, err)
	require.Empty(t, localized.GetLocaleOverlay().GetBlocks()[0].GetParagraph().GetContent()[0].GetText().GetText())
}

func TestLocalizedRichTextProtoAdapterPreservesEmptySourceBlocks(t *testing.T) {
	firstID := uuid.New()
	secondID := uuid.New()
	document := paragraphDocument(firstID, "")
	second := paragraphDocument(secondID, "")
	second.GetBase().GetNodes()[0].GetPlacement().Index = 1
	document.GetBase().Nodes = append(document.GetBase().Nodes, second.GetBase().GetNodes()[0])
	document.GetLocaleOverlays()[0].Blocks = append(
		document.GetLocaleOverlays()[0].Blocks,
		second.GetLocaleOverlays()[0].GetBlocks()[0],
	)

	localized, err := SnapshotToLocalizedRichTextDocument(snapshotFromRichDocument(t, document), "en")
	require.NoError(t, err)
	require.Len(t, localized.GetBase().GetNodes(), 2)
	require.Len(t, localized.GetLocaleOverlay().GetBlocks(), 2)
	for _, block := range localized.GetLocaleOverlay().GetBlocks() {
		require.Empty(t, block.GetParagraph().GetContent()[0].GetText().GetText())
	}
}

func TestLocalizedRichTextProtoAdapterNeverFallsBackForMissingSourceBlock(t *testing.T) {
	snapshot := snapshotFromRichDocument(t, paragraphDocument(uuid.New(), "source"))
	snapshot.LocaleOverlays = nil

	_, err := SnapshotToLocalizedRichTextDocument(snapshot, "en")
	require.Error(t, err)
	_, err = SnapshotToLocalizedRichTextDocument(snapshot, "ko")
	require.Error(t, err)
}

func TestLocalizedRichTextProtoAdapterIgnoresDeletedTargetBlock(t *testing.T) {
	remainingID := uuid.New()
	deletedID := uuid.New()
	snapshot := snapshotFromRichDocument(t, paragraphDocument(remainingID, "source"))
	deleted := paragraphDocument(deletedID, "old translation")
	deletedReplace, err := ReplaceFromRichTextProto(uuid.New(), uuid.New(), deleted)
	require.NoError(t, err)
	snapshot.LocaleOverlays = append(snapshot.LocaleOverlays, LocaleOverlay{
		Locale: "ko",
		Blocks: []LocaleBlockUpdate{deletedReplace.LocaleOverlays[0].Blocks[0]},
	})

	localized, err := SnapshotToLocalizedRichTextDocument(snapshot, "ko")
	require.NoError(t, err)
	require.Empty(t, localized.GetLocaleOverlay().GetBlocks(), "target units deleted from the current base graph are ignored")
}

func TestRichTextBatchAdapterCanonicalizesContributorMembers(t *testing.T) {
	documentID := uuid.New()
	revision := uuid.New()
	first := uuid.New()
	second := uuid.New()
	blockID := uuid.New()
	batch, err := BatchFromRichTextProto(documentID, &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		ExpectedRevision:        revision.String(),
		BaseMutations: []*contentv1.RichTextBlockMutation{{Operation: &contentv1.RichTextBlockMutation_Move{
			Move: &contentv1.MoveRichTextBlock{BlockId: blockID.String(), Placement: &contentv1.ContentBlockPlacement{}},
		}}},
		ContributorMemberIds: []string{second.String(), first.String()},
	})
	require.NoError(t, err)
	require.Equal(t, documentID, batch.DocumentID)
	require.Equal(t, revision, batch.ExpectedRevision)
	require.Len(t, batch.Reorders, 1)
	require.Equal(t, blockID, batch.Reorders[0].BlockID)
	require.ElementsMatch(t, []uuid.UUID{first, second}, batch.ContributorMemberIDs)
	require.Less(t, batch.ContributorMemberIDs[0].String(), batch.ContributorMemberIDs[1].String())
}

func TestMutationBatchAdaptersEnforceBrowserAndSystemContributorBoundaries(t *testing.T) {
	documentID := uuid.New()
	revision := uuid.New()
	blockID := uuid.New()
	memberID := uuid.New()

	rich := &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		ExpectedRevision:        revision.String(),
		BaseMutations: []*contentv1.RichTextBlockMutation{{
			Operation: &contentv1.RichTextBlockMutation_Delete{
				Delete: &contentv1.DeleteRichTextBlock{BlockId: blockID.String()},
			},
		}},
	}
	_, err := BatchFromRichTextProto(documentID, rich)
	require.Error(t, err)
	_, err = BatchFromRichTextSystemProto(documentID, rich)
	require.NoError(t, err)
	rich.ContributorMemberIds = []string{memberID.String()}
	_, err = BatchFromRichTextProto(documentID, rich)
	require.NoError(t, err)
	_, err = BatchFromRichTextSystemProto(documentID, rich)
	require.Error(t, err)

	page := &contentv1.PageSectionMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		ExpectedRevision:        revision.String(),
		BaseMutations: []*contentv1.PageSectionMutation{{
			Operation: &contentv1.PageSectionMutation_Delete{
				Delete: &contentv1.DeletePageSection{SectionId: blockID.String()},
			},
		}},
	}
	_, err = BatchFromPageSystemProto(documentID, page)
	require.NoError(t, err)
	page.ContributorMemberIds = []string{memberID.String()}
	_, err = BatchFromPageSystemProto(documentID, page)
	require.Error(t, err)
}

func TestMutationContributorRequiresOneCanonicalMember(t *testing.T) {
	memberID := uuid.NewString()
	actual, err := MutationContributor([]string{memberID})
	require.NoError(t, err)
	require.Equal(t, memberID, actual)

	for _, values := range [][]string{nil, {}, {memberID, uuid.NewString()}, {" "}, {"not-a-uuid"}} {
		_, err := MutationContributor(values)
		require.Error(t, err)
	}
}

func TestRichTextBatchAdapterCarriesGeneratedSharedFileProvenance(t *testing.T) {
	documentID := uuid.New()
	revision := uuid.New()
	blockID := uuid.New()
	fileID := uuid.New()
	node := localizedFileDocument(blockID, activeFileAttachment(fileID)).GetBase().GetNodes()[0]
	input := &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		ExpectedRevision:        revision.String(),
		BaseMutations: []*contentv1.RichTextBlockMutation{{
			Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{Node: node}},
		}},
	}

	batch, err := BatchFromRichTextSystemProto(documentID, input)
	require.NoError(t, err)
	require.Equal(t, "post", batch.validatedProfile)
	require.Equal(t, []FileReference{{
		BlockID: blockID, ReferencePath: "file", FileID: fileID,
	}}, batch.validatedBaseReferences[blockID])

	node.GetBlock().GetFile().GetProps().Attachment = &contentv1.FileAttachment{
		State: &contentv1.FileAttachment_MissingAttachment{MissingAttachment: &contentv1.MissingAttachment{
			FormerFileId: fileID.String(),
			MediaKind:    contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_FILE,
		}},
	}
	_, err = BatchFromRichTextSystemProto(documentID, input)
	require.Error(t, err, "ordinary WRITE mutations must reject restore-only missing attachments")
}

func TestVersionRestoreAdapterRewritesUnavailableFileToTypedMissingAttachment(t *testing.T) {
	documentID := uuid.New()
	revision := uuid.New()
	blockID := uuid.New()
	fileID := uuid.New()
	input := localizedFileDocument(blockID, activeFileAttachment(fileID))
	original := proto.Clone(input).(*contentv1.LocalizedRichTextDocument)

	replace, err := ReplaceFromLocalizedRichTextProtoWithUnavailableAttachments(
		documentID,
		revision,
		input,
		map[uuid.UUID]contentv1.MissingAttachmentMediaKind{
			fileID: contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_FILE,
		},
	)
	require.NoError(t, err)
	require.True(t, proto.Equal(original, input), "restore rewrite must not mutate the Version snapshot")

	restored, err := SnapshotToLocalizedRichTextDocument(Snapshot{
		Document:       Document{ID: documentID, Profile: "post", Revision: revision},
		SourceLocale:   "en",
		Blocks:         replace.Blocks,
		LocaleOverlays: replace.LocaleOverlays,
	}, "en")
	require.NoError(t, err)
	missing := restored.GetBase().GetNodes()[0].GetBlock().GetFile().GetProps().GetAttachment().GetMissingAttachment()
	require.Equal(t, fileID.String(), missing.GetFormerFileId())
	require.Equal(t, contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_FILE, missing.GetMediaKind())

	_, err = ReplaceFromLocalizedRichTextProtoWithUnavailableAttachments(
		documentID,
		revision,
		input,
		map[uuid.UUID]contentv1.MissingAttachmentMediaKind{
			fileID: contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_UNSPECIFIED,
		},
	)
	require.Error(t, err)
}

func TestPageProtoAdaptersKeepNestedRichRows(t *testing.T) {
	documentID := uuid.New()
	revision := uuid.New()
	sectionID := uuid.New()
	blockID := uuid.New()
	document := &contentv1.PageDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		SourceLocale:            "en",
		Base: &contentv1.PageSectionGraph{Nodes: []*contentv1.PageSectionNode{{
			Section: &contentv1.PageSection{Id: sectionID.String(), Value: &contentv1.PageSection_RichText{
				RichText: &contentv1.RichTextSection{Props: &contentv1.RichTextSectionProps{}, Blocks: paragraphDocument(blockID, "nested").GetBase()},
			}},
			Placement: &contentv1.PageSectionPlacement{},
		}}},
		LocaleOverlays: []*contentv1.PageLocaleOverlay{{
			Locale: "en",
			Sections: []*contentv1.PageSectionLocale{{
				SectionId: sectionID.String(),
				Value: &contentv1.PageSectionLocale_RichText{RichText: &contentv1.RichTextSectionLocale{
					Props:  &contentv1.RichTextSectionLocaleProps{},
					Blocks: paragraphDocument(blockID, "nested").GetLocaleOverlays()[0],
				}},
			}},
		}},
	}

	replace, err := replaceFromPageProtoForTest(documentID, revision, document)
	require.NoError(t, err)
	require.Len(t, replace.Blocks, 2)
	var nested BaseBlock
	for _, block := range replace.Blocks {
		if block.ID == blockID {
			nested = block
		}
	}
	require.NotNil(t, nested.ParentID)
	require.Equal(t, sectionID, *nested.ParentID)
	require.Equal(t, "content", nested.ContainerSlot)

	snapshot := Snapshot{
		Document:       Document{ID: documentID, Profile: "page", Revision: revision},
		SourceLocale:   "en",
		Blocks:         replace.Blocks,
		LocaleOverlays: replace.LocaleOverlays,
	}
	materialized, err := SnapshotToPageDocument(snapshot)
	require.NoError(t, err)
	nestedNode := materialized.GetBase().GetNodes()[0].GetSection().GetRichText().GetBlocks().GetNodes()[0]
	require.Equal(t, blockID.String(), nestedNode.GetBlock().GetId())
	require.Empty(t, nestedNode.GetPlacement().GetParentBlockId())

	publicProjection, err := MaterializeSnapshotPageLocale(snapshot, "en")
	require.NoError(t, err)
	publicNode := publicProjection.GetBase().GetNodes()[0].GetSection().GetRichText().GetBlocks().GetNodes()[0]
	require.Equal(t, blockID.String(), publicNode.GetBlock().GetId())
	require.Empty(t, publicNode.GetPlacement().GetParentBlockId())
}

func TestMaterializeSnapshotPageLocaleFallsBackInsideSparseRichTextSection(t *testing.T) {
	sectionID := uuid.New()
	firstID := uuid.New()
	secondID := uuid.New()
	rich := paragraphDocument(firstID, "source first")
	second := paragraphDocument(secondID, "source second")
	second.GetBase().GetNodes()[0].GetPlacement().Index = 1
	rich.GetBase().Nodes = append(rich.GetBase().Nodes, second.GetBase().GetNodes()[0])
	rich.GetLocaleOverlays()[0].Blocks = append(
		rich.GetLocaleOverlays()[0].Blocks,
		second.GetLocaleOverlays()[0].GetBlocks()[0],
	)
	emptyTarget := paragraphDocument(firstID, "").GetLocaleOverlays()[0]
	emptyTarget.Locale = "ko"
	document := &contentv1.PageDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		SourceLocale:            "en",
		Base: &contentv1.PageSectionGraph{Nodes: []*contentv1.PageSectionNode{{
			Section: &contentv1.PageSection{
				Id: sectionID.String(), Settings: &contentv1.PageSectionSettings{},
				Value: &contentv1.PageSection_RichText{RichText: &contentv1.RichTextSection{
					Props: &contentv1.RichTextSectionProps{}, Blocks: rich.GetBase(),
				}},
			},
			Placement: &contentv1.PageSectionPlacement{},
		}}},
		LocaleOverlays: []*contentv1.PageLocaleOverlay{
			{Locale: "en", Sections: []*contentv1.PageSectionLocale{{
				SectionId: sectionID.String(),
				Value: &contentv1.PageSectionLocale_RichText{RichText: &contentv1.RichTextSectionLocale{
					Props: &contentv1.RichTextSectionLocaleProps{}, Blocks: rich.GetLocaleOverlays()[0],
				}},
			}}},
			{Locale: "ko", Sections: []*contentv1.PageSectionLocale{{
				SectionId: sectionID.String(),
				Value: &contentv1.PageSectionLocale_RichText{RichText: &contentv1.RichTextSectionLocale{
					Props: &contentv1.RichTextSectionLocaleProps{}, Blocks: emptyTarget,
				}},
			}}},
		},
	}
	replace, err := replaceFromPageProtoForTest(uuid.New(), uuid.New(), document)
	require.NoError(t, err)
	snapshot := Snapshot{
		Document: Document{Profile: "page"}, SourceLocale: "en",
		Blocks: replace.Blocks, LocaleOverlays: replace.LocaleOverlays,
	}

	sparse, err := SnapshotToLocalizedPageDocument(snapshot, "ko")
	require.NoError(t, err)
	require.Len(t, sparse.GetLocaleOverlay().GetSections()[0].GetRichText().GetBlocks().GetBlocks(), 1)

	localized, err := MaterializeSnapshotPageLocale(snapshot, "ko")
	require.NoError(t, err)
	blocks := localized.GetLocaleOverlay().GetSections()[0].GetRichText().GetBlocks().GetBlocks()
	require.Len(t, blocks, 2)
	byID := make(map[string]*contentv1.RichTextBlockLocale, len(blocks))
	for _, block := range blocks {
		byID[block.GetBlockId()] = block
	}
	require.Empty(t, byID[firstID.String()].GetParagraph().GetContent()[0].GetText().GetText())
	require.Equal(t, "source second", byID[secondID.String()].GetParagraph().GetContent()[0].GetText().GetText())
}

func TestPageVersionRestoreAdapterRewritesUnavailableImageAttachment(t *testing.T) {
	documentID := uuid.New()
	revision := uuid.New()
	sectionID := uuid.New()
	unitID := uuid.New()
	meshFileID := uuid.New()
	textureFileID := uuid.New()
	document := immersivePageDocument(sectionID, unitID, meshFileID, textureFileID)
	input := &contentv1.LocalizedPageDocument{
		BlockCatalogFingerprint: document.GetBlockCatalogFingerprint(),
		Locale:                  document.GetSourceLocale(),
		Base:                    document.GetBase(),
		LocaleOverlay:           document.GetLocaleOverlays()[0],
	}
	original := proto.Clone(input).(*contentv1.LocalizedPageDocument)

	replace, err := ReplaceFromLocalizedPageProtoWithUnavailableAttachments(
		documentID,
		revision,
		input,
		map[uuid.UUID]contentv1.MissingAttachmentMediaKind{
			textureFileID: contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_IMAGE,
		},
	)
	require.NoError(t, err)
	require.True(t, proto.Equal(original, input), "restore rewrite must not mutate the Page Version snapshot")

	restored, err := SnapshotToLocalizedPageDocument(Snapshot{
		Document:       Document{ID: documentID, Profile: "page", Revision: revision},
		SourceLocale:   "en",
		Blocks:         replace.Blocks,
		LocaleOverlays: replace.LocaleOverlays,
	}, "en")
	require.NoError(t, err)
	props := restored.GetBase().GetNodes()[0].GetSection().GetImmersiveScene().GetUnits()[0].GetProps()
	require.Equal(t, meshFileID.String(), props.GetMeshFile().GetActiveFileId())
	missing := props.GetTextureFile().GetMissingAttachment()
	require.Equal(t, textureFileID.String(), missing.GetFormerFileId())
	require.Equal(t, contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_IMAGE, missing.GetMediaKind())
}

func TestGeneratedContractTranslationSourceChangeIgnoresPresentationAndTracksSourceText(t *testing.T) {
	blockID := uuid.New()
	document := paragraphDocument(blockID, "source")
	initial, err := ReplaceFromRichTextProto(uuid.New(), uuid.New(), document)
	require.NoError(t, err)
	initialBlocks := replaceSourceBlocks(initial, document.GetSourceLocale())

	contract := NewGeneratedContract()
	previewWidth := int32(80)
	document.GetBase().GetNodes()[0].GetBlock().GetParagraph().Props.PreviewWidth = &previewWidth
	presentationOnly, err := ReplaceFromRichTextProto(uuid.New(), uuid.New(), document)
	require.NoError(t, err)
	presentationBlocks := replaceSourceBlocks(presentationOnly, document.GetSourceLocale())
	changed, err := contract.TranslationSourceChanged("post", initialBlocks, presentationBlocks)
	require.NoError(t, err)
	require.False(t, changed)
	document.GetLocaleOverlays()[0].GetBlocks()[0].GetParagraph().Content[0].GetText().Text = "changed"
	sourceEdit, err := ReplaceFromRichTextProto(uuid.New(), uuid.New(), document)
	require.NoError(t, err)
	sourceBlocks := replaceSourceBlocks(sourceEdit, document.GetSourceLocale())
	changed, err = contract.TranslationSourceChanged("post", presentationBlocks, sourceBlocks)
	require.NoError(t, err)
	require.True(t, changed)
}

func TestPageImmersiveUnitFilesUseStableExactReferencePaths(t *testing.T) {
	sectionID := uuid.New()
	unitID := uuid.New()
	meshFileID := uuid.New()
	textureFileID := uuid.New()
	document := immersivePageDocument(sectionID, unitID, meshFileID, textureFileID)

	replace, err := replaceFromPageProtoForTest(uuid.New(), uuid.New(), document)
	require.NoError(t, err)
	require.Len(t, replace.Blocks, 1)
	require.Len(t, replace.LocaleOverlays, 1)
	require.Len(t, replace.LocaleOverlays[0].Blocks, 1)

	normalized, err := normalizeBlock(NewGeneratedContract(), "page", FullBlock{
		BaseBlock:     replace.Blocks[0],
		LocalizedData: replace.LocaleOverlays[0].Blocks[0].LocalizedData,
	})
	require.NoError(t, err)
	require.Equal(t, sectionID, normalized.ID)
	require.ElementsMatch(t, []FileReference{
		{
			BlockID:          sectionID,
			ReferencePath:    "immersive_scene:" + unitID.String() + ":mesh",
			FileID:           meshFileID,
			AllowedMIMETypes: []string{"model/gltf-binary"},
		},
		{
			BlockID:             sectionID,
			ReferencePath:       "immersive_scene:" + unitID.String() + ":texture",
			FileID:              textureFileID,
			AllowedMIMEPrefixes: []string{"image/"},
		},
	}, normalized.FileReferences)

	replacementID := uuid.New()
	document.GetBase().GetNodes()[0].GetSection().GetImmersiveScene().GetUnits()[0].Props.TextureFile = activeFileAttachment(replacementID)
	replaced, err := replaceFromPageProtoForTest(uuid.New(), uuid.New(), document)
	require.NoError(t, err)
	replacedBlock, err := normalizeBlock(NewGeneratedContract(), "page", FullBlock{
		BaseBlock:     replaced.Blocks[0],
		LocalizedData: replaced.LocaleOverlays[0].Blocks[0].LocalizedData,
	})
	require.NoError(t, err)
	references := referenceMap(replacedBlock.FileReferences)
	require.Len(t, references, 2)
	require.Equal(t, replacementID, references["immersive_scene:"+unitID.String()+":texture"].FileID)
	for _, reference := range references {
		require.NotEqual(t, textureFileID, reference.FileID)
	}
}

func replaceFromPageProtoForTest(
	documentID uuid.UUID,
	expectedRevision uuid.UUID,
	document *contentv1.PageDocument,
) (ReplaceInput, error) {
	rows, err := contentv1.FlattenPageDocumentStorage(
		document,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE,
	)
	if err != nil {
		return ReplaceInput{}, err
	}
	return replaceFromStorage(documentID, expectedRevision, rows)
}

func immersivePageDocument(sectionID, unitID, meshFileID, textureFileID uuid.UUID) *contentv1.PageDocument {
	return &contentv1.PageDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		SourceLocale:            "en",
		Base: &contentv1.PageSectionGraph{Nodes: []*contentv1.PageSectionNode{{
			Section: &contentv1.PageSection{
				Id: sectionID.String(),
				Value: &contentv1.PageSection_ImmersiveScene{ImmersiveScene: &contentv1.ImmersiveSceneSection{
					Props: &contentv1.ImmersiveSceneSectionProps{},
					Units: []*contentv1.PageImmersiveUnit{{
						Id: unitID.String(),
						Props: &contentv1.PageImmersiveUnitProps{
							MeshFile:    activeFileAttachment(meshFileID),
							TextureFile: activeFileAttachment(textureFileID),
						},
					}},
				}},
			},
			Placement: &contentv1.PageSectionPlacement{},
		}}},
		LocaleOverlays: []*contentv1.PageLocaleOverlay{{
			Locale: "en",
			Sections: []*contentv1.PageSectionLocale{{
				SectionId: sectionID.String(),
				Value: &contentv1.PageSectionLocale_ImmersiveScene{ImmersiveScene: &contentv1.ImmersiveSceneSectionLocale{
					Props: &contentv1.ImmersiveSceneSectionLocaleProps{},
					Units: []*contentv1.PageImmersiveUnitLocale{{
						UnitId: unitID.String(), Props: &contentv1.PageImmersiveUnitLocaleProps{},
					}},
				}},
			}},
		}},
	}
}

func activeFileAttachment(fileID uuid.UUID) *contentv1.FileAttachment {
	return &contentv1.FileAttachment{State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: fileID.String()}}
}

func replaceSourceBlocks(input ReplaceInput, sourceLocale string) []FullBlock {
	state := newAggregate(Document{Profile: "post"})
	for _, block := range input.Blocks {
		state.blocks[block.ID] = FullBlock{BaseBlock: block}
	}
	for _, overlay := range input.LocaleOverlays {
		for _, block := range overlay.Blocks {
			if state.locales[block.BlockID] == nil {
				state.locales[block.BlockID] = make(map[string]json.RawMessage)
			}
			state.locales[block.BlockID][overlay.Locale] = block.LocalizedData
		}
	}
	return state.localizedBlocks(sourceLocale)
}

func paragraphDocument(blockID uuid.UUID, text string) *contentv1.RichTextDocument {
	return &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		SourceLocale:            "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{Id: blockID.String(), Value: &contentv1.RichTextBlock_Paragraph{
				Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}},
			}},
			Placement: &contentv1.ContentBlockPlacement{},
		}}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{
			Locale: "en",
			Blocks: []*contentv1.RichTextBlockLocale{{
				BlockId: blockID.String(),
				Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
					Props: &contentv1.ParagraphLocaleProps{},
					Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
						Text: &contentv1.RichTextStyledText{Text: text},
					}}},
				}},
			}},
		}},
	}
}
