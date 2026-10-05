package aidocumentadapter

import (
	"context"
	"reflect"
	"testing"

	"connectrpc.com/connect"
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
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

func TestRichTextCodecReindexesDeletedAndMovedSiblingGroups(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST)
	require.NoError(t, err)
	ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()}
	document := localizedParagraphDocument(uuid.MustParse(ids[0]), "first")
	for index := 1; index < len(ids); index++ {
		kind := core.BlockKind("paragraph")
		if index == 3 {
			kind = "callout"
		}
		node, locale, err := codec.newBlock(kind, ids[index])
		require.NoError(t, err)
		node.Placement = &contentv1.ContentBlockPlacement{Index: uint32(index)}
		document.Base.Nodes = append(document.Base.Nodes, node)
		document.LocaleOverlay.Blocks = append(document.LocaleOverlay.Blocks, locale)
	}
	// Exercise placement order independently of the transport array order.
	for left, right := 0, len(document.Base.Nodes)-1; left < right; left, right = left+1, right-1 {
		document.Base.Nodes[left], document.Base.Nodes[right] = document.Base.Nodes[right], document.Base.Nodes[left]
	}
	inserted := core.BlockID(uuid.NewString())
	for _, test := range []struct {
		name       string
		operations []core.Operation
		roots      []string
		children   []string
	}{
		{"delete first", []core.Operation{core.DeleteBlockOperation(core.BlockID(ids[0]))}, ids[1:], nil},
		{"delete middle", []core.Operation{core.DeleteBlockOperation(core.BlockID(ids[1]))}, []string{ids[0], ids[2], ids[3]}, nil},
		{"move first into callout", []core.Operation{core.MoveBlockOperation(core.BlockID(ids[0]), core.BlockID(ids[3]), "")}, ids[1:], []string{ids[0]}},
		{"move into and back out", []core.Operation{core.MoveBlockOperation(core.BlockID(ids[0]), core.BlockID(ids[3]), ""), core.MoveBlockOperation(core.BlockID(ids[1]), core.BlockID(ids[3]), core.BlockID(ids[0])), core.MoveBlockOperation(core.BlockID(ids[0]), "", core.BlockID(ids[2]))}, []string{ids[2], ids[0], ids[3]}, []string{ids[1]}},
		{"delete moved subtree", []core.Operation{core.MoveBlockOperation(core.BlockID(ids[0]), core.BlockID(ids[3]), ""), core.MoveBlockOperation(core.BlockID(ids[3]), "", core.BlockID(ids[1])), core.DeleteBlockOperation(core.BlockID(ids[3]))}, []string{ids[1], ids[2]}, nil},
		{"move insert delete", []core.Operation{core.MoveBlockOperation(core.BlockID(ids[3]), "", ""), core.InsertBlockOperation(inserted, "paragraph", "", core.BlockID(ids[0])), core.DeleteBlockOperation(core.BlockID(ids[1]))}, []string{ids[3], ids[0], string(inserted), ids[2]}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, store, created := newCodecStoreForTest(t, "post")
			seed, issues, err := codec.Compile(created.Document.ID, document, core.LocaleRoleSource, core.Revision(created.Document.Revision.String()), uuid.New(), nil)
			require.NoError(t, err)
			require.Empty(t, issues)
			storedSeed := persistCodecBatchForTest(t, db, store, seed)
			before := proto.Clone(document)
			batch, issues, err := codec.Compile(created.Document.ID, document, core.LocaleRoleSource, core.Revision(storedSeed.Document.Revision.String()), uuid.New(), test.operations)
			require.NoError(t, err)
			require.Empty(t, issues)
			require.True(t, proto.Equal(before, document))
			snapshot := persistCodecBatchForTest(t, db, store, batch)
			loaded, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, "en")
			require.NoError(t, err)
			roots, children := make([]string, len(test.roots)), make([]string, len(test.children))
			for _, node := range loaded.Base.Nodes {
				if node.Placement.GetParentBlockId() == "" {
					require.Less(t, int(node.Placement.Index), len(roots))
					roots[node.Placement.Index] = node.Block.Id
				} else {
					require.Equal(t, ids[3], node.Placement.GetParentBlockId())
					require.Less(t, int(node.Placement.Index), len(children))
					children[node.Placement.Index] = node.Block.Id
				}
			}
			require.Equal(t, test.roots, roots)
			if len(test.children) == 0 {
				require.Empty(t, children)
			} else {
				require.Equal(t, test.children, children)
			}
		})
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
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Empty(t, issues, "batch-level generated validation must not invent an operation index")
}

