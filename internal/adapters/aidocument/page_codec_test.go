package aidocumentadapter

import (
	"context"
	"sort"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	pagedomain "github.com/echovisionlab/geul-api/internal/page"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestPageCodecProjectsGeneratedSectionAndNestedRichTextHandles(t *testing.T) {
	codec, err := NewPageCodec()
	require.NoError(t, err)
	sectionID, paragraphID := uuid.NewString(), uuid.NewString()
	paragraph := &contentv1.RichTextBlockNode{
		Block: &contentv1.RichTextBlock{
			Id: paragraphID,
			Value: &contentv1.RichTextBlock_Paragraph{
				Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}},
			},
		},
		Placement: &contentv1.ContentBlockPlacement{},
	}
	section := &contentv1.PageSectionNode{
		Section: &contentv1.PageSection{
			Id:       sectionID,
			Settings: &contentv1.PageSectionSettings{},
			Value: &contentv1.PageSection_RichText{RichText: &contentv1.RichTextSection{
				Props:  &contentv1.RichTextSectionProps{},
				Blocks: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{paragraph}},
			}},
		},
		Placement: &contentv1.PageSectionPlacement{},
	}
	document := &contentv1.LocalizedPageDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Locale: "ko",
		Base:          &contentv1.PageSectionGraph{Nodes: []*contentv1.PageSectionNode{section}},
		LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: "ko"},
	}

	nodes, err := codec.Project(document)
	require.NoError(t, err)
	require.Len(t, nodes, 2)
	require.Equal(t, core.BlockID(sectionID), nodes[0].ID)
	require.Equal(t, core.BlockID(paragraphID), nodes[1].ID)
	require.Equal(t, core.BlockID(sectionID), nodes[1].Parent)
	require.Empty(t, nodes[0].Localized, "missing target section must stay absent")
	require.Empty(t, nodes[1].Localized, "missing target Block must stay absent")
}

func TestPageCodecProjectsColumnsAsStableContainerHandles(t *testing.T) {
	codec, err := NewPageCodec()
	require.NoError(t, err)
	columnsID, columnAID, columnBID, childID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	parent, column := columnsID, columnBID
	document := &contentv1.LocalizedPageDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Locale: "en",
		Base: &contentv1.PageSectionGraph{Nodes: []*contentv1.PageSectionNode{
			{Section: &contentv1.PageSection{Id: columnsID, Settings: &contentv1.PageSectionSettings{}, Value: &contentv1.PageSection_Columns{Columns: &contentv1.ColumnsSection{Props: &contentv1.ColumnsSectionProps{Columns: []*contentv1.ColumnsSectionProps_ColumnsItem{
				{Id: columnAID, Ratio: 1}, {Id: columnBID, Ratio: 2},
			}}}}}, Placement: &contentv1.PageSectionPlacement{}},
			{Section: &contentv1.PageSection{Id: childID, Settings: &contentv1.PageSectionSettings{}, Value: &contentv1.PageSection_ExternalVideo{ExternalVideo: &contentv1.ExternalVideoSection{Props: &contentv1.ExternalVideoSectionProps{Uri: "https://youtu.be/example"}}}}, Placement: &contentv1.PageSectionPlacement{ParentSectionId: &parent, ColumnId: &column}},
		}},
		LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: "en"},
	}
	nodes, err := codec.Project(document)
	require.NoError(t, err)
	require.Len(t, nodes, 4)
	byID := make(map[core.BlockID]core.Node, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
	}
	require.Equal(t, pageColumnBlockKind, byID[core.BlockID(columnBID)].Kind)
	require.Equal(t, core.BlockID(columnsID), byID[core.BlockID(columnBID)].Parent)
	require.Equal(t, core.Number("2"), byID[core.BlockID(columnBID)].Shared[0].Value)
	require.Equal(t, core.BlockID(columnBID), byID[core.BlockID(childID)].Parent)
}

func TestPageCodecCompilesSectionMoveByStableColumnHandle(t *testing.T) {
	codec, err := NewPageCodec()
	require.NoError(t, err)
	columnsID, columnAID, columnBID, childID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	parent, column := columnsID, columnAID
	document := &contentv1.LocalizedPageDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Locale: "en",
		Base: &contentv1.PageSectionGraph{Nodes: []*contentv1.PageSectionNode{
			{Section: &contentv1.PageSection{Id: columnsID, Settings: &contentv1.PageSectionSettings{}, Value: &contentv1.PageSection_Columns{Columns: &contentv1.ColumnsSection{Props: &contentv1.ColumnsSectionProps{Columns: []*contentv1.ColumnsSectionProps_ColumnsItem{
				{Id: columnAID, Ratio: 1}, {Id: columnBID, Ratio: 1},
			}}}}}, Placement: &contentv1.PageSectionPlacement{}},
			{Section: &contentv1.PageSection{Id: childID, Settings: &contentv1.PageSectionSettings{}, Value: &contentv1.PageSection_ExternalVideo{ExternalVideo: &contentv1.ExternalVideoSection{Props: &contentv1.ExternalVideoSectionProps{Uri: "https://youtu.be/example"}}}}, Placement: &contentv1.PageSectionPlacement{ParentSectionId: &parent, ColumnId: &column}},
		}},
		LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: "en", Sections: []*contentv1.PageSectionLocale{
			{SectionId: columnsID, Value: &contentv1.PageSectionLocale_Columns{Columns: &contentv1.ColumnsSectionLocale{Props: &contentv1.ColumnsSectionLocaleProps{}}}},
			{SectionId: childID, Value: &contentv1.PageSectionLocale_ExternalVideo{ExternalVideo: &contentv1.ExternalVideoSectionLocale{Props: &contentv1.ExternalVideoSectionLocaleProps{}}}},
		}},
	}
	batch, issues, err := codec.Compile(uuid.New(), document, core.LocaleRoleSource, core.Revision(uuid.NewString()), uuid.New(), []core.Operation{
		core.MoveBlockOperation(core.BlockID(childID), core.BlockID(columnBID), ""),
	})
	require.NoError(t, err)
	require.Empty(t, issues)
	var moved *contentblock.BaseBlock
	for index := range batch.Upserts {
		if batch.Upserts[index].ID.String() == childID {
			moved = &batch.Upserts[index]
		}
	}
	require.NotNil(t, moved)
	require.Equal(t, "column-"+columnBID, moved.ContainerSlot)
	require.Equal(t, columnsID, moved.ParentID.String())
}

func TestNewPageRegistrationRejectsMissingDomainService(t *testing.T) {
	_, err := NewPageRegistration(nil)
	require.ErrorContains(t, err, "dependencies are required")
}

