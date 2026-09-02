//go:build integration

package page

import (
	"context"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestInternalPageBlockAggregateLifecycleIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Page Block Admin")
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, adminID, policyv1.Role.Admin())
	ctx := workIntegrationAdminCtx(adminID)
	store := newPageIntegrationContentBlockStore(t, spiceDB)

	pageService := NewPageService(
		db,
		newPageRuntimeForTest(db, "https://cdn.example.com"),
		&recordingPageDeleteFileDeleter{},
		noopAsyncPublisher{},
		&fakeIdentityManager{identity: postIntegrationIdentity(adminID, "en")},
		spiceDB,
		WithPageContentBlockStore(store),
		WithPageContentBlockMediaHydrator(passthroughPageContentBlockMediaHydrator{}),
	)
	created, err := pageService.CreatePage(ctx, connect.NewRequest(&managev1.CreatePageRequest{
		Title: "Typed Page",
	}))
	require.NoError(t, err)

	sessionID := insertPageIntegrationSession(t, db, adminID)
	internalService := NewInternalPageService(
		db,
		noopAsyncPublisher{},
		spiceDB,
		newPageRuntimeForTest(db, "https://cdn.example.com"),
		WithInternalPageDomainAuditWriter(apitelemetry.NewDurableWriter(db)),
		WithInternalPageContentBlockStore(store),
		WithInternalPageContentBlockMediaHydrator(passthroughPageContentBlockMediaHydrator{}),
	)
	loaded, err := internalService.LoadPageBlockDocument(
		ctx,
		connect.NewRequest(&intrav1.LoadPageBlockDocumentRequest{
			PageId:    created.Msg.Id,
			Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
			Locale:    "en",
		}),
	)
	require.NoError(t, err)
	require.Empty(t, loaded.Msg.Document.Base.Nodes)
	require.Equal(t, "en", loaded.Msg.Document.Locale)
	require.NotEmpty(t, loaded.Msg.DocumentRevision)
	require.Equal(t, "en", loaded.Msg.SourceMetadata.GetLocale())
	require.Nil(t, loaded.Msg.TargetRevision)
	require.Empty(t, loaded.Msg.PresentLocaleValues)

	columnsID := integrationTestUUID()
	columnAID := integrationTestUUID()
	columnBID := integrationTestUUID()
	richSectionID := integrationTestUUID()
	paragraphID := integrationTestUUID()
	memberID := integrationMemberID(adminID)
	applied, err := internalService.ApplyPageBlockBatch(
		withPageAuditedCollabRequestContext(t, context.Background()),
		connect.NewRequest(&intrav1.ApplyPageBlockBatchRequest{
			PageId: created.Msg.Id,
			Locale: "en",
			AffectedLocaleValues: []*managev1.AIDocumentFieldTarget{{
				Owner:       &managev1.AIDocumentFieldTarget_BlockHandle{BlockHandle: paragraphID},
				FieldHandle: "content",
			}},
			Batch: pageColumnsRichTextBatch(
				loaded.Msg.DocumentRevision,
				columnsID,
				columnAID,
				columnBID,
				richSectionID,
				paragraphID,
				map[string]string{"en": "Source paragraph"},
				[]string{memberID},
			),
		}),
	)
	require.NoError(t, err)
	require.True(t, applied.Msg.Changed)
	require.NotEqual(t, loaded.Msg.DocumentRevision, applied.Msg.DocumentRevision)
	afterApplied, err := internalService.LoadPageBlockDocument(ctx, connect.NewRequest(&intrav1.LoadPageBlockDocumentRequest{
		PageId: created.Msg.Id, Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID}, Locale: "en",
	}))
	require.NoError(t, err)
	require.Equal(t, "Source paragraph", pageParagraphText(
		t, afterApplied.Msg.Document, richSectionID, paragraphID,
	))
	require.Len(t, afterApplied.Msg.PresentLocaleValues, 1)
	require.Equal(t, paragraphID, afterApplied.Msg.PresentLocaleValues[0].GetBlockHandle())
	require.Equal(t, "content", afterApplied.Msg.PresentLocaleValues[0].GetFieldHandle())
	require.Empty(t, afterApplied.Msg.PresentLocaleValues[0].GetPath())
	documentID, err := loadPageContentDocumentID(ctx, db, created.Msg.Id)
	require.NoError(t, err)
	var targetRevision *string
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, targetRevision, err = applyPageTargetLocaleBatch(
			ctx,
			tx,
			store,
			created.Msg.Id,
			documentID,
			"ko",
			contentblock.Batch{
				DocumentID:       documentID,
				ExpectedRevision: uuid.MustParse(afterApplied.Msg.DocumentRevision),
			},
			nil,
			pageTargetMetadataPatch{EnsureLocale: true},
			true,
			false,
			time.Now().UTC(),
			lockedPageContentFence(created.Msg.Id),
		)
		return err
	}))
	require.NotNil(t, targetRevision)
	targetRoom, err := internalService.LoadPageBlockDocument(ctx, connect.NewRequest(&intrav1.LoadPageBlockDocumentRequest{
		PageId: created.Msg.Id, Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID}, Locale: "ko",
	}))
	require.NoError(t, err)
	require.True(t, targetRoom.Msg.LocaleExists)
	require.Equal(t, *targetRevision, targetRoom.Msg.GetTargetRevision())
	require.Equal(t, "Source paragraph", pageParagraphText(t, targetRoom.Msg.Document, richSectionID, paragraphID))
	require.Empty(t, targetRoom.Msg.PresentLocaleValues, "source fallback must not become target presence")
	requirePageContentRows(
		t,
		db,
		created.Msg.Id,
		columnsID,
		columnAID,
		richSectionID,
		paragraphID,
	)

	moved, err := internalService.ApplyPageBlockBatch(
		ctx,
		connect.NewRequest(&intrav1.ApplyPageBlockBatchRequest{
			PageId: created.Msg.Id,
			Locale: "en",
			Batch: pageMoveSectionBatch(
				applied.Msg.DocumentRevision,
				richSectionID,
				columnsID,
				columnBID,
				[]string{memberID},
			),
		}),
	)
	require.NoError(t, err)
	require.True(t, moved.Msg.Changed)
	requirePageSectionSlot(t, db, richSectionID, columnsID, "column-"+columnBID)

	noop, err := internalService.ApplyPageBlockBatch(
		ctx,
		connect.NewRequest(&intrav1.ApplyPageBlockBatchRequest{
			PageId: created.Msg.Id,
			Locale: "en",
			Batch: pageMoveSectionBatch(
				moved.Msg.DocumentRevision,
				richSectionID,
				columnsID,
				columnBID,
				[]string{memberID},
			),
		}),
	)
	require.NoError(t, err)
	require.False(t, noop.Msg.Changed)
	require.Equal(t, moved.Msg.DocumentRevision, noop.Msg.DocumentRevision)

	_, err = internalService.ApplyPageBlockBatch(
		ctx,
		connect.NewRequest(&intrav1.ApplyPageBlockBatchRequest{
			PageId: created.Msg.Id,
			Locale: "en",
			Batch: pageMoveSectionBatch(
				loaded.Msg.DocumentRevision,
				richSectionID,
				columnsID,
				columnAID,
				[]string{memberID},
			),
		}),
	)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	_, err = internalService.ApplyPageBlockBatch(
		ctx,
		connect.NewRequest(&intrav1.ApplyPageBlockBatchRequest{
			PageId: created.Msg.Id,
			Locale: "en",
			Batch: pageMoveSectionBatch(
				moved.Msg.DocumentRevision,
				richSectionID,
				columnsID,
				columnAID,
				[]string{memberID, memberID},
			),
		}),
	)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	unauthorizedIdentityID := integrationTestUUID()
	unauthorizedMemberID := seedExternalKratosIdentityWithTraits(
		t, db, unauthorizedIdentityID, "Unauthorized Page contributor",
	)
	_, err = internalService.ApplyPageBlockBatch(
		withPageAuditedCollabRequestContext(t, context.Background()),
		connect.NewRequest(&intrav1.ApplyPageBlockBatchRequest{
			PageId: created.Msg.Id,
			Locale: "en",
			Batch: pageMoveSectionBatch(
				moved.Msg.DocumentRevision,
				richSectionID,
				columnsID,
				columnAID,
				[]string{unauthorizedMemberID},
			),
		}),
	)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestInternalPageMetadataCheckpointAndMissingFileIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Page Metadata Admin")
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, adminID, policyv1.Role.Admin())
	ctx := withPageAuditedRequestContext(t, workIntegrationAdminCtx(adminID))
	store := newPageIntegrationContentBlockStore(t, spiceDB)
	pageService := NewPageService(
		db,
		newPageRuntimeForTest(db, "https://cdn.example.com"),
		&recordingPageDeleteFileDeleter{},
		noopAsyncPublisher{},
		&fakeIdentityManager{identity: postIntegrationIdentity(adminID, "en")},
		spiceDB,
		WithPageContentBlockStore(store),
		WithPageContentBlockMediaHydrator(passthroughPageContentBlockMediaHydrator{}),
	)
	created, err := pageService.CreatePage(ctx, connect.NewRequest(&managev1.CreatePageRequest{Title: "Metadata Page"}))
	require.NoError(t, err)

	sessionID := insertPageIntegrationSession(t, db, adminID)
	internalService := NewInternalPageService(
		db,
		noopAsyncPublisher{},
		spiceDB,
		newPageRuntimeForTest(db, "https://cdn.example.com"),
		WithInternalPageDomainAuditWriter(apitelemetry.NewDurableWriter(db)),
		WithInternalPageContentBlockStore(store),
		WithInternalPageContentBlockMediaHydrator(passthroughPageContentBlockMediaHydrator{}),
	)
	loaded, err := internalService.LoadPageBlockDocument(ctx, connect.NewRequest(&intrav1.LoadPageBlockDocumentRequest{
		PageId: created.Msg.Id, Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID}, Locale: "en",
	}))
	require.NoError(t, err)
	memberID := integrationMemberID(adminID)

	nextTitle := "Updated metadata title"
	metadata, err := internalService.UpdatePageLocaleMetadata(ctx, connect.NewRequest(&intrav1.UpdatePageLocaleMetadataRequest{
		PageId:               created.Msg.Id,
		Title:                &nextTitle,
		SummaryChange:        &intrav1.UpdatePageLocaleMetadataRequest_ClearSummary{ClearSummary: true},
		ExpectedRevision:     loaded.Msg.DocumentRevision,
		ContributorMemberIds: []string{memberID},
		Locale:               "en",
	}))
	require.NoError(t, err)
	require.True(t, metadata.Msg.Changed)
	require.NotEqual(t, loaded.Msg.DocumentRevision, metadata.Msg.DocumentRevision)

	layout, err := internalService.UpdatePageDocumentMetadata(ctx, connect.NewRequest(&intrav1.UpdatePageDocumentMetadataRequest{
		PageId:           created.Msg.Id,
		ExpectedRevision: metadata.Msg.DocumentRevision,
		Locale:           "en",
		DocumentLayout: &commonv1.DocumentLayout{
			ContentHeight: commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_VIEWPORT,
			PageChrome:    commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_PINNED,
			Footer:        commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW,
		},
		ContributorMemberIds: []string{memberID},
	}))
	require.NoError(t, err)
	require.True(t, layout.Msg.Changed)
	require.NotEqual(t, metadata.Msg.DocumentRevision, layout.Msg.DocumentRevision)
	require.False(t, layout.Msg.SourceChanged)

	checkpoint, err := internalService.CreatePageVersionCheckpoint(withPageAuditedCollabRequestContext(t, ctx), connect.NewRequest(&intrav1.CreatePageVersionCheckpointRequest{
		PageId:               created.Msg.Id,
		ExpectedRevision:     layout.Msg.DocumentRevision,
		ContributorMemberIds: []string{memberID},
		Locale:               "en",
	}))
	require.NoError(t, err)
	require.True(t, checkpoint.Msg.Created)
	require.NotNil(t, checkpoint.Msg.VersionId)
	require.Equal(t, layout.Msg.DocumentRevision, checkpoint.Msg.Revision)

	duplicate, err := internalService.CreatePageVersionCheckpoint(withPageAuditedCollabRequestContext(t, ctx), connect.NewRequest(&intrav1.CreatePageVersionCheckpointRequest{
		PageId:               created.Msg.Id,
		ExpectedRevision:     layout.Msg.DocumentRevision,
		ContributorMemberIds: []string{memberID},
		Locale:               "en",
	}))
	require.NoError(t, err)
	require.False(t, duplicate.Msg.Created)
	require.Nil(t, duplicate.Msg.VersionId)

	sectionID := integrationTestUUID()
	fileBlockID := integrationTestUUID()
	missingFileID := integrationTestUUID()
	_, err = internalService.ApplyPageBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyPageBlockBatchRequest{
		PageId: created.Msg.Id,
		Locale: "en",
		Batch: pageMissingFileBatch(
			layout.Msg.DocumentRevision, sectionID, fileBlockID, missingFileID, memberID,
		),
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	afterFailure, err := internalService.LoadPageBlockDocument(ctx, connect.NewRequest(&intrav1.LoadPageBlockDocumentRequest{
		PageId: created.Msg.Id, Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID}, Locale: "en",
	}))
	require.NoError(t, err)
	require.Equal(t, layout.Msg.DocumentRevision, afterFailure.Msg.DocumentRevision)
}

