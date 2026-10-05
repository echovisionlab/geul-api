package aidocumentadapter

import (
	"fmt"
	"strconv"
	"testing"

	"connectrpc.com/connect"
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestRichTextIntegerConversionHonorsProtobufWidth(t *testing.T) {
	field := findMessageFieldDescriptor((&contentv1.ParagraphProps{}).ProtoReflect().Descriptor(), "previewWidth")
	descriptor := contentv1.ContentFieldDescriptor{Type: "integer"}
	for _, input := range []string{"-2147483648", "0", "2147483647"} {
		converted, err := scalarProtoValue(field, descriptor, core.Number(input))
		require.NoError(t, err, input)
		require.Equal(t, input, strconv.FormatInt(converted.Int(), 10))
	}
	for _, input := range []string{"2147483648", "-2147483649", "4294967346", "-4294967246", "1.5"} {
		_, err := scalarProtoValue(field, descriptor, core.Number(input))
		require.Error(t, err, input)
	}
}

func TestRichTextCodecRejectsIntegerOverflowBeforePersistence(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST)
	require.NoError(t, err)
	block := uuid.New()
	document := localizedParagraphDocument(block, "Before")
	for _, input := range []string{"9", "101"} {
		batch, issues, err := codec.Compile(uuid.New(), document, core.LocaleRoleSource, core.Revision(uuid.NewString()), uuid.New(), []core.Operation{
			core.SetFieldOperation(core.BlockID(block.String()), "previewWidth", core.Number(input)),
		})
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), input)
		require.Empty(t, issues)
		require.Empty(t, batch.Upserts)
	}
	for _, input := range []string{"4294967346", "-4294967246"} {
		operation := core.SetFieldOperation(core.BlockID(block.String()), "previewWidth", core.Number(input))
		validateRichTextOperationForTest(t, codec, document, operation)
		batch, issues, err := codec.Compile(uuid.New(), document, core.LocaleRoleSource, core.Revision(uuid.NewString()), uuid.New(), []core.Operation{operation})
		require.NoError(t, err)
		require.Len(t, issues, 1)
		require.Equal(t, 0, issues[0].Operation)
		require.Equal(t, core.IssueInvalidOperation, issues[0].Code)
		require.Contains(t, issues[0].Message, "value out of range")
		require.Empty(t, batch.Upserts)
	}
	db, store, created := newCodecStoreForTest(t, "post")
	batch, issues, err := codec.Compile(created.Document.ID, document, core.LocaleRoleSource, core.Revision(created.Document.Revision.String()), uuid.New(), []core.Operation{
		core.SetFieldOperation(core.BlockID(block.String()), "previewWidth", core.Number("50")),
	})
	require.NoError(t, err)
	require.Empty(t, issues)
	snapshot := persistCodecBatchForTest(t, db, store, batch)
	loaded, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, "en")
	require.NoError(t, err)
	require.Equal(t, int32(50), loaded.Base.Nodes[0].Block.GetParagraph().GetProps().GetPreviewWidth())
	nodes, err := codec.Project(loaded)
	require.NoError(t, err)
	require.Equal(t, core.Number("50"), pageTestNodeField(t, nodes[0].Shared, "previewWidth"))
}

func TestRichTextGeneratedCatalogEnumsRoundTripExactly(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST)
	require.NoError(t, err)
	checked := 0
	var checkFields func(protoreflect.MessageDescriptor, []contentv1.ContentFieldDescriptor)
	var checkValue func(protoreflect.FieldDescriptor, contentv1.ContentFieldDescriptor)
	checkValue = func(field protoreflect.FieldDescriptor, descriptor contentv1.ContentFieldDescriptor) {
		switch descriptor.Type {
		case "enum", "enum_int":
			for _, candidate := range descriptor.Values {
				text := fmt.Sprint(candidate)
				value := core.Text(text)
				if descriptor.Type == "enum_int" {
					value = core.Number(text)
				}
				converted, err := scalarProtoValue(field, descriptor, value)
				require.NoError(t, err, "%s = %s", field.FullName(), text)
				projected, err := projectScalarValue(converted, field, descriptor)
				require.NoError(t, err)
				require.Equal(t, value, projected, "%s = %s", field.FullName(), text)
				checked++
			}
		case "object":
			checkFields(field.Message(), descriptor.Fields)
		case "array":
			require.NotNil(t, descriptor.Item)
			checkValue(field, *descriptor.Item)
		}
	}
	checkFields = func(message protoreflect.MessageDescriptor, descriptors []contentv1.ContentFieldDescriptor) {
		for _, descriptor := range descriptors {
			field := findMessageFieldDescriptor(message, descriptor.Name)
			require.NotNil(t, field, "%s.%s", message.FullName(), descriptor.Name)
			checkValue(field, descriptor)
		}
	}
	for _, descriptor := range codec.descriptor.Blocks {
		base, locale, err := codec.newBlock(core.BlockKind(descriptor.Kind), uuid.NewString())
		require.NoError(t, err)
		_, baseMessage, err := codec.blockMessage(base.Block.ProtoReflect())
		require.NoError(t, err)
		_, localeMessage, err := codec.localeBlockMessage(locale.ProtoReflect())
		require.NoError(t, err)
		for _, field := range descriptor.Fields {
			message := baseMessage
			if field.Ownership == "locale" {
				message = localeMessage
			}
			props := findMessageField(message, "props")
			require.NotNil(t, props, "%s props", descriptor.Kind)
			checkFields(props.Message(), []contentv1.ContentFieldDescriptor{field})
		}
	}
	checkFields((&contentv1.RichTextTableBase{}).ProtoReflect().Descriptor(), codec.descriptor.Table.Fields)
	checkFields((&contentv1.RichTextTableCellProps{}).ProtoReflect().Descriptor(), codec.descriptor.Table.CellFields)
	require.Positive(t, checked)
	t.Logf("round-tripped %d generated enum values, including nested arrays and table cells", checked)
}

func TestRichTextCodecObjectiveCLanguagePersistsAndReadsBackExactly(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST)
	require.NoError(t, err)
	block := uuid.NewString()
	base, locale, err := codec.newBlock("code-block", block)
	require.NoError(t, err)
	base.Placement = &contentv1.ContentBlockPlacement{}
	document := &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: codec.Catalog().Fingerprint, Profile: contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST, Locale: "en",
		Base:          &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{base}},
		LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{locale}},
	}
	operation := core.SetFieldOperation(core.BlockID(block), "language", core.Text("objective-c"))
	validateRichTextOperationForTest(t, codec, document, operation)
	db, store, created := newCodecStoreForTest(t, "post")
	batch, issues, err := codec.Compile(created.Document.ID, document, core.LocaleRoleSource, core.Revision(created.Document.Revision.String()), uuid.New(), []core.Operation{operation})
	require.NoError(t, err)
	require.Empty(t, issues)
	snapshot := persistCodecBatchForTest(t, db, store, batch)
	loaded, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, "en")
	require.NoError(t, err)
	require.Equal(t, contentv1.CodeBlockProps_LANGUAGE_OBJECTIVE_C, loaded.Base.Nodes[0].Block.GetCodeBlock().GetProps().GetLanguage())
	nodes, err := codec.Project(loaded)
	require.NoError(t, err)
	require.Equal(t, core.Text("objective-c"), pageTestNodeField(t, nodes[0].Shared, "language"))
}