func TestCompilePageMetadataPreservesExplicitEmptyAndRejectsUnset(t *testing.T) {
	patch := pagedomain.AIDocumentMetadataPatch{}
	handled, issue := compilePageMetadataOperation(&patch, core.SetFieldOperation(pageMetadataBlockID, pageTitleField, core.Text("")), 0)
	require.True(t, handled)
	require.Nil(t, issue)
	require.True(t, patch.SetTitle)
	require.NotNil(t, patch.Title)
	require.Empty(t, *patch.Title)

	handled, issue = compilePageMetadataOperation(&patch, core.UnsetFieldOperation(pageMetadataBlockID, pageTitleField), 1)
	require.True(t, handled)
	require.NotNil(t, issue)
	require.Equal(t, core.IssueInvalidOperation, issue.Code)
}

func TestPageCodecCompilesTargetExplicitEmptyWithoutBaseMutation(t *testing.T) {
	codec, err := NewPageCodec()
	require.NoError(t, err)
	sectionID := uuid.NewString()
	document := &contentv1.LocalizedPageDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Locale: "en",
		Base: &contentv1.PageSectionGraph{Nodes: []*contentv1.PageSectionNode{{
			Section:   &contentv1.PageSection{Id: sectionID, Settings: &contentv1.PageSectionSettings{}, Value: &contentv1.PageSection_ExternalVideo{ExternalVideo: &contentv1.ExternalVideoSection{Props: &contentv1.ExternalVideoSectionProps{Uri: "https://youtu.be/example"}}}},
			Placement: &contentv1.PageSectionPlacement{},
		}}},
		LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: "en"},
	}
	batch, issues, err := codec.Compile(uuid.New(), document, core.LocaleRoleNonSource, core.Revision(uuid.NewString()), uuid.New(), []core.Operation{
		core.SetNestedFieldOperation(core.BlockID(sectionID), pageSectionLocaleField, []core.FieldPathSegment{core.ObjectPath("props"), core.ObjectPath("caption")}, core.Text("")),
	})
	require.NoError(t, err)
	require.Empty(t, issues)
	require.Empty(t, batch.Upserts)
	require.Len(t, batch.LocaleGroups, 1)
	require.Len(t, batch.LocaleGroups[0].Upserts, 1)
	require.Contains(t, string(batch.LocaleGroups[0].Upserts[0].LocalizedData), `"caption":""`)
}

func TestPageCodecCompilesNestedRichTextThroughGeneratedPageMutations(t *testing.T) {
	codec, err := NewPageCodec()
	require.NoError(t, err)
	sectionID, firstID, secondID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	document := &contentv1.LocalizedPageDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Locale: "en",
		Base: &contentv1.PageSectionGraph{Nodes: []*contentv1.PageSectionNode{{
			Section: &contentv1.PageSection{Id: sectionID, Settings: &contentv1.PageSectionSettings{}, Value: &contentv1.PageSection_RichText{RichText: &contentv1.RichTextSection{
				Props: &contentv1.RichTextSectionProps{}, Blocks: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
					Block: &contentv1.RichTextBlock{Id: firstID, Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}}}}, Placement: &contentv1.ContentBlockPlacement{},
				}}},
			}}}, Placement: &contentv1.PageSectionPlacement{},
		}}},
		LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: "en", Sections: []*contentv1.PageSectionLocale{{
			SectionId: sectionID, Value: &contentv1.PageSectionLocale_RichText{RichText: &contentv1.RichTextSectionLocale{
				Props: &contentv1.RichTextSectionLocaleProps{}, Blocks: &contentv1.RichTextLocaleOverlay{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{{
					BlockId: firstID, Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{Props: &contentv1.ParagraphLocaleProps{}}},
				}}},
			}},
		}}},
	}
	batch, issues, err := codec.Compile(uuid.New(), document, core.LocaleRoleSource, core.Revision(uuid.NewString()), uuid.New(), []core.Operation{
		core.InsertBlockOperation(core.BlockID(secondID), "paragraph", core.BlockID(sectionID), core.BlockID(firstID)),
	})
	require.NoError(t, err)
	require.Empty(t, issues)
	require.Len(t, batch.Upserts, 3, "one section and two nested Rich Text Blocks are one generated batch")
	require.Len(t, batch.LocaleGroups, 1)
	require.Len(t, batch.LocaleGroups[0].Upserts, 3)
	for _, localized := range batch.LocaleGroups[0].Upserts {
		if localized.BlockID.String() == secondID {
			require.Contains(t, string(localized.LocalizedData), `"content":[]`)
			return
		}
	}
	t.Fatal("inserted nested empty Paragraph locale upsert missing")
}

func TestPageCompileDoesNotCreateMissingTargetForUnset(t *testing.T) {
	codec, err := NewPageCodec()
	require.NoError(t, err)
	sectionID := uuid.NewString()
	revision := uuid.New()
	document := &contentv1.LocalizedPageDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Locale: "ko",
		Base: &contentv1.PageSectionGraph{Nodes: []*contentv1.PageSectionNode{{
			Section:   &contentv1.PageSection{Id: sectionID, Settings: &contentv1.PageSectionSettings{}, Value: &contentv1.PageSection_ExternalVideo{ExternalVideo: &contentv1.ExternalVideoSection{Props: &contentv1.ExternalVideoSectionProps{Uri: "https://youtu.be/example"}}}},
			Placement: &contentv1.PageSectionPlacement{},
		}}},
		LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: "ko"},
	}
	state := pagedomain.AIDocumentState{
		SourceLocale: "en", Locale: "ko", LocaleExists: false,
		Snapshot: contentblock.Snapshot{Document: contentblock.Document{ID: uuid.New(), Revision: revision}},
		Document: document,
	}
	loaded := core.Document{
		Identity:         core.DocumentIdentity{Domain: core.DomainPage, Reference: core.DocumentReference(uuid.NewString())},
		DocumentRevision: core.Revision(revision.String()), SourceLocale: "en", Locale: "ko", LocaleExists: false,
	}
	port := &pagePort{codec: codec}
	mutation, issues, err := port.compile(state, uuid.New(), loaded, []core.Operation{
		core.UnsetNestedFieldOperation(core.BlockID(sectionID), pageSectionLocaleField, []core.FieldPathSegment{core.ObjectPath("props"), core.ObjectPath("caption")}),
	})
	require.NoError(t, err)
	require.Empty(t, issues)
	require.False(t, mutation.Metadata.EnsureLocale)
	require.Empty(t, mutation.Batch.LocaleGroups)
	require.Empty(t, mutation.Batch.Upserts)
	require.Empty(t, mutation.Batch.Deletes)
}

func TestPageNestedRichTextUnsetDoesNotCreateMissingTarget(t *testing.T) {
	codec, err := NewPageCodec()
	require.NoError(t, err)
	sectionID, paragraphID := uuid.NewString(), uuid.NewString()
	document := &contentv1.LocalizedPageDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Locale: "ko",
		Base: &contentv1.PageSectionGraph{Nodes: []*contentv1.PageSectionNode{{
			Section: &contentv1.PageSection{Id: sectionID, Settings: &contentv1.PageSectionSettings{}, Value: &contentv1.PageSection_RichText{RichText: &contentv1.RichTextSection{
				Props: &contentv1.RichTextSectionProps{}, Blocks: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
					Block: &contentv1.RichTextBlock{Id: paragraphID, Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}}}}, Placement: &contentv1.ContentBlockPlacement{},
				}}},
			}}}, Placement: &contentv1.PageSectionPlacement{},
		}}},
		LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: "ko"},
	}
	batch, issues, err := codec.Compile(uuid.New(), document, core.LocaleRoleNonSource, core.Revision(uuid.NewString()), uuid.New(), []core.Operation{
		core.UnsetFieldOperation(core.BlockID(paragraphID), "content"),
	})
	require.NoError(t, err)
	require.Empty(t, issues)
	require.Empty(t, batch.LocaleGroups)
	require.Empty(t, batch.Upserts)
}