func pageColumnsRichTextBatch(
	expectedRevision string,
	columnsID string,
	columnAID string,
	columnBID string,
	richSectionID string,
	paragraphID string,
	localizedText map[string]string,
	contributors []string,
) *contentv1.PageSectionMutationBatch {
	parentSectionID := columnsID
	columnID := columnAID
	batch := &contentv1.PageSectionMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		ExpectedRevision:        expectedRevision,
		ContributorMemberIds:    contributors,
		BaseMutations: []*contentv1.PageSectionMutation{
			{
				Operation: &contentv1.PageSectionMutation_Upsert{Upsert: &contentv1.UpsertPageSection{
					Node: &contentv1.PageSectionNode{
						Section: &contentv1.PageSection{
							Id:       columnsID,
							Settings: &contentv1.PageSectionSettings{},
							Value: &contentv1.PageSection_Columns{Columns: &contentv1.ColumnsSection{
								Props: &contentv1.ColumnsSectionProps{Columns: []*contentv1.ColumnsSectionProps_ColumnsItem{
									{Id: columnAID, Ratio: 1},
									{Id: columnBID, Ratio: 1},
								}},
							}},
						},
						Placement: &contentv1.PageSectionPlacement{Index: 0},
					},
				}},
			},
			{
				Operation: &contentv1.PageSectionMutation_Upsert{Upsert: &contentv1.UpsertPageSection{
					Node: &contentv1.PageSectionNode{
						Section: &contentv1.PageSection{
							Id:       richSectionID,
							Settings: &contentv1.PageSectionSettings{},
							Value: &contentv1.PageSection_RichText{RichText: &contentv1.RichTextSection{
								Props:  &contentv1.RichTextSectionProps{},
								Blocks: &contentv1.RichTextBlockGraph{},
							}},
						},
						Placement: &contentv1.PageSectionPlacement{
							ParentSectionId: &parentSectionID,
							ColumnId:        &columnID,
							Index:           0,
						},
					},
				}},
			},
			pageRichTextBaseUpsert(richSectionID, &contentv1.RichTextBlock{
				Id: paragraphID,
				Value: &contentv1.RichTextBlock_Paragraph{
					Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}},
				},
			}),
		},
	}
	locales := make([]string, 0, len(localizedText))
	for locale := range localizedText {
		locales = append(locales, locale)
	}
	slices.Sort(locales)
	for _, locale := range locales {
		mutations := []*contentv1.PageSectionLocaleMutation{
			pageSectionLocaleUpsert(&contentv1.PageSectionLocale{SectionId: columnsID, Value: &contentv1.PageSectionLocale_Columns{Columns: &contentv1.ColumnsSectionLocale{Props: &contentv1.ColumnsSectionLocaleProps{}}}}),
			pageSectionLocaleUpsert(&contentv1.PageSectionLocale{SectionId: richSectionID, Value: &contentv1.PageSectionLocale_RichText{RichText: &contentv1.RichTextSectionLocale{Props: &contentv1.RichTextSectionLocaleProps{}, Blocks: &contentv1.RichTextLocaleOverlay{}}}}),
		}
		mutations = append(mutations, pageRichTextLocaleUpsert(richSectionID, paragraphID, localizedText[locale]))
		batch.LocaleMutationGroups = append(batch.LocaleMutationGroups, &contentv1.PageLocaleMutationGroup{
			Locale:    locale,
			Mutations: mutations,
		})
	}
	return batch
}

