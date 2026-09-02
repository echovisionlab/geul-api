package translation

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBuildProviderTargetRichTextBatchKeepsOnlyCurrentCompatibleSourceUnits(t *testing.T) {
	t.Parallel()

	documentID := uuid.New()
	revision := uuid.New()
	currentID := uuid.New()
	currentDeleteID := uuid.New()
	removedID := uuid.New()
	kindChangedID := uuid.New()
	snapshot := contentblock.Snapshot{
		Document: contentblock.Document{ID: documentID, Revision: revision},
		Blocks: []contentblock.BaseBlock{
			{ID: currentID, Kind: "paragraph"},
			{ID: currentDeleteID, Kind: "paragraph"},
			{ID: kindChangedID, Kind: "heading"},
		},
	}
	candidate := &Candidate{
		ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{
			Locale: "ko",
			Blocks: []*contentv1.RichTextBlockLocale{
				providerTargetParagraph(currentID, "translated"),
				providerTargetParagraph(removedID, "removed"),
				providerTargetParagraph(kindChangedID, "kind changed"),
			},
		},
		ContentBlockLocaleDeletes: []string{currentDeleteID.String(), removedID.String()},
	}

	batch, err := BuildProviderTargetRichTextBatch(
		snapshot,
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_EMAIL,
		"ko",
		candidate,
	)
	require.NoError(t, err)
	require.Equal(t, documentID, batch.DocumentID)
	require.Equal(t, revision, batch.ExpectedRevision)
	require.Len(t, batch.LocaleGroups, 1)
	require.Equal(t, "ko", batch.LocaleGroups[0].Locale)
	require.Len(t, batch.LocaleGroups[0].Upserts, 1)
	require.Equal(t, currentID, batch.LocaleGroups[0].Upserts[0].BlockID)
	require.Equal(t, []uuid.UUID{currentDeleteID}, batch.LocaleGroups[0].Deletes)
}

func TestBuildProviderTargetRichTextBatchDropsEmptyRemovedSourceGroup(t *testing.T) {
	t.Parallel()

	snapshot := contentblock.Snapshot{
		Document: contentblock.Document{ID: uuid.New(), Revision: uuid.New()},
	}
	removedID := uuid.New()
	candidate := &Candidate{
		ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{
			Locale: "ko",
			Blocks: []*contentv1.RichTextBlockLocale{providerTargetParagraph(removedID, "removed")},
		},
	}

	batch, err := BuildProviderTargetRichTextBatch(
		snapshot,
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_EMAIL,
		"ko",
		candidate,
	)
	require.NoError(t, err)
	require.Empty(t, batch.LocaleGroups)
}