func TestPagePortProjectionSatisfiesCompactDocumentCatalog(t *testing.T) {
	codec, err := NewPageCodec()
	require.NoError(t, err)
	port := &pagePort{codec: codec, catalog: func() core.Catalog {
		catalog := codec.Catalog()
		catalog.BlockKinds = append(catalog.BlockKinds, pageMetadataBlockKind)
		catalog.Fields = append(catalog.Fields,
			core.FieldRule{BlockKind: pageMetadataBlockKind, Field: pageTitleField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
			core.FieldRule{BlockKind: pageMetadataBlockKind, Field: pageSummaryField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
		)
		return catalog
	}()}
	identity := core.DocumentIdentity{Domain: core.DomainPage, Reference: core.DocumentReference(uuid.NewString())}
	sectionID := uuid.NewString()
	document, err := port.project(identity, "en", pageStateForCodecTest(sectionID))
	require.NoError(t, err)
	service, err := core.NewService(&registryPort{document: document})
	require.NoError(t, err)
	_, err = service.Open(context.Background(), core.OpenRequest{Document: identity, Locale: "en"})
	require.NoError(t, err)
}

func TestPagePortProjectionOmitsEmptyGeneratedObjectWrappers(t *testing.T) {
	codec, err := NewPageCodec()
	require.NoError(t, err)
	port := &pagePort{codec: codec, catalog: func() core.Catalog {
		catalog := codec.Catalog()
		catalog.BlockKinds = append(catalog.BlockKinds, pageMetadataBlockKind)
		catalog.Fields = append(catalog.Fields,
			core.FieldRule{BlockKind: pageMetadataBlockKind, Field: pageTitleField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
			core.FieldRule{BlockKind: pageMetadataBlockKind, Field: pageSummaryField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
		)
		return catalog
	}()}
	immersiveUnitID := uuid.NewString()
	tests := []struct {
		name    string
		section *contentv1.PageSection
		locale  *contentv1.PageSectionLocale
	}{
		{
			name: "immersive scene locale props",
			section: &contentv1.PageSection{Value: &contentv1.PageSection_ImmersiveScene{ImmersiveScene: &contentv1.ImmersiveSceneSection{
				Props: &contentv1.ImmersiveSceneSectionProps{},
				Units: []*contentv1.PageImmersiveUnit{{Id: immersiveUnitID, Props: &contentv1.PageImmersiveUnitProps{}}},
			}}},
			locale: &contentv1.PageSectionLocale{Value: &contentv1.PageSectionLocale_ImmersiveScene{ImmersiveScene: &contentv1.ImmersiveSceneSectionLocale{
				Props: &contentv1.ImmersiveSceneSectionLocaleProps{},
				Units: []*contentv1.PageImmersiveUnitLocale{{UnitId: immersiveUnitID, Props: &contentv1.PageImmersiveUnitLocaleProps{}}},
			}}},
		},
		{
			name: "work map locale props",
			section: &contentv1.PageSection{Value: &contentv1.PageSection_WorkMap{WorkMap: &contentv1.WorkMapSection{
				Props: &contentv1.WorkMapSectionProps{},
			}}},
			locale: &contentv1.PageSectionLocale{Value: &contentv1.PageSectionLocale_WorkMap{WorkMap: &contentv1.WorkMapSectionLocale{
				Props: &contentv1.WorkMapSectionLocaleProps{},
			}}},
		},
		{
			name: "rich text source props",
			section: &contentv1.PageSection{Value: &contentv1.PageSection_RichText{RichText: &contentv1.RichTextSection{
				Props: &contentv1.RichTextSectionProps{}, Blocks: &contentv1.RichTextBlockGraph{},
			}}},
			locale: &contentv1.PageSectionLocale{Value: &contentv1.PageSectionLocale_RichText{RichText: &contentv1.RichTextSectionLocale{
				Props: &contentv1.RichTextSectionLocaleProps{}, Blocks: &contentv1.RichTextLocaleOverlay{Locale: "en"},
			}}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sectionID := uuid.NewString()
			test.section.Id = sectionID
			test.section.Settings = &contentv1.PageSectionSettings{}
			test.locale.SectionId = sectionID
			revision := uuid.New()
			title := "Page"
			state := pagedomain.AIDocumentState{
				Revision: revision.String(), SourceLocale: "en", Locale: "en", LocaleExists: true, Title: &title,
				Snapshot: contentblock.Snapshot{Document: contentblock.Document{ID: uuid.New(), Revision: revision}},
				Document: &contentv1.LocalizedPageDocument{
					BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Locale: "en",
					Base: &contentv1.PageSectionGraph{Nodes: []*contentv1.PageSectionNode{{
						Section: test.section, Placement: &contentv1.PageSectionPlacement{},
					}}},
					LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: "en", Sections: []*contentv1.PageSectionLocale{test.locale}},
				},
			}
			identity := core.DocumentIdentity{Domain: core.DomainPage, Reference: core.DocumentReference(uuid.NewString())}
			document, err := port.project(identity, "en", state)
			require.NoError(t, err)
			service, err := core.NewService(&registryPort{document: document})
			require.NoError(t, err)
			_, err = service.Open(t.Context(), core.OpenRequest{Document: identity, Locale: "en"})
			require.NoError(t, err)
		})
	}
}

func pageStateForCodecTest(sectionID string) pagedomain.AIDocumentState {
	title := "Page"
	revision := uuid.New()
	return pagedomain.AIDocumentState{
		Revision: revision.String(), SourceLocale: "en", Locale: "en", LocaleExists: true, Title: &title,
		Snapshot: contentblock.Snapshot{Document: contentblock.Document{ID: uuid.New(), Revision: revision}},
		Document: &contentv1.LocalizedPageDocument{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Locale: "en",
			Base: &contentv1.PageSectionGraph{Nodes: []*contentv1.PageSectionNode{{
				Section:   &contentv1.PageSection{Id: sectionID, Settings: &contentv1.PageSectionSettings{}, Value: &contentv1.PageSection_ExternalVideo{ExternalVideo: &contentv1.ExternalVideoSection{Props: &contentv1.ExternalVideoSectionProps{Uri: "https://youtu.be/example"}}}},
				Placement: &contentv1.PageSectionPlacement{},
			}}},
			LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: "en", Sections: []*contentv1.PageSectionLocale{{
				SectionId: sectionID, Value: &contentv1.PageSectionLocale_ExternalVideo{ExternalVideo: &contentv1.ExternalVideoSectionLocale{Props: &contentv1.ExternalVideoSectionLocaleProps{}}},
			}}},
		},
	}
}

func TestPageCodecCanonicalEnumsAndExactRecursiveMutationParity(t *testing.T) {
	codec, err := NewPageCodec()
	require.NoError(t, err)
	textureSize := findMessageFieldDescriptor((&contentv1.ImmersiveSceneSectionProps{}).ProtoReflect().Descriptor(), "textureSize")
	require.NotNil(t, textureSize)
	require.Equal(t, core.ValueKindNumber, mustPageFieldSchema(t, textureSize).Kind)
	textureValue, err := pageScalarValue(textureSize, core.Number("64"))
	require.NoError(t, err)
	require.Equal(t, contentv1.ImmersiveSceneSectionProps_TEXTURE_SIZE_64, contentv1.ImmersiveSceneSectionProps_TextureSize(textureValue.Enum()))
	projectedTexture, _, err := pageProjectSingular(textureSize, textureValue)
	require.NoError(t, err)
	require.Equal(t, core.Number("64"), projectedTexture)
	sectionID := uuid.NewString()
	ratio := contentv1.ExternalVideoSectionProps_ASPECT_RATIO_X_16_9
	section := &contentv1.PageSectionNode{
		Section: &contentv1.PageSection{
			Id: sectionID, Settings: &contentv1.PageSectionSettings{},
			Value: &contentv1.PageSection_ExternalVideo{ExternalVideo: &contentv1.ExternalVideoSection{Props: &contentv1.ExternalVideoSectionProps{
				Uri: "https://example.com/video", AspectRatio: &ratio,
			}}},
		},
		Placement: &contentv1.PageSectionPlacement{},
	}
	document := &contentv1.LocalizedPageDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Locale:                  "en",
		Base:                    &contentv1.PageSectionGraph{Nodes: []*contentv1.PageSectionNode{section}},
		LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: "en", Sections: []*contentv1.PageSectionLocale{{
			SectionId: sectionID,
			Value: &contentv1.PageSectionLocale_ExternalVideo{ExternalVideo: &contentv1.ExternalVideoSectionLocale{
				Props: &contentv1.ExternalVideoSectionLocaleProps{},
			}},
		}}},
	}

	nodes, err := codec.Project(document)
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	data := pageTestNodeField(t, nodes[0].Shared, pageSectionDataField)
	props := pageTestObjectField(t, data.Object, "props")
	require.Equal(t, core.Text("16:9"), pageTestObjectField(t, props.Object, "aspectRatio"))

	require.NoError(t, codec.setPageField(document, core.LocaleRoleSource, core.FieldTarget{
		Block: core.BlockID(sectionID), Field: pageSectionDataField,
		Path: []core.FieldPathSegment{core.ObjectPath("props"), core.ObjectPath("aspectRatio")},
	}, core.Text("auto")))
	require.Equal(t, contentv1.ExternalVideoSectionProps_ASPECT_RATIO_AUTO, document.GetBase().GetNodes()[0].GetSection().GetExternalVideo().GetProps().GetAspectRatio())

	require.NoError(t, codec.setPageField(document, core.LocaleRoleSource, core.FieldTarget{
		Block: core.BlockID(sectionID), Field: pageSectionDataField,
		Path: []core.FieldPathSegment{core.ObjectPath("props")},
	}, core.Object(core.ObjectValue("uri", core.Text("https://example.com/replacement")))))
	require.Equal(t, "https://example.com/replacement", document.GetBase().GetNodes()[0].GetSection().GetExternalVideo().GetProps().GetUri())
	require.Nil(t, document.GetBase().GetNodes()[0].GetSection().GetExternalVideo().GetProps().AspectRatio, "object set is exact replacement")

	before := proto.Clone(document)
	batch, issues, err := codec.Compile(uuid.New(), document, core.LocaleRoleNonSource, core.Revision(uuid.NewString()), uuid.New(), []core.Operation{
		core.SetNestedFieldOperation(core.BlockID(sectionID), pageSectionLocaleField, []core.FieldPathSegment{core.ObjectPath("props"), core.ObjectPath("caption")}, core.Text("first")),
		core.SetNestedFieldOperation(core.BlockID(sectionID), pageSectionLocaleField, []core.FieldPathSegment{core.ObjectPath("props")}, core.Object(core.ObjectValue("caption", core.Text("invalid composite")))),
	})
	require.NoError(t, err)
	require.Len(t, issues, 1)
	require.Equal(t, 1, issues[0].Operation)
	require.Empty(t, batch.Upserts)
	require.Empty(t, batch.LocaleGroups)
	require.True(t, proto.Equal(before, document), "failed fallback batch must not mutate its input")
}

