package aidocumentadapter

import (
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPageLocalePresenceFollowsLastEffectiveWrite(t *testing.T) {
	for _, test := range []struct {
		name           string
		operations     []string
		richText       bool
		captionPresent bool
		caption        string
	}{
		{name: "set then replace", operations: []string{"set", "replace"}, richText: true},
		{name: "set then delete and reinsert", operations: []string{"set", "delete", "insert"}, richText: true},
		{name: "set then unset", operations: []string{"set", "unset"}},
		{name: "unset then set empty", operations: []string{"unset", "set empty"}, captionPresent: true},
		{name: "parent set then child unset", operations: []string{"set props", "unset"}},
		{name: "child set then parent replacement", operations: []string{"set empty", "set empty props"}},
		{name: "child set then parent unset", operations: []string{"set empty", "unset props"}},
		{name: "child unset then parent set empty", operations: []string{"unset", "set props empty caption"}, captionPresent: true},
		{name: "last nonempty set", operations: []string{"set empty", "unset", "set"}, captionPresent: true, caption: "edited"},
	} {
		t.Run(test.name, func(t *testing.T) {
			codec, err := NewPageCodec()
			require.NoError(t, err)
			sectionID := uuid.NewString()
			document := pagePresenceExternalVideoForTest(sectionID)
			db, store, created := newCodecStoreForTest(t, "page")
			path := []core.FieldPathSegment{core.ObjectPath("props"), core.ObjectPath("caption")}
			seed, issues, err := codec.Compile(created.Document.ID, document, core.LocaleRoleSource, core.Revision(created.Document.Revision.String()), uuid.New(), []core.Operation{
				core.SetNestedFieldOperation(core.BlockID(sectionID), pageSectionLocaleField, path, core.Text("source")),
			})
			require.NoError(t, err)
			require.Empty(t, issues)
			snapshot := persistCodecBatchForTest(t, db, store, seed)
			document, err = contentblock.SnapshotToLocalizedPageDocument(snapshot, "en")
			require.NoError(t, err)
			available := map[string]core.Operation{
				"set":                     core.SetNestedFieldOperation(core.BlockID(sectionID), pageSectionLocaleField, path, core.Text("edited")),
				"set empty":               core.SetNestedFieldOperation(core.BlockID(sectionID), pageSectionLocaleField, path, core.Text("")),
				"unset":                   core.UnsetNestedFieldOperation(core.BlockID(sectionID), pageSectionLocaleField, path),
				"replace":                 core.ReplaceBlockKindOperation(core.BlockID(sectionID), "rich-text"),
				"delete":                  core.DeleteBlockOperation(core.BlockID(sectionID)),
				"insert":                  core.InsertBlockOperation(core.BlockID(sectionID), "rich-text", "", ""),
				"set props":               core.SetNestedFieldOperation(core.BlockID(sectionID), pageSectionLocaleField, path[:1], core.Object(core.ObjectValue("caption", core.Text("edited")))),
				"set empty props":         core.SetNestedFieldOperation(core.BlockID(sectionID), pageSectionLocaleField, path[:1], core.Object()),
				"unset props":             core.UnsetNestedFieldOperation(core.BlockID(sectionID), pageSectionLocaleField, path[:1]),
				"set props empty caption": core.SetNestedFieldOperation(core.BlockID(sectionID), pageSectionLocaleField, path[:1], core.Object(core.ObjectValue("caption", core.Text("")))),
			}
			operations := make([]core.Operation, 0, len(test.operations))
			for _, name := range test.operations {
				operations = append(operations, available[name])
			}
			command := pagePresenceValidateForTest(t, codec, document, snapshot, operations)
			batch, issues, err := codec.Compile(created.Document.ID, document, core.LocaleRoleSource, command.ExpectedDocumentRevision, uuid.New(), command.Operations)
			require.NoError(t, err)
			require.Empty(t, issues)
			snapshot = persistCodecBatchForTest(t, db, store, batch)
			readback, err := contentblock.SnapshotToLocalizedPageDocument(snapshot, "en")
			require.NoError(t, err)
			require.Len(t, readback.Base.Nodes, 1)
			if test.richText {
				require.NotNil(t, readback.Base.Nodes[0].Section.GetRichText())
			} else {
				require.NotNil(t, readback.Base.Nodes[0].Section.GetExternalVideo())
				locale, exists := findPageLocaleSection(readback, sectionID)
				require.True(t, exists)
				require.Equal(t, test.caption, locale.GetExternalVideo().GetProps().GetCaption())
			}
			present, err := contentblock.PresentPageLocaleValues(snapshot, "en")
			require.NoError(t, err)
			captionPresent := false
			for _, target := range present {
				if target.GetBlockHandle() == sectionID && len(target.Path) == 2 && target.Path[1].GetFieldHandle() == "caption" {
					captionPresent = true
				}
			}
			require.Equal(t, test.captionPresent, captionPresent, "persisted presence must follow the final effective write")
		})
	}
}

func TestPageLocalePresenceTargetEmptySetPreservesSharedRevision(t *testing.T) {
	codec, err := NewPageCodec()
	require.NoError(t, err)
	sectionID := uuid.NewString()
	db, store, created := newCodecStoreForTest(t, "page")
	seed, issues, err := codec.Compile(created.Document.ID, pagePresenceExternalVideoForTest(sectionID), core.LocaleRoleSource, core.Revision(created.Document.Revision.String()), uuid.New(), nil)
	require.NoError(t, err)
	require.Empty(t, issues)
	snapshot := persistCodecBatchForTest(t, db, store, seed)
	target, err := contentblock.SnapshotToLocalizedPageDocument(snapshot, "ko")
	require.NoError(t, err)
	command := pagePresenceValidateForTest(t, codec, target, snapshot, []core.Operation{
		core.SetNestedFieldOperation(core.BlockID(sectionID), pageSectionLocaleField, []core.FieldPathSegment{core.ObjectPath("props"), core.ObjectPath("caption")}, core.Text("")),
	})
	batch, issues, err := codec.Compile(created.Document.ID, target, core.LocaleRoleNonSource, command.ExpectedDocumentRevision, uuid.New(), command.Operations)
	require.NoError(t, err)
	require.Empty(t, issues)
	require.Empty(t, batch.Upserts)
	revision := snapshot.Document.Revision
	snapshot = persistCodecTargetBatchForTest(t, db, store, batch)
	require.Equal(t, revision, snapshot.Document.Revision)
	present, err := contentblock.PresentPageLocaleValues(snapshot, "ko")
	require.NoError(t, err)
	require.Len(t, present, 1)
	require.Equal(t, "caption", present[0].Path[1].GetFieldHandle())
	readback, err := contentblock.SnapshotToLocalizedPageDocument(snapshot, "ko")
	require.NoError(t, err)
	locale, exists := findPageLocaleSection(readback, sectionID)
	require.True(t, exists)
	require.Empty(t, locale.GetExternalVideo().GetProps().GetCaption())
}

func pagePresenceExternalVideoForTest(sectionID string) *contentv1.LocalizedPageDocument {
	return &contentv1.LocalizedPageDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Locale: "en",
		Base: &contentv1.PageSectionGraph{Nodes: []*contentv1.PageSectionNode{{
			Section:   &contentv1.PageSection{Id: sectionID, Settings: &contentv1.PageSectionSettings{}, Value: &contentv1.PageSection_ExternalVideo{ExternalVideo: &contentv1.ExternalVideoSection{Props: &contentv1.ExternalVideoSectionProps{Uri: "https://youtu.be/example"}}}},
			Placement: &contentv1.PageSectionPlacement{},
		}}},
		LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: "en", Sections: []*contentv1.PageSectionLocale{{SectionId: sectionID, Value: &contentv1.PageSectionLocale_ExternalVideo{ExternalVideo: &contentv1.ExternalVideoSectionLocale{Props: &contentv1.ExternalVideoSectionLocaleProps{}}}}}},
	}
}