func pageSectionLocaleUpsert(
	section *contentv1.PageSectionLocale,
) *contentv1.PageSectionLocaleMutation {
	return &contentv1.PageSectionLocaleMutation{
		Operation: &contentv1.PageSectionLocaleMutation_Upsert{Upsert: &contentv1.UpsertPageSectionLocale{
			Section: section,
		}},
	}
}

func pageMoveSectionBatch(
	expectedRevision string,
	sectionID string,
	parentSectionID string,
	columnID string,
	contributors []string,
) *contentv1.PageSectionMutationBatch {
	return &contentv1.PageSectionMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		ExpectedRevision:        expectedRevision,
		ContributorMemberIds:    contributors,
		BaseMutations: []*contentv1.PageSectionMutation{{
			Operation: &contentv1.PageSectionMutation_Move{Move: &contentv1.MovePageSection{
				SectionId: sectionID,
				Placement: &contentv1.PageSectionPlacement{
					ParentSectionId: &parentSectionID,
					ColumnId:        &columnID,
					Index:           0,
				},
			}},
		}},
	}
}

func pageMissingFileBatch(
	expectedRevision string,
	sectionID string,
	fileBlockID string,
	missingFileID string,
	memberID string,
) *contentv1.PageSectionMutationBatch {
	return &contentv1.PageSectionMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		ExpectedRevision:        expectedRevision,
		ContributorMemberIds:    []string{memberID},
		BaseMutations: []*contentv1.PageSectionMutation{
			{
				Operation: &contentv1.PageSectionMutation_Upsert{Upsert: &contentv1.UpsertPageSection{
					Node: &contentv1.PageSectionNode{
						Section: &contentv1.PageSection{
							Id:       sectionID,
							Settings: &contentv1.PageSectionSettings{},
							Value: &contentv1.PageSection_RichText{RichText: &contentv1.RichTextSection{
								Props:  &contentv1.RichTextSectionProps{},
								Blocks: &contentv1.RichTextBlockGraph{},
							}},
						},
						Placement: &contentv1.PageSectionPlacement{Index: 0},
					},
				}},
			},
			pageRichTextBaseUpsert(sectionID, &contentv1.RichTextBlock{
				Id: fileBlockID,
				Value: &contentv1.RichTextBlock_File{File: &contentv1.FileBlock{
					Props: &contentv1.FileProps{Attachment: &contentv1.FileAttachment{
						State: &contentv1.FileAttachment_MissingAttachment{MissingAttachment: &contentv1.MissingAttachment{
							FormerFileId: missingFileID,
							MediaKind:    contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_FILE,
						}},
					}},
				}},
			}),
		},
		LocaleMutationGroups: []*contentv1.PageLocaleMutationGroup{{
			Locale: "en",
			Mutations: []*contentv1.PageSectionLocaleMutation{
				pageRichTextLocaleFileUpsert(sectionID, fileBlockID),
			},
		}},
	}
}