func mustPageFieldSchema(t *testing.T, field protoreflect.FieldDescriptor) core.FieldSchema {
	t.Helper()
	schema, err := pageFieldSchema(field, core.FieldOwnershipShared)
	require.NoError(t, err)
	return schema
}

func TestPageCodecStableImmersiveTargetAndSourceTopologyParity(t *testing.T) {
	codec, err := NewPageCodec()
	require.NoError(t, err)
	sectionID, firstID, secondID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	oldTitle := "old"
	document := &contentv1.LocalizedPageDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Locale: "en",
		Base: &contentv1.PageSectionGraph{Nodes: []*contentv1.PageSectionNode{{
			Section: &contentv1.PageSection{Id: sectionID, Settings: &contentv1.PageSectionSettings{}, Value: &contentv1.PageSection_ImmersiveScene{ImmersiveScene: &contentv1.ImmersiveSceneSection{
				Props: &contentv1.ImmersiveSceneSectionProps{}, Units: []*contentv1.PageImmersiveUnit{{Id: firstID, Props: &contentv1.PageImmersiveUnitProps{}}},
			}}}, Placement: &contentv1.PageSectionPlacement{},
		}}},
		LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: "en", Sections: []*contentv1.PageSectionLocale{{
			SectionId: sectionID, Value: &contentv1.PageSectionLocale_ImmersiveScene{ImmersiveScene: &contentv1.ImmersiveSceneSectionLocale{
				Props: &contentv1.ImmersiveSceneSectionLocaleProps{}, Units: []*contentv1.PageImmersiveUnitLocale{{UnitId: firstID, Props: &contentv1.PageImmersiveUnitLocaleProps{Title: &oldTitle}}},
			}},
		}}},
	}
	titleTarget := core.FieldTarget{
		Block: core.BlockID(sectionID), Field: pageSectionLocaleField,
		Path: []core.FieldPathSegment{core.ObjectPath("units"), core.ListPath(core.RelationItemID(firstID)), core.ObjectPath("props"), core.ObjectPath("title")},
	}
	require.NoError(t, codec.setPageField(document, core.LocaleRoleNonSource, titleTarget, core.Text("translated")))
	require.Equal(t, "translated", document.GetLocaleOverlay().GetSections()[0].GetImmersiveScene().GetUnits()[0].GetProps().GetTitle())
	require.ErrorContains(t, codec.setPageField(document, core.LocaleRoleNonSource, core.FieldTarget{
		Block: core.BlockID(sectionID), Field: pageSectionLocaleField,
		Path: []core.FieldPathSegment{core.ObjectPath("units")},
	}, core.List()), "scalar leaf")
	missing := titleTarget
	missing.Path[1] = core.ListPath(core.RelationItemID(secondID))
	require.ErrorContains(t, codec.setPageField(document, core.LocaleRoleNonSource, missing, core.Text("missing")), "does not exist")

	require.NoError(t, codec.setPageField(document, core.LocaleRoleSource, core.FieldTarget{
		Block: core.BlockID(sectionID), Field: pageSectionDataField,
		Path: []core.FieldPathSegment{core.ObjectPath("units")},
	}, core.List(
		core.StableItem(core.RelationItemID(secondID), core.Object(
			core.ObjectValue("id", core.Text(secondID)), core.ObjectValue("props", core.Object()),
		)),
		core.StableItem(core.RelationItemID(firstID), core.Object(
			core.ObjectValue("id", core.Text(firstID)), core.ObjectValue("props", core.Object()),
		)),
	)))
	units := document.GetLocaleOverlay().GetSections()[0].GetImmersiveScene().GetUnits()
	require.Equal(t, []string{secondID, firstID}, []string{units[0].GetUnitId(), units[1].GetUnitId()})
	require.Nil(t, units[0].GetProps())
	require.Equal(t, "translated", units[1].GetProps().GetTitle())
}