func pagePresenceValidateForTest(t *testing.T, codec *PageCodec, document *contentv1.LocalizedPageDocument, snapshot contentblock.Snapshot, operations []core.Operation) core.ValidatedApply {
	t.Helper()
	nodes, err := codec.Project(document)
	require.NoError(t, err)
	identity := core.DocumentIdentity{Domain: core.DomainPage, Reference: core.DocumentReference(uuid.NewString())}
	loaded := core.Document{Identity: identity, SourceLocale: "en", Locale: core.Locale(document.Locale), LocaleExists: document.Locale == "en", DocumentRevision: core.Revision(snapshot.Document.Revision.String()), Catalog: codec.Catalog(), Nodes: nodes}
	command, validation := core.ValidateLoadedApply(loaded, core.ApplyRequest{Protocol: core.ProtocolVersion, Profile: identity.Domain, Document: identity.Reference, Locale: loaded.Locale, ExpectedDocumentRevision: loaded.DocumentRevision, Operations: operations})
	require.True(t, validation.Valid(), "%+v", validation)
	return command
}

func TestPageLocalePresencePrunesDeletedTableCell(t *testing.T) {
	codec, source, sectionID, _ := pageRichTextDocumentForTest(t, 0)
	_, table, blockID, rowID, cellID := localizedTableDocumentForTest(t)
	source.Base.Nodes[0].Section.GetRichText().Blocks = table.Base
	source.LocaleOverlay.Sections[0].GetRichText().Blocks.Blocks = table.LocaleOverlay.Blocks
	db, store, created := newCodecStoreForTest(t, "page")
	seed, issues, err := codec.Compile(created.Document.ID, source, core.LocaleRoleSource, core.Revision(created.Document.Revision.String()), uuid.New(), nil)
	require.NoError(t, err)
	require.Empty(t, issues)
	snapshot := persistCodecBatchForTest(t, db, store, seed)
	source, err = contentblock.SnapshotToLocalizedPageDocument(snapshot, "en")
	require.NoError(t, err)
	cellPath := []core.FieldPathSegment{core.ObjectPath("rows"), core.ListPath(core.RelationItemID(rowID)), core.ObjectPath("cells"), core.ListPath(core.RelationItemID(cellID))}
	contentPath := append(append([]core.FieldPathSegment(nil), cellPath...), core.ObjectPath("content"))
	command := pagePresenceValidateForTest(t, codec, source, snapshot, []core.Operation{
		core.SetNestedFieldOperation(core.BlockID(blockID), richTextTableLocaleField, contentPath, core.RichText()),
		core.UnsetNestedFieldOperation(core.BlockID(blockID), richTextTableField, cellPath),
	})
	batch, issues, err := codec.Compile(created.Document.ID, source, core.LocaleRoleSource, command.ExpectedDocumentRevision, uuid.New(), command.Operations)
	require.NoError(t, err)
	require.Empty(t, issues)
	snapshot = persistCodecBatchForTest(t, db, store, batch)
	present, err := contentblock.PresentPageLocaleValues(snapshot, "en")
	require.NoError(t, err)
	for _, target := range present {
		if target.GetBlockHandle() == blockID && target.GetFieldHandle() == string(richTextTableLocaleField) {
			require.NotEqual(t, cellID, target.Path[3].GetItemHandle(), "removed cell must not regain explicit empty presence")
		}
	}
	readback, err := contentblock.SnapshotToLocalizedPageDocument(snapshot, "en")
	require.NoError(t, err)
	require.Len(t, readback.Base.Nodes[0].Section.GetRichText().Blocks.Nodes[0].Block.GetTable().Content.Rows[0].Cells, 1)
	locale, exists := findPageLocaleSection(readback, sectionID)
	require.True(t, exists)
	cells := locale.GetRichText().Blocks.Blocks[0].GetTable().Content.Rows[0].Cells
	// Native table storage retains orphan locale identities; only authored
	// content presence is pruned here, without changing that storage behavior.
	require.Len(t, cells, 2)
	require.Equal(t, cellID, cells[0].GetCellId())
	require.Empty(t, cells[0].GetContent())
	require.Equal(t, "second target", cells[1].Content[0].GetText().Text)
}

