package aidocumentadapter

import (
	"reflect"
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

func TestRichTextCodecCompilesGeneratedInline(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST)
	if err != nil {
		t.Fatal(err)
	}
	blockID := uuid.New()
	operation := core.SetFieldOperation(core.BlockID(blockID.String()), "content", core.RichText(
		core.TextColor("#aabbcc", core.Bold(core.InlineText("after"))),
	))
	batch, issues, err := codec.Compile(uuid.New(), localizedParagraphDocument(blockID, "before"), core.LocaleRoleSource, core.Revision(uuid.NewString()), uuid.New(), []core.Operation{operation})
	if err != nil || len(issues) != 0 {
		t.Fatalf("compile = (%+v, %+v, %v)", batch, issues, err)
	}
	if len(batch.Upserts) != 1 || len(batch.LocaleGroups) != 1 || len(batch.LocaleGroups[0].Upserts) != 1 {
		t.Fatalf("compiled batch = %+v", batch)
	}
}

func TestRichTextCodecTargetUnsetOnAbsentOverlayIsNoOp(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST)
	if err != nil {
		t.Fatal(err)
	}
	blockID, documentID, contributor, revision := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	document := localizedParagraphDocument(blockID, "before")
	document.Locale = "ko"
	document.LocaleOverlay = &contentv1.RichTextLocaleOverlay{Locale: "ko"}

	batch, issues, err := codec.Compile(
		documentID,
		document,
		core.LocaleRoleNonSource,
		core.Revision(revision.String()),
		contributor,
		[]core.Operation{core.UnsetFieldOperation(core.BlockID(blockID.String()), "content")},
	)
	if err != nil || len(issues) != 0 {
		t.Fatalf("compile no-op = (%+v, %+v, %v)", batch, issues, err)
	}
	if batch.DocumentID != documentID || batch.ExpectedRevision != revision {
		t.Fatalf("no-op envelope = %+v", batch)
	}
	if len(batch.ContributorMemberIDs) != 1 || batch.ContributorMemberIDs[0] != contributor {
		t.Fatalf("no-op contributors = %+v", batch.ContributorMemberIDs)
	}
	if len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 || len(batch.LocaleGroups) != 0 {
		t.Fatalf("no-op unexpectedly contains mutations = %+v", batch)
	}
}

func TestRichTextCodecPreservesExplicitEmptyDistinctFromAbsentTargetContent(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST)
	if err != nil {
		t.Fatal(err)
	}
	blockID := uuid.New()
	document := localizedParagraphDocument(blockID, "source")
	document.Locale = "ko"
	document.LocaleOverlay = &contentv1.RichTextLocaleOverlay{Locale: "ko"}

	before, err := codec.Project(document)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 || len(before[0].Localized) != 0 {
		t.Fatalf("absent target projection = %+v", before)
	}
	operation := core.SetFieldOperation(core.BlockID(blockID.String()), "content", core.RichText(core.InlineText("")))
	batch, issues, err := codec.Compile(
		uuid.New(), document, core.LocaleRoleNonSource, core.Revision(uuid.NewString()), uuid.New(), []core.Operation{operation},
	)
	if err != nil || len(issues) != 0 || len(batch.LocaleGroups) != 1 || len(batch.LocaleGroups[0].Upserts) != 1 {
		t.Fatalf("explicit-empty compile = (%+v, %+v, %v)", batch, issues, err)
	}
	working := proto.Clone(document).(*contentv1.LocalizedRichTextDocument)
	if err := codec.applyOperation(working, operation, map[string]struct{}{}); err != nil {
		t.Fatal(err)
	}
	after, err := codec.Project(working)
	if err != nil {
		t.Fatal(err)
	}
	content, ok := fieldValue(after[0].Localized, "content")
	if !ok || content.Kind != core.ValueKindInline || len(content.Inline) != 1 || content.Inline[0].Kind != core.InlineKindText || content.Inline[0].Text != "" {
		t.Fatalf("explicit-empty projection = %+v", after[0].Localized)
	}
}

func TestRichTextCodecCompilesBlockTopologyOperationsThroughGeneratedBatch(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST)
	if err != nil {
		t.Fatal(err)
	}
	first, second := uuid.New(), uuid.New()
	base := localizedParagraphDocument(first, "first")
	node, locale, err := codec.newBlock("paragraph", second.String())
	if err != nil {
		t.Fatal(err)
	}
	node.Placement = &contentv1.ContentBlockPlacement{Index: 1}
	base.Base.Nodes = append(base.Base.Nodes, node)
	base.LocaleOverlay.Blocks = append(base.LocaleOverlay.Blocks, locale)

	cases := []struct {
		name      string
		operation core.Operation
	}{
		{name: "insert", operation: core.InsertBlockOperation(core.BlockID(uuid.NewString()), "paragraph", "", core.BlockID(second.String()))},
		{name: "move", operation: core.MoveBlockOperation(core.BlockID(second.String()), "", "")},
		{name: "delete", operation: core.DeleteBlockOperation(core.BlockID(second.String()))},
		{name: "replace", operation: core.ReplaceBlockKindOperation(core.BlockID(second.String()), "divider")},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			batch, issues, err := codec.Compile(
				uuid.New(), base, core.LocaleRoleSource, core.Revision(uuid.NewString()), uuid.New(),
				[]core.Operation{test.operation},
			)
			if err != nil || len(issues) != 0 {
				t.Fatalf("compile = (%+v, %+v, %v)", batch, issues, err)
			}
			if len(batch.Upserts)+len(batch.Deletes)+len(batch.Reorders)+len(batch.LocaleGroups) == 0 {
				t.Fatalf("compiled batch is empty: %+v", batch)
			}
		})
	}
}