func TestPageCodecRejectsGenericColumnsAndNonEmptyKindReplacementWithoutMutation(t *testing.T) {
	codec, err := NewPageCodec()
	require.NoError(t, err)
	columnsID, columnID := uuid.NewString(), uuid.NewString()
	document := &contentv1.LocalizedPageDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Locale: "en",
		Base: &contentv1.PageSectionGraph{Nodes: []*contentv1.PageSectionNode{{
			Section: &contentv1.PageSection{Id: columnsID, Settings: &contentv1.PageSectionSettings{}, Value: &contentv1.PageSection_Columns{Columns: &contentv1.ColumnsSection{Props: &contentv1.ColumnsSectionProps{
				Columns: []*contentv1.ColumnsSectionProps_ColumnsItem{{Id: columnID, Ratio: 1}},
			}}}}, Placement: &contentv1.PageSectionPlacement{},
		}}},
		LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: "en", Sections: []*contentv1.PageSectionLocale{{
			SectionId: columnsID, Value: &contentv1.PageSectionLocale_Columns{Columns: &contentv1.ColumnsSectionLocale{Props: &contentv1.ColumnsSectionLocaleProps{}}},
		}}},
	}
	nodes, err := codec.Project(document)
	require.NoError(t, err)
	columns := nodes[0]
	for _, field := range columns.Shared {
		require.NotEqual(t, pageSectionDataField, field.ID, "synthetic columns must not leak through generic data")
	}
	before := proto.Clone(document)
	require.ErrorContains(t, codec.replacePageSection(document, &core.ReplaceBlockKind{Block: core.BlockID(columnsID), Kind: "external-video"}), "empty")
	require.True(t, proto.Equal(before, document))
	batch, issues, err := codec.Compile(uuid.New(), document, core.LocaleRoleNonSource, core.Revision(uuid.NewString()), uuid.New(), []core.Operation{
		core.ReplaceBlockKindOperation(core.BlockID(columnsID), "external-video"),
	})
	require.NoError(t, err)
	require.Len(t, issues, 1)
	require.Contains(t, issues[0].Message, "topology")
	require.Empty(t, batch.Upserts)
	require.True(t, proto.Equal(before, document))
}

func pageTestObjectField(t *testing.T, fields []core.ObjectField, id core.FieldID) core.Value {
	t.Helper()
	for _, field := range fields {
		if field.ID == id {
			return field.Value
		}
	}
	t.Fatalf("field %q not found", id)
	return core.Value{}
}

func pageTestNodeField(t *testing.T, fields []core.FieldValue, id core.FieldID) core.Value {
	t.Helper()
	for _, field := range fields {
		if field.ID == id {
			return field.Value
		}
	}
	t.Fatalf("field %q not found", id)
	return core.Value{}
}

func TestPageCodecSeparatesMermaidSectionsFromNestedRichTextBlocks(t *testing.T) {
	codec, err := NewPageCodec()
	require.NoError(t, err)
	seen := make(map[core.BlockKind]bool)
	for _, kind := range codec.Catalog().BlockKinds {
		require.False(t, seen[kind], "duplicate catalog kind %q", kind)
		seen[kind] = true
	}
	require.True(t, seen["mermaid"])
	require.True(t, seen["page-mermaid"])

	sectionID, richID, blockID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	block := &contentv1.RichTextBlockNode{
		Block: &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_Mermaid{
			Mermaid: &contentv1.MermaidBlock{Props: &contentv1.MermaidProps{Source: proto.String("graph LR; A-->B")}},
		}}, Placement: &contentv1.ContentBlockPlacement{},
	}
	document := &contentv1.LocalizedPageDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Locale: "en",
		Base: &contentv1.PageSectionGraph{Nodes: []*contentv1.PageSectionNode{
			{Section: &contentv1.PageSection{Id: sectionID, Settings: &contentv1.PageSectionSettings{}, Value: &contentv1.PageSection_Mermaid{
				Mermaid: &contentv1.MermaidSection{Props: &contentv1.MermaidSectionProps{Source: proto.String("graph LR; C-->D")}},
			}}, Placement: &contentv1.PageSectionPlacement{}},
			{Section: &contentv1.PageSection{Id: richID, Settings: &contentv1.PageSectionSettings{}, Value: &contentv1.PageSection_RichText{
				RichText: &contentv1.RichTextSection{Props: &contentv1.RichTextSectionProps{}, Blocks: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{block}}},
			}}, Placement: &contentv1.PageSectionPlacement{Index: 1}},
		}},
		LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: "en"},
	}
	nodes, err := codec.Project(document)
	require.NoError(t, err)
	require.Len(t, nodes, 3)
	require.Equal(t, core.BlockKind("page-mermaid"), nodes[0].Kind)
	require.Equal(t, core.BlockKind("mermaid"), nodes[2].Kind)
	require.Equal(t, core.BlockID(richID), nodes[2].Parent)

	for _, operation := range []core.Operation{
		core.SetNestedFieldOperation(core.BlockID(sectionID), pageSectionDataField,
			[]core.FieldPathSegment{core.ObjectPath("props"), core.ObjectPath("source")}, core.Text("graph LR; E-->F")),
		core.SetFieldOperation(core.BlockID(blockID), "source", core.Text("graph LR; G-->H")),
	} {
		require.NoError(t, codec.applyPageOperation(document, core.LocaleRoleSource, operation, make(map[string]struct{})))
	}
	require.Equal(t, "graph LR; E-->F", document.Base.Nodes[0].Section.GetMermaid().GetProps().GetSource())
	require.Equal(t, "graph LR; G-->H", document.Base.Nodes[1].Section.GetRichText().Blocks.Nodes[0].Block.GetMermaid().GetProps().GetSource())
}