func TestBuildProviderTargetRichTextBatchAcceptsEmptyCandidate(t *testing.T) {
	t.Parallel()

	snapshot := contentblock.Snapshot{
		Document: contentblock.Document{ID: uuid.New(), Revision: uuid.New()},
	}
	candidate := &Candidate{ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko"}}

	batch, err := BuildProviderTargetRichTextBatch(
		snapshot,
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_EMAIL,
		"ko",
		candidate,
	)
	require.NoError(t, err)
	require.Empty(t, batch.LocaleGroups)
}

func TestBuildProviderTargetRichTextBatchDoesNotCreateCurrentOnlySourceUnits(t *testing.T) {
	t.Parallel()

	documentID := uuid.New()
	revision := uuid.New()
	requestedID := uuid.New()
	currentOnlyID := uuid.New()
	document := &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		SourceLocale:            "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{
			providerTargetBaseNode(requestedID, 0), providerTargetBaseNode(currentOnlyID, 1),
		}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{
			providerTargetParagraph(requestedID, "requested source"),
			providerTargetParagraph(currentOnlyID, "current-only source"),
		}}},
	}
	replace, err := contentblock.ReplaceFromRichTextProto(documentID, revision, document)
	require.NoError(t, err)
	snapshot := contentblock.Snapshot{
		Document:     contentblock.Document{ID: documentID, Profile: "post", Revision: revision},
		SourceLocale: "en", Blocks: replace.Blocks, LocaleOverlays: replace.LocaleOverlays,
	}
	unitID := "block:" + requestedID.String() + ":typed:paragraph/content"
	plan := &ExtractionPlan{Units: []Unit{{
		UnitID: unitID, EntityType: "post", EntityID: "post-1", SourceLocale: "en",
		ContainerType: ContainerTypeBlock, ContainerID: requestedID.String(),
	}}}
	results := map[string]UnitResult{unitID: {UnitID: unitID, TranslatedText: "번역"}}
	candidate := &Candidate{ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko"}}
	require.NoError(t, candidate.SetProviderUnitPatch(plan, results))

	batch, err := BuildProviderTargetRichTextBatch(
		snapshot, contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST, "ko", candidate,
	)
	require.NoError(t, err)
	require.Len(t, batch.LocaleGroups, 1)
	require.Len(t, batch.LocaleGroups[0].Upserts, 1)
	require.Equal(t, requestedID, batch.LocaleGroups[0].Upserts[0].BlockID)
	require.Contains(t, string(batch.LocaleGroups[0].Upserts[0].LocalizedData), "번역")
	require.NotContains(t, string(batch.LocaleGroups[0].Upserts[0].LocalizedData), "current-only source")
}

func TestBuildProviderTargetRichTextBatchUsesBaseTopologyAfterSourceLocaleSwitch(t *testing.T) {
	t.Parallel()

	documentID, revision, blockID := uuid.New(), uuid.New(), uuid.New()
	document := &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		SourceLocale:            "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{
			providerTargetBaseNode(blockID, 0),
		}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{
			{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{
				providerTargetParagraph(blockID, "request-time source"),
			}},
			{Locale: "ko", Blocks: []*contentv1.RichTextBlockLocale{
				providerTargetParagraph(blockID, ""),
			}},
		},
	}
	replace, err := contentblock.ReplaceFromRichTextProto(documentID, revision, document)
	require.NoError(t, err)
	snapshot := contentblock.Snapshot{
		Document: contentblock.Document{ID: documentID, Profile: "post", Revision: revision},
		// The root now points at explicit-empty KO values. Current text emptiness
		// must not hide the still-existing base unit from the request-time EN job.
		SourceLocale: "ko", Blocks: replace.Blocks, LocaleOverlays: replace.LocaleOverlays,
	}
	unitID := "block:" + blockID.String() + ":typed:paragraph/content"
	plan := &ExtractionPlan{Units: []Unit{{
		UnitID: unitID, EntityType: "post", EntityID: "post-1", SourceLocale: "en",
		ContainerType: ContainerTypeBlock, ContainerID: blockID.String(),
	}}}
	results := map[string]UnitResult{unitID: {UnitID: unitID, TranslatedText: "late translation"}}
	candidate := &Candidate{ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{
		Locale: "fr", Blocks: []*contentv1.RichTextBlockLocale{
			providerTargetParagraph(blockID, "late translation"),
		},
	}}
	require.NoError(t, candidate.SetProviderUnitPatch(plan, results))

	batch, err := BuildProviderTargetRichTextBatch(
		snapshot, contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST, "fr", candidate,
	)
	require.NoError(t, err)
	require.Len(t, batch.LocaleGroups, 1)
	require.Len(t, batch.LocaleGroups[0].Upserts, 1)
	require.Contains(t, string(batch.LocaleGroups[0].Upserts[0].LocalizedData), "late translation")
}