func TestRichTextCodecCompilesFileAttachAndRejectsRequiredDetach(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST)
	if err != nil {
		t.Fatal(err)
	}
	blockID, fileID := uuid.New(), uuid.New()
	node, locale, err := codec.newBlock("file", blockID.String())
	if err != nil {
		t.Fatal(err)
	}
	node.Placement = &contentv1.ContentBlockPlacement{}
	document := &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: codec.Catalog().Fingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST, Locale: "en",
		Base:          &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{node}},
		LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{locale}},
	}
	target := core.FieldTarget{Block: core.BlockID(blockID.String()), Field: "attachment"}
	batch, issues, err := codec.Compile(
		uuid.New(), document, core.LocaleRoleSource, core.Revision(uuid.NewString()), uuid.New(),
		[]core.Operation{core.AttachFileOperation(target.Block, target.Field, core.FileReference(fileID.String()))},
	)
	if err != nil || len(issues) != 0 || len(batch.Upserts) != 1 {
		t.Fatalf("attach compile = (%+v, %+v, %v)", batch, issues, err)
	}
	if err := codec.setFile(document, target, core.FileReference(fileID.String())); err != nil {
		t.Fatal(err)
	}
	_, issues, err = codec.Compile(
		uuid.New(), document, core.LocaleRoleSource, core.Revision(uuid.NewString()), uuid.New(),
		[]core.Operation{core.DetachFileOperation(target.Block, target.Field)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 {
		t.Fatalf("required detach issues = %+v", issues)
	}
}

func TestRichTextCodecRoundTripsStableNestedArrayFilePath(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST)
	if err != nil {
		t.Fatal(err)
	}
	blockID, fileID := uuid.New(), uuid.New()
	node, locale, err := codec.newBlock("shader", blockID.String())
	if err != nil {
		t.Fatal(err)
	}
	node.Placement = &contentv1.ContentBlockPlacement{}
	document := &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: codec.Catalog().Fingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST, Locale: "en",
		Base:          &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{node}},
		LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{locale}},
	}
	stageKinds := []string{"common", "vertex", "bufferA", "bufferB", "bufferC", "bufferD", "cubemap", "sound", "image"}
	channelHandles := []string{"channel-a", "channel-b", "channel-c", "channel-d"}
	stages := make([]core.ListItem, 0, len(stageKinds))
	for _, stageKind := range stageKinds {
		channels := make([]core.ListItem, 0, len(channelHandles))
		for _, handle := range channelHandles {
			kind := "none"
			if stageKind == "common" && handle == "channel-a" {
				kind = "textureFile"
			}
			channels = append(channels, core.StableItem(core.RelationItemID(handle), core.Object(
				core.ObjectValue("kind", core.Text(kind)),
			)))
		}
		stages = append(stages, core.StableItem(core.RelationItemID(stageKind), core.Object(
			core.ObjectValue("kind", core.Text(stageKind)),
			core.ObjectValue("source", core.Text("void main() {}")),
			core.ObjectValue("channels", core.List(channels...)),
		)))
	}
	setStages := core.SetFieldOperation(core.BlockID(blockID.String()), "stages", core.List(stages...))
	fileTarget := core.FieldTarget{
		Block: core.BlockID(blockID.String()), Field: "stages",
		Path: []core.FieldPathSegment{
			core.ListPath("common"), core.ObjectPath("channels"),
			core.ListPath("channel-a"), core.ObjectPath("file"),
		},
	}
	attach := core.Operation{
		Kind:       core.OperationAttachFile,
		AttachFile: &core.AttachFile{Target: fileTarget, File: core.FileReference(fileID.String())},
	}
	batch, issues, err := codec.Compile(
		uuid.New(), document, core.LocaleRoleSource, core.Revision(uuid.NewString()), uuid.New(),
		[]core.Operation{setStages, attach},
	)
	if err != nil || len(issues) != 0 || len(batch.Upserts) != 1 {
		t.Fatalf("nested compile = (%+v, %+v, %v)", batch, issues, err)
	}

	working := proto.Clone(document).(*contentv1.LocalizedRichTextDocument)
	deleted := map[string]struct{}{}
	for _, operation := range []core.Operation{setStages, attach} {
		if err := codec.applyOperation(working, operation, deleted); err != nil {
			t.Fatal(err)
		}
	}
	nodes, err := codec.Project(working)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || len(nodes[0].Files) != 1 || nodes[0].Files[0].File != core.FileReference(fileID.String()) {
		t.Fatalf("nested File projection = %+v", nodes)
	}
	if !reflect.DeepEqual(nodes[0].Files[0].Path, fileTarget.Path) {
		t.Fatalf("nested File path = %+v", nodes[0].Files[0].Path)
	}
}