func pageRichTextDocumentForTest(t *testing.T, count int) (*PageCodec, *contentv1.LocalizedPageDocument, string, []string) {
	t.Helper()
	codec, err := NewPageCodec()
	require.NoError(t, err)
	sectionID := uuid.NewString()
	section := &contentv1.PageSectionNode{Section: &contentv1.PageSection{Id: sectionID, Settings: &contentv1.PageSectionSettings{}, Value: &contentv1.PageSection_RichText{RichText: &contentv1.RichTextSection{Props: &contentv1.RichTextSectionProps{}, Blocks: &contentv1.RichTextBlockGraph{}}}}, Placement: &contentv1.PageSectionPlacement{}}
	localized := &contentv1.PageSectionLocale{SectionId: sectionID, Value: &contentv1.PageSectionLocale_RichText{RichText: &contentv1.RichTextSectionLocale{Props: &contentv1.RichTextSectionLocaleProps{}, Blocks: &contentv1.RichTextLocaleOverlay{Locale: "en"}}}}
	ids := make([]string, count)
	for i := range ids {
		ids[i] = uuid.NewString()
		node, locale, err := codec.rich.newBlock("paragraph", ids[i])
		require.NoError(t, err)
		node.Placement = &contentv1.ContentBlockPlacement{Index: uint32(i)}
		section.Section.GetRichText().Blocks.Nodes = append(section.Section.GetRichText().Blocks.Nodes, node)
		localized.GetRichText().Blocks.Blocks = append(localized.GetRichText().Blocks.Blocks, locale)
	}
	return codec, &contentv1.LocalizedPageDocument{BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Locale: "en", Base: &contentv1.PageSectionGraph{Nodes: []*contentv1.PageSectionNode{section}}, LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: "en", Sections: []*contentv1.PageSectionLocale{localized}}}, sectionID, ids
}

func TestPageCodecRichTextRootMovesAndDeletesPersistDenseOrder(t *testing.T) {
	codec, document, sectionID, ids := pageRichTextDocumentForTest(t, 3)
	seed, issues, err := codec.Compile(uuid.New(), document, core.LocaleRoleSource, core.Revision(uuid.NewString()), uuid.New(), nil)
	require.NoError(t, err)
	require.Empty(t, issues)
	document, err = contentblock.SnapshotToLocalizedPageDocument(pageSnapshotFromBatch(seed), "en")
	require.NoError(t, err)
	// Storage does not promise that the node array is in placement order.
	sort.Slice(document.Base.Nodes[0].Section.GetRichText().Blocks.Nodes, func(i, j int) bool {
		return document.Base.Nodes[0].Section.GetRichText().Blocks.Nodes[i].Placement.Index > document.Base.Nodes[0].Section.GetRichText().Blocks.Nodes[j].Placement.Index
	})
	inserted := uuid.NewString()
	for _, test := range []struct {
		name       string
		operations []core.Operation
		want       []string
	}{
		{"first after last", []core.Operation{core.MoveBlockOperation(core.BlockID(ids[0]), core.BlockID(sectionID), core.BlockID(ids[2]))}, []string{ids[1], ids[2], ids[0]}},
		{"last first", []core.Operation{core.MoveBlockOperation(core.BlockID(ids[2]), core.BlockID(sectionID), "")}, []string{ids[2], ids[0], ids[1]}},
		{"delete first", []core.Operation{core.DeleteBlockOperation(core.BlockID(ids[0]))}, []string{ids[1], ids[2]}},
		{"delete middle", []core.Operation{core.DeleteBlockOperation(core.BlockID(ids[1]))}, []string{ids[0], ids[2]}},
		{"delete last", []core.Operation{core.DeleteBlockOperation(core.BlockID(ids[2]))}, []string{ids[0], ids[1]}},
		{"insert after actual last", []core.Operation{core.InsertBlockOperation(core.BlockID(inserted), "paragraph", core.BlockID(sectionID), core.BlockID(ids[2]))}, []string{ids[0], ids[1], ids[2], inserted}},
		{"move then delete", []core.Operation{core.MoveBlockOperation(core.BlockID(ids[0]), core.BlockID(sectionID), core.BlockID(ids[2])), core.DeleteBlockOperation(core.BlockID(ids[1]))}, []string{ids[2], ids[0]}},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := proto.Clone(document)
			batch, issues, err := codec.Compile(uuid.New(), document, core.LocaleRoleSource, core.Revision(uuid.NewString()), uuid.New(), test.operations)
			require.NoError(t, err)
			require.Empty(t, issues)
			require.True(t, proto.Equal(before, document))
			loaded, err := contentblock.SnapshotToLocalizedPageDocument(pageSnapshotFromBatch(batch), "en")
			require.NoError(t, err)
			children := loaded.Base.Nodes[0].Section.GetRichText().Blocks.Nodes
			require.Len(t, children, len(test.want))
			for _, node := range children {
				require.Less(t, int(node.Placement.Index), len(test.want))
				require.Equal(t, test.want[node.Placement.Index], node.Block.Id)
			}
		})
	}
}

func TestPageCodecOuterSectionDeletionReindexesRemainingSlots(t *testing.T) {
	codec, document, _, _ := pageRichTextDocumentForTest(t, 0)
	for index := 1; index < 3; index++ {
		_, next, _, _ := pageRichTextDocumentForTest(t, 0)
		next.Base.Nodes[0].Placement.Index = uint32(index)
		document.Base.Nodes = append(document.Base.Nodes, next.Base.Nodes[0])
		document.LocaleOverlay.Sections = append(document.LocaleOverlay.Sections, next.LocaleOverlay.Sections[0])
	}
	want := []string{document.Base.Nodes[1].Section.Id, document.Base.Nodes[2].Section.Id}
	deleted := document.Base.Nodes[0].Section.Id
	document.Base.Nodes[0], document.Base.Nodes[2] = document.Base.Nodes[2], document.Base.Nodes[0]
	batch, issues, err := codec.Compile(uuid.New(), document, core.LocaleRoleSource, core.Revision(uuid.NewString()), uuid.New(), []core.Operation{core.DeleteBlockOperation(core.BlockID(deleted))})
	require.NoError(t, err)
	require.Empty(t, issues)
	loaded, err := contentblock.SnapshotToLocalizedPageDocument(pageSnapshotFromBatch(batch), "en")
	require.NoError(t, err)
	require.Len(t, loaded.Base.Nodes, 2)
	for _, node := range loaded.Base.Nodes {
		require.Less(t, int(node.Placement.Index), len(want))
		require.Equal(t, want[node.Placement.Index], node.Section.Id)
	}
}