type codecStoreFileReuse struct{}

func (codecStoreFileReuse) AuthorizeFileReuse(context.Context, *gorm.DB, contentblock.Document, contentblock.FullBlock, contentblock.FileReference, contentblock.File) error {
	return nil
}

// Use the production generated contract and Store on a fresh SQLite database.
// These tests cover aggregate validation and persistence, not PostgreSQL locks.
func newCodecStoreForTest(t *testing.T, profile string) (*gorm.DB, *contentblock.Store, contentblock.Snapshot) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	for _, statement := range []string{
		`CREATE TABLE content_document (id TEXT PRIMARY KEY, profile TEXT, revision TEXT, created_at DATETIME, updated_at DATETIME)`,
		`CREATE TABLE content_block (id TEXT PRIMARY KEY, document_id TEXT, parent_block_id TEXT, container_slot TEXT, position INTEGER, kind TEXT, shared_data BLOB, created_at DATETIME, updated_at DATETIME)`,
		`CREATE TABLE content_block_locale (block_id TEXT, locale TEXT, localized_data BLOB, PRIMARY KEY(block_id, locale))`,
		`CREATE TABLE content_block_attachment (block_id TEXT, reference_path TEXT, selector_kind TEXT, file_id TEXT, missing_kind TEXT, download_audience TEXT, PRIMARY KEY(block_id, reference_path))`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	store, err := contentblock.NewGeneratedStore(codecStoreFileReuse{})
	require.NoError(t, err)
	var created contentblock.Snapshot
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var createErr error
		created, createErr = store.CreateDocument(t.Context(), tx, contentblock.CreateInput{Profile: profile, SourceLocale: "en"})
		return createErr
	}))
	return db, store, created
}

func persistCodecBatchForTest(t *testing.T, db *gorm.DB, store *contentblock.Store, batch contentblock.Batch) contentblock.Snapshot {
	t.Helper()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, err := store.ApplyBatch(t.Context(), tx, batch, func(context.Context, *gorm.DB, uuid.UUID) (contentblock.DomainContext, error) {
			return contentblock.DomainContext{SourceLocale: "en"}, nil
		})
		return err
	}))
	snapshot, err := store.LoadSnapshot(t.Context(), db, batch.DocumentID, "en")
	require.NoError(t, err)
	return snapshot
}

func persistCodecTargetBatchForTest(t *testing.T, db *gorm.DB, store *contentblock.Store, batch contentblock.Batch) contentblock.Snapshot {
	t.Helper()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, err := store.ApplyTargetLocaleBatchWithMetadata(t.Context(), tx, batch, "ko",
			func(context.Context, *gorm.DB, uuid.UUID) (contentblock.DomainContext, error) {
				return contentblock.DomainContext{SourceLocale: "en"}, nil
			},
			func(_ context.Context, _ *gorm.DB, changed bool) (contentblock.MetadataEffect, error) {
				return contentblock.MetadataEffect{Changed: changed, ChangedLocales: []string{"ko"}}, nil
			})
		return err
	}))
	snapshot, err := store.LoadSnapshot(t.Context(), db, batch.DocumentID, "en")
	require.NoError(t, err)
	require.Equal(t, batch.ExpectedRevision, snapshot.Document.Revision)
	return snapshot
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