func TestPageLocalePresenceWholeUnitsUnsetClearsReinsertedLocale(t *testing.T) {
	codec, err := NewPageCodec()
	require.NoError(t, err)
	sectionID, firstID, secondID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	oldTitle := "Before"
	document := &contentv1.LocalizedPageDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Locale: "en",
		Base: &contentv1.PageSectionGraph{Nodes: []*contentv1.PageSectionNode{{
			Section: &contentv1.PageSection{Id: sectionID, Settings: &contentv1.PageSectionSettings{}, Value: &contentv1.PageSection_ImmersiveScene{ImmersiveScene: &contentv1.ImmersiveSceneSection{
				Props: &contentv1.ImmersiveSceneSectionProps{}, Units: []*contentv1.PageImmersiveUnit{{Id: firstID, Props: &contentv1.PageImmersiveUnitProps{}}, {Id: secondID, Props: &contentv1.PageImmersiveUnitProps{}}},
			}}}, Placement: &contentv1.PageSectionPlacement{},
		}}},
		LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: "en", Sections: []*contentv1.PageSectionLocale{{
			SectionId: sectionID, Value: &contentv1.PageSectionLocale_ImmersiveScene{ImmersiveScene: &contentv1.ImmersiveSceneSectionLocale{
				Props: &contentv1.ImmersiveSceneSectionLocaleProps{}, Units: []*contentv1.PageImmersiveUnitLocale{{UnitId: firstID, Props: &contentv1.PageImmersiveUnitLocaleProps{Title: &oldTitle}}, {UnitId: secondID, Props: &contentv1.PageImmersiveUnitLocaleProps{}}},
			}},
		}}},
	}
	db, store, created := newCodecStoreForTest(t, "page")
	seed, issues, err := codec.Compile(created.Document.ID, document, core.LocaleRoleSource, core.Revision(created.Document.Revision.String()), uuid.New(), nil)
	require.NoError(t, err)
	require.Empty(t, issues)
	snapshot := persistCodecBatchForTest(t, db, store, seed)
	document, err = contentblock.SnapshotToLocalizedPageDocument(snapshot, "en")
	require.NoError(t, err)
	unitPath := []core.FieldPathSegment{core.ObjectPath("units"), core.ListPath(core.RelationItemID(firstID))}
	titlePath := append(append([]core.FieldPathSegment(nil), unitPath...), core.ObjectPath("props"), core.ObjectPath("title"))
	units := core.List(
		core.StableItem(core.RelationItemID(firstID), core.Object(core.ObjectValue("id", core.Text(firstID)), core.ObjectValue("props", core.Object()))),
		core.StableItem(core.RelationItemID(secondID), core.Object(core.ObjectValue("id", core.Text(secondID)), core.ObjectValue("props", core.Object()))),
	)
	operations := []core.Operation{
		core.SetNestedFieldOperation(core.BlockID(sectionID), pageSectionLocaleField, titlePath, core.Text("")),
		core.UnsetNestedFieldOperation(core.BlockID(sectionID), pageSectionDataField, unitPath[:1]),
		core.SetNestedFieldOperation(core.BlockID(sectionID), pageSectionDataField, unitPath[:1], units),
	}
	command := pagePresenceValidateForTest(t, codec, document, snapshot, operations)
	batch, issues, err := codec.Compile(created.Document.ID, document, core.LocaleRoleSource, command.ExpectedDocumentRevision, uuid.New(), command.Operations)
	require.NoError(t, err)
	require.Empty(t, issues)
	snapshot = persistCodecBatchForTest(t, db, store, batch)
	present, err := contentblock.PresentPageLocaleValues(snapshot, "en")
	require.NoError(t, err)
	revived := false
	for _, target := range present {
		if target.GetBlockHandle() == sectionID && len(target.Path) == 4 && target.Path[1].GetItemHandle() == firstID && target.Path[3].GetFieldHandle() == "title" {
			revived = true
		}
	}
	require.False(t, revived, "whole shared units Unset must cancel the earlier title presence")
	readback, err := contentblock.SnapshotToLocalizedPageDocument(snapshot, "en")
	require.NoError(t, err)
	require.Len(t, readback.Base.Nodes[0].Section.GetImmersiveScene().Units, 2)
	locale, exists := findPageLocaleSection(readback, sectionID)
	require.True(t, exists)
	require.Len(t, locale.GetImmersiveScene().Units, 2)
	require.Equal(t, firstID, locale.GetImmersiveScene().Units[0].GetUnitId())
	require.Nil(t, locale.GetImmersiveScene().Units[0].GetProps().Title)
}
