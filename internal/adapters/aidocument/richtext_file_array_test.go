package aidocumentadapter

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

var cubemapFaceHandles = []core.RelationItemID{"px", "nx", "py", "ny", "pz", "nz"}

type cubemapCodecFixture struct {
	codec    *RichTextCodec
	db       *gorm.DB
	store    *contentblock.Store
	snapshot contentblock.Snapshot
	document *contentv1.LocalizedRichTextDocument
	block    core.BlockID
	files    []string
}

func newCubemapCodecFixture(t *testing.T, withFaces bool) cubemapCodecFixture {
	t.Helper()
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST)
	require.NoError(t, err)
	block := core.BlockID(uuid.NewString())
	node, locale, err := codec.newBlock("shader", string(block))
	require.NoError(t, err)
	node.Placement = &contentv1.ContentBlockPlacement{}
	node.Block.GetShader().Props = &contentv1.ShaderProps{}
	for _, kind := range []contentv1.ShaderProps_StagesItem_Kind{
		contentv1.ShaderProps_StagesItem_KIND_COMMON, contentv1.ShaderProps_StagesItem_KIND_VERTEX,
		contentv1.ShaderProps_StagesItem_KIND_BUFFER_A, contentv1.ShaderProps_StagesItem_KIND_BUFFER_B,
		contentv1.ShaderProps_StagesItem_KIND_BUFFER_C, contentv1.ShaderProps_StagesItem_KIND_BUFFER_D,
		contentv1.ShaderProps_StagesItem_KIND_CUBEMAP, contentv1.ShaderProps_StagesItem_KIND_SOUND,
		contentv1.ShaderProps_StagesItem_KIND_IMAGE,
	} {
		stage := &contentv1.ShaderProps_StagesItem{Kind: kind, Source: "void main() {}"}
		for range 4 {
			stage.Channels = append(stage.Channels, &contentv1.ShaderProps_StagesItem_ChannelsItem{Kind: contentv1.ShaderProps_StagesItem_ChannelsItem_KIND_NONE})
		}
		node.Block.GetShader().Props.Stages = append(node.Block.GetShader().Props.Stages, stage)
	}
	channel := node.Block.GetShader().Props.Stages[0].Channels[0]
	channel.Kind = contentv1.ShaderProps_StagesItem_ChannelsItem_KIND_CUBEMAP_FILES
	db, store, created := newCodecStoreForTest(t, "post")
	require.NoError(t, db.Exec("CREATE TABLE file (id TEXT PRIMARY KEY, mime_type TEXT, delete_requested_at DATETIME)").Error)
	require.NoError(t, db.Exec("CREATE TABLE content_block_attachment_download_audience_segment (block_id TEXT, reference_path TEXT, audience_segment_id TEXT, PRIMARY KEY (block_id,reference_path,audience_segment_id))").Error)
	files := make([]string, len(cubemapFaceHandles))
	for index := range files {
		files[index] = uuid.NewString()
		require.NoError(t, db.Exec("INSERT INTO file (id,mime_type) VALUES (?,?)", files[index], "image/png").Error)
		if withFaces {
			channel.Faces = append(channel.Faces, &contentv1.FileAttachment{State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: files[index]}})
		}
	}
	document := &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: codec.Catalog().Fingerprint, Profile: contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST, Locale: "en",
		Base:          &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{node}},
		LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{locale}},
	}
	batch, issues, err := codec.Compile(created.Document.ID, document, core.LocaleRoleSource, core.Revision(created.Document.Revision.String()), uuid.New(), nil)
	require.NoError(t, err)
	require.Empty(t, issues)
	snapshot := persistCodecBatchForTest(t, db, store, batch)
	document, err = contentblock.SnapshotToLocalizedRichTextDocument(snapshot, "en")
	require.NoError(t, err)
	return cubemapCodecFixture{codec, db, store, snapshot, document, block, files}
}