func TestBuildProviderTargetRichTextBatchPreservesUnrelatedTargetTableUnit(t *testing.T) {
	t.Parallel()

	documentID, revision, blockID := uuid.New(), uuid.New(), uuid.New()
	rowID, requestedCellID, unrelatedCellID := uuid.New(), uuid.New(), uuid.New()
	document := &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST, SourceLocale: "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{Id: blockID.String(), Value: &contentv1.RichTextBlock_Table{
				Table: &contentv1.TableBlock{Props: &contentv1.TableProps{}, Content: &contentv1.RichTextTableBase{
					Rows: []*contentv1.RichTextTableRowBase{{Id: rowID.String(), Cells: []*contentv1.RichTextTableCellBase{
						{Id: requestedCellID.String()}, {Id: unrelatedCellID.String()},
					}}},
				}},
			}}, Placement: &contentv1.ContentBlockPlacement{},
		}}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{
			{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{providerTargetTable(
				blockID, rowID, requestedCellID, "source requested", unrelatedCellID, "source unrelated",
			)}},
			{Locale: "ko", Blocks: []*contentv1.RichTextBlockLocale{providerTargetTable(
				blockID, rowID, requestedCellID, "old requested", unrelatedCellID, "기존 번역",
			)}},
		},
	}
	replace, err := contentblock.ReplaceFromRichTextProto(documentID, revision, document)
	require.NoError(t, err)
	snapshot := contentblock.Snapshot{
		Document:     contentblock.Document{ID: documentID, Profile: "post", Revision: revision},
		SourceLocale: "en", Blocks: replace.Blocks, LocaleOverlays: replace.LocaleOverlays,
	}
	unitID := "block:" + blockID.String() + ":typed:table/content/rows/" + rowID.String() +
		"/cells/" + requestedCellID.String() + "/content"
	plan := &ExtractionPlan{Units: []Unit{{
		UnitID: unitID, EntityType: "post", EntityID: "post-1", SourceLocale: "en",
		ContainerType: ContainerTypeBlock, ContainerID: blockID.String(),
	}}}
	results := map[string]UnitResult{unitID: {UnitID: unitID, TranslatedText: "새 번역"}}
	candidate := &Candidate{ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko"}}
	require.NoError(t, candidate.SetProviderUnitPatch(plan, results))

	batch, err := BuildProviderTargetRichTextBatch(
		snapshot, contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST, "ko", candidate,
	)
	require.NoError(t, err)
	require.Len(t, batch.LocaleGroups, 1)
	require.Len(t, batch.LocaleGroups[0].Upserts, 1)
	data := string(batch.LocaleGroups[0].Upserts[0].LocalizedData)
	require.Contains(t, data, "새 번역")
	require.Contains(t, data, "기존 번역")
	require.NotContains(t, data, "source unrelated")
}

func TestBuildProviderTargetRichTextBatchIgnoresRequestedTableCellDeletedBeforeApply(t *testing.T) {
	t.Parallel()

	documentID, revision, blockID := uuid.New(), uuid.New(), uuid.New()
	rowID, deletedCellID, survivingCellID := uuid.New(), uuid.New(), uuid.New()
	document := &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		SourceLocale:            "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{Id: blockID.String(), Value: &contentv1.RichTextBlock_Table{
				Table: &contentv1.TableBlock{Props: &contentv1.TableProps{}, Content: &contentv1.RichTextTableBase{
					Rows: []*contentv1.RichTextTableRowBase{{Id: rowID.String(), Cells: []*contentv1.RichTextTableCellBase{
						{Id: survivingCellID.String()},
					}}},
				}},
			}}, Placement: &contentv1.ContentBlockPlacement{},
		}}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{
			{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{providerTargetSingleCellTable(
				blockID, rowID, survivingCellID, "surviving source",
			)}},
			{Locale: "ko", Blocks: []*contentv1.RichTextBlockLocale{providerTargetSingleCellTable(
				blockID, rowID, survivingCellID, "기존 번역",
			)}},
		},
	}
	replace, err := contentblock.ReplaceFromRichTextProto(documentID, revision, document)
	require.NoError(t, err)
	snapshot := contentblock.Snapshot{
		Document:     contentblock.Document{ID: documentID, Profile: "post", Revision: revision},
		SourceLocale: "en", Blocks: replace.Blocks, LocaleOverlays: replace.LocaleOverlays,
	}
	unitID := "block:" + blockID.String() + ":typed:table/content/rows/" + rowID.String() +
		"/cells/" + deletedCellID.String() + "/content"
	plan := &ExtractionPlan{Units: []Unit{{
		UnitID: unitID, EntityType: "post", EntityID: "post-1", SourceLocale: "en",
		ContainerType: ContainerTypeBlock, ContainerID: blockID.String(),
	}}}
	results := map[string]UnitResult{unitID: {UnitID: unitID, TranslatedText: "늦은 번역"}}
	candidate := &Candidate{ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko"}}
	require.NoError(t, candidate.SetProviderUnitPatch(plan, results))

	batch, err := BuildProviderTargetRichTextBatch(
		snapshot, contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST, "ko", candidate,
	)
	require.NoError(t, err)
	require.Empty(t, batch.LocaleGroups)
}