func pageRichTextBaseUpsert(
	sectionID string,
	block *contentv1.RichTextBlock,
) *contentv1.PageSectionMutation {
	return &contentv1.PageSectionMutation{
		Operation: &contentv1.PageSectionMutation_MutateRichTextBlock{MutateRichTextBlock: &contentv1.MutatePageRichTextBlock{
			SectionId: sectionID,
			Mutation: &contentv1.RichTextBlockMutation{
				Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{
					Node: &contentv1.RichTextBlockNode{
						Block:     block,
						Placement: &contentv1.ContentBlockPlacement{Index: 0},
					},
				}},
			},
		}},
	}
}

func pageRichTextLocaleUpsert(
	sectionID string,
	blockID string,
	text string,
) *contentv1.PageSectionLocaleMutation {
	return &contentv1.PageSectionLocaleMutation{
		Operation: &contentv1.PageSectionLocaleMutation_MutateRichTextBlock{MutateRichTextBlock: &contentv1.MutatePageRichTextBlockLocale{
			SectionId: sectionID,
			Mutation: &contentv1.RichTextBlockLocaleMutation{
				Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{
					Block: &contentv1.RichTextBlockLocale{
						BlockId: blockID,
						Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
							Props: &contentv1.ParagraphLocaleProps{},
							Content: []*contentv1.RichTextInline{{
								Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: text}},
							}},
						}},
					},
				}},
			},
		}},
	}
}