func (fixture cubemapCodecFixture) service(t *testing.T) (*core.Service, *exactPostDocumentAPI, core.DocumentIdentity) {
	t.Helper()
	identity := core.DocumentIdentity{Domain: core.DomainPost, Reference: core.DocumentReference(uuid.NewString())}
	owner := &exactPostDocumentAPI{state: postdomain.AIDocumentState{
		PostID: string(identity.Reference), ContentDocumentID: fixture.snapshot.Document.ID.String(),
		DocumentRevision: fixture.snapshot.Document.Revision.String(), ViewerMemberID: uuid.NewString(),
		SourceLocale: "en", RequestedLocale: "en", LocaleExists: true, LocalizedDocument: fixture.document,
	}}
	service, err := core.NewService(&postPort{service: owner, codec: fixture.codec, catalog: postCatalog(fixture.codec)})
	require.NoError(t, err)
	return service, owner, identity
}

func cubemapFaceTarget(block core.BlockID, handle core.RelationItemID) core.FieldTarget {
	return core.FieldTarget{Block: block, Field: "stages", Path: []core.FieldPathSegment{
		core.ListPath("common"), core.ObjectPath("channels"), core.ListPath("channel-a"), core.ObjectPath("faces"), core.ListPath(handle),
	}}
}

func (fixture cubemapCodecFixture) request(identity core.DocumentIdentity, operations ...core.Operation) core.ApplyRequest {
	return core.ApplyRequest{Protocol: core.ProtocolVersion, Profile: identity.Domain, Document: identity.Reference,
		Locale: "en", ExpectedDocumentRevision: core.Revision(fixture.snapshot.Document.Revision.String()), Operations: operations}
}

func (fixture cubemapCodecFixture) assertReadable(t *testing.T, wantFiles []string) {
	t.Helper()
	service, _, identity := fixture.service(t)
	_, err := service.Open(t.Context(), core.OpenRequest{Document: identity, Locale: "en"})
	require.NoError(t, err)
	read, err := service.Read(t.Context(), core.ReadRequest{Document: identity, Locale: "en", Mode: core.ReadBlocks, Blocks: []core.BlockID{fixture.block}})
	require.NoError(t, err)
	_, err = json.Marshal(read)
	require.NoError(t, err)
	nodes, err := fixture.codec.Project(fixture.document)
	require.NoError(t, err)
	gotFiles := make(map[string]string)
	for _, binding := range nodes[0].Files {
		gotFiles[string(binding.Path[len(binding.Path)-1].Item)] = string(binding.File)
	}
	for index, handle := range cubemapFaceHandles {
		require.Equal(t, wantFiles[index], gotFiles[string(handle)], string(handle))
	}
	stages := pageTestNodeField(t, nodes[0].Shared, "stages")
	channels, _ := coreObjectValue(stages.List[0].Value, "channels")
	_, present := coreObjectValue(channels.List[0].Value, "faces")
	require.False(t, present, "File-only arrays must not create typed values")
}

func TestRichTextCubemapFilesReadAndIndividualAttachRoundTrip(t *testing.T) {
	fixture := newCubemapCodecFixture(t, true)
	fixture.assertReadable(t, fixture.files)
	replacement := uuid.NewString()
	require.NoError(t, fixture.db.Exec("INSERT INTO file (id,mime_type) VALUES (?,?)", replacement, "image/png").Error)
	service, owner, identity := fixture.service(t)
	operation := core.Operation{Kind: core.OperationAttachFile, AttachFile: &core.AttachFile{Target: cubemapFaceTarget(fixture.block, "px"), File: core.FileReference(replacement)}}
	validation, err := service.Validate(t.Context(), fixture.request(identity, operation))
	require.NoError(t, err)
	require.True(t, validation.Valid(), "%+v", validation)
	fixture.snapshot = persistCodecBatchForTest(t, fixture.db, fixture.store, owner.mutation.Batch)
	fixture.document, err = contentblock.SnapshotToLocalizedRichTextDocument(fixture.snapshot, "en")
	require.NoError(t, err)
	fixture.files[0] = replacement
	fixture.assertReadable(t, fixture.files)
	faces := fixture.document.Base.Nodes[0].Block.GetShader().Props.Stages[0].Channels[0].Faces
	require.Len(t, faces, 6)
	for index, face := range faces {
		require.Equal(t, fixture.files[index], face.GetActiveFileId())
	}
}