func TestBuildProviderTargetRichTextBatchRejectsLocaleMismatch(t *testing.T) {
	t.Parallel()

	_, err := BuildProviderTargetRichTextBatch(
		contentblock.Snapshot{Document: contentblock.Document{ID: uuid.New(), Revision: uuid.New()}},
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_EMAIL,
		"ko",
		&Candidate{ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ja"}},
	)
	require.Error(t, err)
}

func providerTargetParagraph(blockID uuid.UUID, text string) *contentv1.RichTextBlockLocale {
	return &contentv1.RichTextBlockLocale{
		BlockId: blockID.String(),
		Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
			Props: &contentv1.ParagraphLocaleProps{},
			Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
				Text: &contentv1.RichTextStyledText{Text: text},
			}}},
		}},
	}
}

func providerTargetBaseNode(blockID uuid.UUID, index uint32) *contentv1.RichTextBlockNode {
	return &contentv1.RichTextBlockNode{
		Block: &contentv1.RichTextBlock{Id: blockID.String(), Value: &contentv1.RichTextBlock_Paragraph{
			Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}},
		}},
		Placement: &contentv1.ContentBlockPlacement{Index: index},
	}
}

func providerTargetTable(
	blockID, rowID, firstCellID uuid.UUID,
	firstText string,
	secondCellID uuid.UUID,
	secondText string,
) *contentv1.RichTextBlockLocale {
	return &contentv1.RichTextBlockLocale{
		BlockId: blockID.String(),
		Value: &contentv1.RichTextBlockLocale_Table{Table: &contentv1.TableBlockLocale{
			Props: &contentv1.TableLocaleProps{}, Content: &contentv1.RichTextTableLocale{
				Rows: []*contentv1.RichTextTableRowLocale{{
					RowId: rowID.String(), Cells: []*contentv1.RichTextTableCellLocale{
						{CellId: firstCellID.String(), Content: providerTargetInline(firstText)},
						{CellId: secondCellID.String(), Content: providerTargetInline(secondText)},
					},
				}},
			},
		}},
	}
}

func providerTargetSingleCellTable(
	blockID, rowID, cellID uuid.UUID,
	text string,
) *contentv1.RichTextBlockLocale {
	return &contentv1.RichTextBlockLocale{
		BlockId: blockID.String(),
		Value: &contentv1.RichTextBlockLocale_Table{Table: &contentv1.TableBlockLocale{
			Props: &contentv1.TableLocaleProps{}, Content: &contentv1.RichTextTableLocale{
				Rows: []*contentv1.RichTextTableRowLocale{{
					RowId: rowID.String(), Cells: []*contentv1.RichTextTableCellLocale{{
						CellId: cellID.String(), Content: providerTargetInline(text),
					}},
				}},
			},
		}},
	}
}

func providerTargetInline(text string) []*contentv1.RichTextInline {
	return []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
		Text: &contentv1.RichTextStyledText{Text: text},
	}}}
}