func pageRichTextLocaleFileUpsert(
	sectionID string,
	blockID string,
) *contentv1.PageSectionLocaleMutation {
	return &contentv1.PageSectionLocaleMutation{
		Operation: &contentv1.PageSectionLocaleMutation_MutateRichTextBlock{MutateRichTextBlock: &contentv1.MutatePageRichTextBlockLocale{
			SectionId: sectionID,
			Mutation: &contentv1.RichTextBlockLocaleMutation{
				Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{
					Block: &contentv1.RichTextBlockLocale{
						BlockId: blockID,
						Value:   &contentv1.RichTextBlockLocale_File{File: &contentv1.FileBlockLocale{Props: &contentv1.FileLocaleProps{}}},
					},
				}},
			},
		}},
	}
}

func pageParagraphText(
	t *testing.T,
	document *contentv1.LocalizedPageDocument,
	sectionID string,
	blockID string,
) string {
	t.Helper()
	for _, section := range document.GetLocaleOverlay().GetSections() {
		if section.SectionId != sectionID || section.GetRichText() == nil || section.GetRichText().Blocks == nil {
			continue
		}
		for _, block := range section.GetRichText().Blocks.Blocks {
			if block.BlockId != blockID || block.GetParagraph() == nil {
				continue
			}
			content := block.GetParagraph().Content
			require.Len(t, content, 1)
			require.NotNil(t, content[0].GetText())
			return content[0].GetText().Text
		}
	}
	t.Fatalf("missing Page paragraph %s/%s", sectionID, blockID)
	return ""
}