func TestRichTextCubemapOmittedFacesAttachInOneValidatedBatch(t *testing.T) {
	fixture := newCubemapCodecFixture(t, false)
	fixture.assertReadable(t, make([]string, 6))
	service, owner, identity := fixture.service(t)
	operations := make([]core.Operation, 0, 6)
	for index, handle := range cubemapFaceHandles {
		operations = append(operations, core.Operation{Kind: core.OperationAttachFile, AttachFile: &core.AttachFile{Target: cubemapFaceTarget(fixture.block, handle), File: core.FileReference(fixture.files[index])}})
	}
	validation, err := service.Validate(t.Context(), fixture.request(identity, operations...))
	require.NoError(t, err)
	require.True(t, validation.Valid(), "%+v", validation)
	fixture.snapshot = persistCodecBatchForTest(t, fixture.db, fixture.store, owner.mutation.Batch)
	fixture.document, err = contentblock.SnapshotToLocalizedRichTextDocument(fixture.snapshot, "en")
	require.NoError(t, err)
	fixture.assertReadable(t, fixture.files)
}

func TestRichTextCubemapDetachRejectsIncompleteFixedArrayWithoutPersistence(t *testing.T) {
	fixture := newCubemapCodecFixture(t, true)
	before := proto.Clone(fixture.document)
	service, owner, identity := fixture.service(t)
	operation := core.Operation{Kind: core.OperationDetachFile, DetachFile: &core.DetachFile{Target: cubemapFaceTarget(fixture.block, "px")}}
	validation, err := service.Validate(t.Context(), fixture.request(identity, operation))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Contains(t, err.Error(), "File attachment state is required")
	require.Empty(t, validation.Issues, "batch-level generated rejection must not invent an operation index")
	require.Empty(t, owner.mutation.Batch.Upserts)
	require.True(t, proto.Equal(before, fixture.document))
	snapshot, err := fixture.store.LoadSnapshot(t.Context(), fixture.db, fixture.snapshot.Document.ID, "en")
	require.NoError(t, err)
	require.Equal(t, fixture.snapshot.Document.Revision, snapshot.Document.Revision)
	fixture.assertReadable(t, fixture.files)
}

func TestRichTextCubemapRestoredMissingFaceRemainsReadableWithoutActiveBinding(t *testing.T) {
	fixture := newCubemapCodecFixture(t, true)
	fixture.restoreMissingFace(t)

	missing := fixture.document.Base.Nodes[0].Block.GetShader().Props.Stages[0].Channels[0].Faces[0].GetMissingAttachment()
	require.Equal(t, fixture.files[0], missing.GetFormerFileId())
	wantFiles := append([]string(nil), fixture.files...)
	wantFiles[0] = ""
	fixture.assertReadable(t, wantFiles)

	replacement := uuid.NewString()
	require.NoError(t, fixture.db.Exec("INSERT INTO file (id,mime_type) VALUES (?,?)", replacement, "image/png").Error)
	service, owner, identity := fixture.service(t)
	operation := core.Operation{Kind: core.OperationAttachFile, AttachFile: &core.AttachFile{Target: cubemapFaceTarget(fixture.block, "px"), File: core.FileReference(replacement)}}
	validation, err := service.Validate(t.Context(), fixture.request(identity, operation))
	require.NoError(t, err)
	require.True(t, validation.Valid(), "%+v", validation)
	fixture.snapshot = persistCodecBatchForTest(t, fixture.db, fixture.store, owner.mutation.Batch)
	fixture.document, err = contentblock.SnapshotToLocalizedRichTextDocument(fixture.snapshot, "en")
	require.NoError(t, err)
	wantFiles[0] = replacement
	fixture.assertReadable(t, wantFiles)
}

func (fixture *cubemapCodecFixture) restoreMissingFace(t *testing.T) {
	t.Helper()
	restored, err := contentblock.ReplaceFromLocalizedRichTextProtoWithUnavailableAttachments(
		fixture.snapshot.Document.ID, fixture.snapshot.Document.Revision, fixture.document,
		map[uuid.UUID]contentv1.MissingAttachmentMediaKind{uuid.MustParse(fixture.files[0]): contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_IMAGE},
	)
	require.NoError(t, err)
	require.NoError(t, fixture.db.Transaction(func(tx *gorm.DB) error {
		_, err := fixture.store.ReplaceSnapshot(t.Context(), tx, restored, func(context.Context, *gorm.DB, uuid.UUID) (contentblock.DomainContext, error) {
			return contentblock.DomainContext{SourceLocale: "en"}, nil
		})
		return err
	}))
	fixture.snapshot, err = fixture.store.LoadSnapshot(t.Context(), fixture.db, fixture.snapshot.Document.ID, "en")
	require.NoError(t, err)
	fixture.document, err = contentblock.SnapshotToLocalizedRichTextDocument(fixture.snapshot, "en")
	require.NoError(t, err)
}