func TestPageCodecInsertedThenDeletedRichTextDoesNotRestoreRemovedLocaleValue(t *testing.T) {
	codec, document, sectionID, ids := pageRichTextDocumentForTest(t, 1)
	db, store, created := newCodecStoreForTest(t, "page")
	seed, issues, err := codec.Compile(created.Document.ID, document, core.LocaleRoleSource, core.Revision(created.Document.Revision.String()), uuid.New(), nil)
	require.NoError(t, err)
	require.Empty(t, issues)
	before := persistCodecBatchForTest(t, db, store, seed)
	document, err = contentblock.SnapshotToLocalizedPageDocument(before, "en")
	require.NoError(t, err)
	inserted := core.BlockID(uuid.NewString())
	for _, operations := range [][]core.Operation{
		{
			core.InsertBlockOperation(inserted, "paragraph", core.BlockID(sectionID), core.BlockID(ids[0])),
			core.SetFieldOperation(inserted, "content", core.RichText(core.InlineText("temporary"))),
			core.DeleteBlockOperation(inserted),
		},
		{
			core.InsertBlockOperation(inserted, "external-video", "", core.BlockID(sectionID)),
			core.SetNestedFieldOperation(inserted, pageSectionLocaleField, []core.FieldPathSegment{core.ObjectPath("props"), core.ObjectPath("caption")}, core.Text("temporary")),
			core.DeleteBlockOperation(inserted),
		},
	} {
		batch, issues, err := codec.Compile(created.Document.ID, document, core.LocaleRoleSource, core.Revision(before.Document.Revision.String()), uuid.New(), operations)
		require.NoError(t, err)
		require.Empty(t, issues)
		after := persistCodecBatchForTest(t, db, store, batch)
		require.Equal(t, before.Document.Revision, after.Document.Revision, "the temporary block is an aggregate no-op")
		loaded, err := contentblock.SnapshotToLocalizedPageDocument(after, "en")
		require.NoError(t, err)
		require.Len(t, loaded.Base.Nodes, 1)
		require.Len(t, loaded.Base.Nodes[0].Section.GetRichText().Blocks.Nodes, 1)
		require.Equal(t, ids[0], loaded.Base.Nodes[0].Section.GetRichText().Blocks.Nodes[0].Block.Id)
	}
}

func pageSnapshotFromBatch(batch contentblock.Batch) contentblock.Snapshot {
	snapshot := contentblock.Snapshot{Document: contentblock.Document{ID: batch.DocumentID, Profile: "page", Revision: batch.ExpectedRevision}, SourceLocale: "en", Blocks: batch.Upserts}
	for _, group := range batch.LocaleGroups {
		snapshot.LocaleOverlays = append(snapshot.LocaleOverlays, contentblock.LocaleOverlay{Locale: group.Locale, Blocks: group.Upserts})
	}
	return snapshot
}

func TestPageCodecMovesRichTextSubtreeAcrossSectionsWithoutDeletingOrLosingLocale(t *testing.T) {
	codec, document, sourceID, ids := pageRichTextDocumentForTest(t, 2)
	_, destination, destinationID, destinationChildren := pageRichTextDocumentForTest(t, 1)
	destination.Base.Nodes[0].Placement.Index = 1
	document.Base.Nodes = append(document.Base.Nodes, destination.Base.Nodes[0])
	document.LocaleOverlay.Sections = append(document.LocaleOverlay.Sections, destination.LocaleOverlay.Sections[0])
	root, childID := ids[0], uuid.NewString()
	callout, calloutLocale, err := codec.rich.newBlock("callout", root)
	require.NoError(t, err)
	callout.Placement = &contentv1.ContentBlockPlacement{}
	calloutLocale.GetCallout().Content = []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "moving copy"}}}}
	document.Base.Nodes[0].Section.GetRichText().Blocks.Nodes[0] = callout
	document.LocaleOverlay.Sections[0].GetRichText().Blocks.Blocks[0] = calloutLocale
	child, childLocale, err := codec.rich.newBlock("paragraph", childID)
	require.NoError(t, err)
	child.Placement = &contentv1.ContentBlockPlacement{ParentBlockId: &root}
	childLocale.GetParagraph().Content = []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "moving child"}}}}
	document.Base.Nodes[0].Section.GetRichText().Blocks.Nodes = append(document.Base.Nodes[0].Section.GetRichText().Blocks.Nodes, child)
	document.LocaleOverlay.Sections[0].GetRichText().Blocks.Blocks = append(document.LocaleOverlay.Sections[0].GetRichText().Blocks.Blocks, childLocale)
	seed, issues, err := codec.Compile(uuid.New(), document, core.LocaleRoleSource, core.Revision(uuid.NewString()), uuid.New(), nil)
	require.NoError(t, err)
	require.Empty(t, issues)
	document, err = contentblock.SnapshotToLocalizedPageDocument(pageSnapshotFromBatch(seed), "en")
	require.NoError(t, err)
	translated := proto.Clone(calloutLocale).(*contentv1.RichTextBlockLocale)
	translated.GetCallout().Content[0].GetText().Text = "translated moving copy"
	translatedChild := proto.Clone(childLocale).(*contentv1.RichTextBlockLocale)
	translatedChild.GetParagraph().Content[0].GetText().Text = "translated moving child"
	targetBatch, err := contentblock.BatchFromPageProtoWithAffectedLocaleValues(seed.DocumentID, &contentv1.PageSectionMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		ExpectedRevision:        seed.ExpectedRevision.String(), ContributorMemberIds: []string{uuid.NewString()},
		LocaleMutationGroups: []*contentv1.PageLocaleMutationGroup{{Locale: "ko", Mutations: []*contentv1.PageSectionLocaleMutation{
			pageRichTextLocaleMutation(sourceID, translated), pageRichTextLocaleMutation(sourceID, translatedChild),
		}}},
	}, "ko", nil)
	require.NoError(t, err)
	for _, test := range []struct {
		name         string
		after        string
		removeSource bool
	}{
		{"front", "", false}, {"after last", destinationChildren[0], false}, {"move then delete source", destinationChildren[0], true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, store, created := newCodecStoreForTest(t, "page")
			persistedSeed := seed
			persistedSeed.DocumentID, persistedSeed.ExpectedRevision = created.Document.ID, created.Document.Revision
			storedSeed := persistCodecBatchForTest(t, db, store, persistedSeed)
			persistedTarget := targetBatch
			persistedTarget.DocumentID, persistedTarget.ExpectedRevision = created.Document.ID, storedSeed.Document.Revision
			storedSeed = persistCodecTargetBatchForTest(t, db, store, persistedTarget)
			operations := []core.Operation{core.MoveBlockOperation(core.BlockID(root), core.BlockID(destinationID), core.BlockID(test.after))}
			if test.removeSource {
				operations = append(operations, core.DeleteBlockOperation(core.BlockID(sourceID)))
			}
			before := proto.Clone(document)
			batch, issues, err := codec.Compile(created.Document.ID, document, core.LocaleRoleSource, core.Revision(storedSeed.Document.Revision.String()), uuid.New(), operations)
			require.NoError(t, err)
			require.Empty(t, issues)
			require.True(t, proto.Equal(before, document))
			require.NotContains(t, batch.Deletes, uuid.MustParse(root))
			require.NotContains(t, batch.Deletes, uuid.MustParse(childID))
			snapshot := persistCodecBatchForTest(t, db, store, batch)
			loaded, err := contentblock.SnapshotToLocalizedPageDocument(snapshot, "en")
			require.NoError(t, err)
			_, movedSection, ok := findPageNode(loaded, destinationID)
			require.True(t, ok)
			nodes := movedSection.Section.GetRichText().Blocks.Nodes
			require.Len(t, nodes, 3)
			byID := make(map[string]*contentv1.RichTextBlockNode)
			for _, node := range nodes {
				byID[node.Block.Id] = node
			}
			require.Empty(t, byID[root].Placement.GetParentBlockId())
			require.Equal(t, root, byID[childID].Placement.GetParentBlockId())
			wantIndex := uint32(0)
			if test.after != "" {
				wantIndex = 1
			}
			require.Equal(t, wantIndex, byID[root].Placement.Index)
			locale, exists := findPageLocaleSection(loaded, destinationID)
			require.True(t, exists)
			localized := make(map[string]*contentv1.RichTextBlockLocale)
			for _, block := range locale.GetRichText().Blocks.Blocks {
				localized[block.BlockId] = block
			}
			require.Equal(t, "moving copy", localized[root].GetCallout().Content[0].GetText().Text)
			require.Equal(t, "moving child", localized[childID].GetParagraph().Content[0].GetText().Text)
			target, err := contentblock.SnapshotToLocalizedPageDocument(snapshot, "ko")
			require.NoError(t, err)
			targetLocale, exists := findPageLocaleSection(target, destinationID)
			require.True(t, exists)
			foundTranslations := 0
			for _, block := range targetLocale.GetRichText().Blocks.Blocks {
				if block.BlockId == root {
					foundTranslations++
					require.Equal(t, "translated moving copy", block.GetCallout().Content[0].GetText().Text)
				}
				if block.BlockId == childID {
					foundTranslations++
					require.Equal(t, "translated moving child", block.GetParagraph().Content[0].GetText().Text)
				}
			}
			require.Equal(t, 2, foundTranslations)
			if !test.removeSource {
				_, source, exists := findPageNode(loaded, sourceID)
				require.True(t, exists)
				require.Len(t, source.Section.GetRichText().Blocks.Nodes, 1)
				require.Zero(t, source.Section.GetRichText().Blocks.Nodes[0].Placement.Index)
			}
		})
	}
	failed := []core.Operation{core.MoveBlockOperation(core.BlockID(root), core.BlockID(destinationID), core.BlockID(destinationChildren[0])), core.SetFieldOperation(core.BlockID(childID), "content", core.RichText(core.TextColor("invalid", core.InlineText("fail"))))}
	before := proto.Clone(document)
	_, issues, err = codec.Compile(uuid.New(), document, core.LocaleRoleSource, core.Revision(uuid.NewString()), uuid.New(), failed)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Empty(t, issues)
	require.True(t, proto.Equal(before, document))
}