func requirePageContentRows(
	t *testing.T,
	db *gorm.DB,
	pageID string,
	columnsID string,
	columnID string,
	richSectionID string,
	paragraphID string,
) {
	t.Helper()
	var page model.Page
	require.NoError(t, db.Table("page").Where("id = ?", pageID).Take(&page).Error)
	require.NotNil(t, page.ContentDocumentID)
	var rows []struct {
		ID            string  `gorm:"column:id"`
		ParentID      *string `gorm:"column:parent_block_id"`
		ContainerSlot string  `gorm:"column:container_slot"`
		Position      int     `gorm:"column:position"`
	}
	require.NoError(t, db.Table("content_block").
		Select("id", "parent_block_id", "container_slot", "position").
		Where("document_id = ?", *page.ContentDocumentID).
		Order("id ASC").
		Find(&rows).Error)
	require.Len(t, rows, 3)
	byID := make(map[string]struct {
		ParentID      *string
		ContainerSlot string
		Position      int
	}, len(rows))
	for _, row := range rows {
		byID[row.ID] = struct {
			ParentID      *string
			ContainerSlot string
			Position      int
		}{row.ParentID, row.ContainerSlot, row.Position}
	}
	require.Nil(t, byID[columnsID].ParentID)
	require.Equal(t, "sections", byID[columnsID].ContainerSlot)
	require.Equal(t, columnsID, derefString(byID[richSectionID].ParentID))
	require.Equal(t, "column-"+columnID, byID[richSectionID].ContainerSlot)
	require.Equal(t, richSectionID, derefString(byID[paragraphID].ParentID))
	require.Equal(t, "content", byID[paragraphID].ContainerSlot)
}

func requirePageSectionSlot(
	t *testing.T,
	db *gorm.DB,
	sectionID string,
	parentID string,
	slot string,
) {
	t.Helper()
	var row struct {
		ParentID      *string `gorm:"column:parent_block_id"`
		ContainerSlot string  `gorm:"column:container_slot"`
	}
	require.NoError(t, db.Table("content_block").
		Select("parent_block_id", "container_slot").
		Where("id = ?", sectionID).
		Take(&row).Error)
	require.Equal(t, parentID, derefString(row.ParentID))
	require.Equal(t, slot, row.ContainerSlot)
}

func insertPageIntegrationSession(t *testing.T, db *gorm.DB, identityID string) string {
	t.Helper()
	sessionID := integrationTestUUID()
	require.NoError(t, db.Exec(`
		INSERT INTO kratos.sessions (
			id, identity_id, active, authenticated_at, expires_at,
			created_at, updated_at, nid, authentication_methods
		)
		SELECT ?::uuid, id, TRUE, NOW(), NOW() + INTERVAL '1 hour',
		       NOW(), NOW(), nid, '[]'::jsonb
		FROM kratos.identities
		WHERE id = ?::uuid
	`, sessionID, identityID).Error)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM kratos.sessions WHERE id = ?::uuid", sessionID).Error
	})
	return sessionID
}

type passthroughPageContentBlockMediaHydrator struct{}

func (passthroughPageContentBlockMediaHydrator) HydrateAuthorizedPageBlockMediaWithDB(
	_ context.Context,
	_ *gorm.DB,
	_ string,
	_ uuid.UUID,
	_ *auth.UserInfo,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return items, nil
}