func (fixture *cubemapCodecFixture) validateAndPersist(t *testing.T, operations ...core.Operation) {
	t.Helper()
	service, owner, identity := fixture.service(t)
	validation, err := service.Validate(t.Context(), fixture.request(identity, operations...))
	require.NoError(t, err)
	require.True(t, validation.Valid(), "%+v", validation)
	fixture.snapshot = persistCodecBatchForTest(t, fixture.db, fixture.store, owner.mutation.Batch)
	fixture.document, err = contentblock.SnapshotToLocalizedRichTextDocument(fixture.snapshot, "en")
	require.NoError(t, err)
}

func TestRichTextCubemapAncestorReplacementPreservesSeparateFileStates(t *testing.T) {
	for _, test := range []struct {
		name                  string
		missing, changeSource bool
	}{
		{name: "same stages"}, {name: "changed nonfile leaf", changeSource: true},
		{name: "restored missing state", missing: true, changeSource: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCubemapCodecFixture(t, true)
			if test.missing {
				fixture.restoreMissingFace(t)
			}
			facesBefore := proto.Clone(fixture.document.Base.Nodes[0].Block.GetShader().Props.Stages[0].Channels[0])
			nodes, err := fixture.codec.Project(fixture.document)
			require.NoError(t, err)
			stages := pageTestNodeField(t, nodes[0].Shared, "stages")
			if test.changeSource {
				for index := range stages.List[0].Value.Object {
					if stages.List[0].Value.Object[index].ID == "source" {
						stages.List[0].Value.Object[index].Value = core.Text("void main() { /* edited */ }")
					}
				}
			}
			operation := core.SetFieldOperation(fixture.block, "stages", stages)
			if test.missing {
				working := proto.Clone(fixture.document).(*contentv1.LocalizedRichTextDocument)
				require.NoError(t, fixture.codec.applyOperation(working, operation, map[string]struct{}{}))
				require.True(t, proto.Equal(facesBefore, working.Base.Nodes[0].Block.GetShader().Props.Stages[0].Channels[0]))
				service, owner, identity := fixture.service(t)
				_, err := service.Validate(t.Context(), fixture.request(identity, operation))
				require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
				require.Contains(t, err.Error(), "restore_only")
				require.Empty(t, owner.mutation.Batch.Upserts)
				stored, err := fixture.store.LoadSnapshot(t.Context(), fixture.db, fixture.snapshot.Document.ID, "en")
				require.NoError(t, err)
				require.Equal(t, fixture.snapshot.Document.Revision, stored.Document.Revision)
				fixture.assertReadable(t, append([]string{""}, fixture.files[1:]...))
				return
			}
			fixture.validateAndPersist(t, operation)
			channel := fixture.document.Base.Nodes[0].Block.GetShader().Props.Stages[0].Channels[0]
			require.True(t, proto.Equal(facesBefore, channel), "all active and restored File states must survive")
			fixture.assertReadable(t, fixture.files)
			if test.changeSource {
				require.Equal(t, "void main() { /* edited */ }", fixture.document.Base.Nodes[0].Block.GetShader().Props.Stages[0].Source)
			}
		})
	}
}