func TestPageCodecCreatesColumnsWithVirtualChildrenInOneBatch(t *testing.T) {
	codec, document, _, _ := pageRichTextDocumentForTest(t, 0)
	columns, first, second := uuid.NewString(), uuid.NewString(), uuid.NewString()
	batch, issues, err := codec.Compile(uuid.New(), document, core.LocaleRoleSource, core.Revision(uuid.NewString()), uuid.New(), []core.Operation{
		core.InsertBlockOperation(core.BlockID(columns), "columns", "", ""),
		core.InsertBlockOperation(core.BlockID(first), pageColumnBlockKind, core.BlockID(columns), ""),
		core.InsertBlockOperation(core.BlockID(second), pageColumnBlockKind, core.BlockID(columns), core.BlockID(first)),
	})
	require.NoError(t, err)
	require.Empty(t, issues)
	loaded, err := contentblock.SnapshotToLocalizedPageDocument(pageSnapshotFromBatch(batch), "en")
	require.NoError(t, err)
	_, section, found := findPageNode(loaded, columns)
	require.True(t, found)
	items := section.Section.GetColumns().Props.Columns
	require.Len(t, items, 2)
	require.Equal(t, first, items[0].Id)
	require.Equal(t, second, items[1].Id)
}

func TestPageCodecNestedTableTargetSetPreservesUntouchedSourceFallback(t *testing.T) {
	codec, source, sectionID, _ := pageRichTextDocumentForTest(t, 0)
	_, table, block, row, cell := localizedTableDocumentForTest(t)
	source.Base.Nodes[0].Section.GetRichText().Blocks = table.Base
	source.LocaleOverlay.Sections[0].GetRichText().Blocks.Blocks = table.LocaleOverlay.Blocks
	db, store, created := newCodecStoreForTest(t, "page")
	seed, issues, err := codec.Compile(created.Document.ID, source, core.LocaleRoleSource, core.Revision(created.Document.Revision.String()), uuid.New(), nil)
	require.NoError(t, err)
	require.Empty(t, issues)
	snapshot := persistCodecBatchForTest(t, db, store, seed)
	target, err := contentblock.SnapshotToLocalizedPageDocument(snapshot, "ko")
	require.NoError(t, err)
	before := proto.Clone(target)
	path := []core.FieldPathSegment{core.ObjectPath("rows"), core.ListPath(core.RelationItemID(row)), core.ObjectPath("cells"), core.ListPath(core.RelationItemID(cell)), core.ObjectPath("content")}
	batch, issues, err := codec.Compile(seed.DocumentID, target, core.LocaleRoleNonSource, core.Revision(snapshot.Document.Revision.String()), uuid.New(), []core.Operation{
		core.SetNestedFieldOperation(core.BlockID(block), richTextTableLocaleField, path, core.RichText(core.InlineText("changed"))),
	})
	require.NoError(t, err)
	require.Empty(t, issues)
	require.True(t, proto.Equal(before, target))
	require.Empty(t, batch.Upserts)
	snapshot = persistCodecTargetBatchForTest(t, db, store, batch)
	stored, err := contentblock.SnapshotToLocalizedPageDocument(snapshot, "ko")
	require.NoError(t, err)
	locale, found := findPageLocaleSection(stored, sectionID)
	require.True(t, found)
	require.Len(t, locale.GetRichText().Blocks.Blocks[0].GetTable().Content.Rows[0].Cells, 1)
	loaded, err := contentblock.MaterializeSnapshotPageLocale(snapshot, "ko")
	require.NoError(t, err)
	locale, found = findPageLocaleSection(loaded, sectionID)
	require.True(t, found)
	cells := locale.GetRichText().Blocks.Blocks[0].GetTable().Content.Rows[0].Cells
	require.Len(t, cells, 2)
	require.Equal(t, "changed", cells[0].Content[0].GetText().Text)
	require.Equal(t, "second target", cells[1].Content[0].GetText().Text)
}