func TestRichTextCubemapChannelArrayReplacementPreservesTextureFileAndClearsOmittedObject(t *testing.T) {
	fixture := newCubemapCodecFixture(t, false)
	target := core.FieldTarget{Block: fixture.block, Field: "stages", Path: []core.FieldPathSegment{core.ListPath("common"), core.ObjectPath("channels"), core.ListPath("channel-a"), core.ObjectPath("kind")}}
	fileTarget := target
	fileTarget.Path = append([]core.FieldPathSegment(nil), target.Path...)
	fileTarget.Path[len(fileTarget.Path)-1] = core.ObjectPath("file")
	fixture.validateAndPersist(t,
		core.Operation{Kind: core.OperationSetField, SetField: &core.SetField{Target: target, Value: core.Text("textureFile")}},
		core.Operation{Kind: core.OperationAttachFile, AttachFile: &core.AttachFile{Target: fileTarget, File: core.FileReference(fixture.files[0])}},
	)
	// An optional regular selector must retain replacement semantics even though
	// the separate File selector survives the enclosing object replacement.
	samplerTarget := target
	samplerTarget.Path = append([]core.FieldPathSegment(nil), target.Path...)
	samplerTarget.Path[len(samplerTarget.Path)-1] = core.ObjectPath("sampler")
	fixture.validateAndPersist(t, core.Operation{Kind: core.OperationSetField, SetField: &core.SetField{Target: samplerTarget, Value: core.Object(core.ObjectValue("filter", core.Text("nearest")))}})
	nodes, err := fixture.codec.Project(fixture.document)
	require.NoError(t, err)
	stages := pageTestNodeField(t, nodes[0].Shared, "stages")
	channels, present := coreObjectValue(stages.List[0].Value, "channels")
	require.True(t, present)
	channels.List[0].Value = core.Object(core.ObjectValue("kind", core.Text("textureFile")))
	channelTarget := target
	channelTarget.Path = target.Path[:len(target.Path)-2]
	fixture.validateAndPersist(t, core.Operation{Kind: core.OperationSetField, SetField: &core.SetField{Target: channelTarget, Value: channels}})

	channel := fixture.document.Base.Nodes[0].Block.GetShader().Props.Stages[0].Channels[0]
	require.Equal(t, fixture.files[0], channel.GetFile().GetActiveFileId())
	field := findMessageField(channel.ProtoReflect(), "sampler")
	require.False(t, channel.ProtoReflect().Has(field), "omitted regular field must be removed")
	nodes, err = fixture.codec.Project(fixture.document)
	require.NoError(t, err)
	require.Len(t, nodes[0].Files, 1)
	require.Equal(t, fileTarget.Path, nodes[0].Files[0].Path)
	service, _, identity := fixture.service(t)
	_, err = service.Open(t.Context(), core.OpenRequest{Document: identity, Locale: "en"})
	require.NoError(t, err)
	_, err = service.Read(t.Context(), core.ReadRequest{Document: identity, Locale: "en", Mode: core.ReadBlocks, Blocks: []core.BlockID{fixture.block}})
	require.NoError(t, err)
}

func TestRichTextCubemapAncestorReplacementDoesNotRecreateRemovedChannels(t *testing.T) {
	fixture := newCubemapCodecFixture(t, true)
	nodes, err := fixture.codec.Project(fixture.document)
	require.NoError(t, err)
	stages := pageTestNodeField(t, nodes[0].Shared, "stages")
	working := proto.Clone(fixture.document).(*contentv1.LocalizedRichTextDocument)
	removeStage := core.SetFieldOperation(fixture.block, "stages", core.List(stages.List[1:]...))
	require.NoError(t, fixture.codec.applyOperation(working, removeStage, map[string]struct{}{}))
	require.Len(t, working.Base.Nodes[0].Block.GetShader().Props.Stages, 8)
	removedNodes, err := fixture.codec.Project(working)
	require.NoError(t, err)
	require.Empty(t, removedNodes[0].Files, "a removed stable parent item must not be recreated")
	var fields []core.ObjectField
	for _, field := range stages.List[0].Value.Object {
		if field.ID != "channels" {
			fields = append(fields, field)
		}
	}
	stages.List[0].Value = core.Object(fields...)
	fixture.validateAndPersist(t, core.SetFieldOperation(fixture.block, "stages", stages))
	require.Empty(t, fixture.document.Base.Nodes[0].Block.GetShader().Props.Stages[0].Channels)
	fixture.assertReadableWithoutChannels(t)
}

func (fixture cubemapCodecFixture) assertReadableWithoutChannels(t *testing.T) {
	t.Helper()
	service, _, identity := fixture.service(t)
	_, err := service.Open(t.Context(), core.OpenRequest{Document: identity, Locale: "en"})
	require.NoError(t, err)
	_, err = service.Read(t.Context(), core.ReadRequest{Document: identity, Locale: "en", Mode: core.ReadBlocks, Blocks: []core.BlockID{fixture.block}})
	require.NoError(t, err)
	nodes, err := fixture.codec.Project(fixture.document)
	require.NoError(t, err)
	require.Empty(t, nodes[0].Files)
}
