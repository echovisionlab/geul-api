package post

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestPostVersionCheckpointRequiresMigratedCatalog(t *testing.T) {
	document := testPostVersionLocalizedDocument()
	document.BlockCatalogFingerprint = contentv1.ContentBlockCatalogFingerprint
	encoded, _, err := marshalPostVersionContentSnapshot(document, "en", nil, nil)
	require.NoError(t, err)
	for _, fingerprint := range []string{"de190638c6014cc2ac80efce5f747b30d20cc024f3d0820c87ba6c1f1a56e16e", "2db3b07d50a139a670f68e3cd045063d20475a102acbee7852066b1929b7172b", "7ee996e5a23685d3b981268c3dd6608d8f006f5b62dbec5fb35e1b4d0532cf49", "ffc51f7192e406fe25fcb5383428be94e74a52db83c666b550ca38d4db88dc85", "stale-catalog"} {
		require.NotEqual(t, fingerprint, contentv1.ContentBlockCatalogFingerprint)
		stored := bytes.ReplaceAll(encoded, []byte(contentv1.ContentBlockCatalogFingerprint), []byte(fingerprint))
		_, decoded, err := unmarshalPostVersionContentSnapshot(stored)
		require.ErrorContains(t, err, "fingerprint")
		require.Nil(t, decoded)
	}
}

func TestPostVersionProtoFormatting(t *testing.T) {
	createdAt := time.Unix(1_700_000_000, 0).UTC()
	title := "Snapshot title"
	summary := "Snapshot summary"
	snapshot, canonicalHash, err := marshalPostVersionContentSnapshot(
		testPostVersionLocalizedDocument(), "en", &title, &summary,
	)
	require.NoError(t, err)

	version, err := (&PostService{}).toProtoPostVersion(&model.PostVersion{
		ID: "post-version-1", Version: 2, ContentSnapshot: snapshot,
		ContributorMemberIDs: []string{"member-2", "member-1", "member-2"},
		CreatedAt:            createdAt,
	}, map[string]string{"member-1": "Mina", "member-2": "Jules"})
	require.NoError(t, err)
	require.Equal(t, "post-version-1", version.Id)
	require.Equal(t, int32(2), version.Version)
	require.Equal(t, "en", version.SourceLocale)
	require.Equal(t, title, version.Title)
	require.Equal(t, &summary, version.Summary)
	require.Equal(t, canonicalHash, version.CanonicalHash)
	require.Equal(t, []string{"member-1", "member-2"}, []string{
		version.Contributors[0].MemberId,
		version.Contributors[1].MemberId,
	})
	require.Equal(t, createdAt.Unix(), version.CreatedAt.AsTime().Unix())
}

func TestPostVersionProtoFormattingRejectsMissingContributorNickname(t *testing.T) {
	snapshot, _, err := marshalPostVersionContentSnapshot(
		testPostVersionLocalizedDocument(), "en", nil, nil,
	)
	require.NoError(t, err)
	_, err = (&PostService{}).toProtoPostVersion(
		&model.PostVersion{ContentSnapshot: snapshot, ContributorMemberIDs: []string{"member-1"}},
		map[string]string{},
	)
	require.ErrorContains(t, err, "member-1 is unresolved")
}

func TestPostVersionContentSnapshotRoundTrip(t *testing.T) {
	title := "Source title"
	summary := "Source summary"
	document := testPostVersionLocalizedDocument()

	encoded, hash, err := marshalPostVersionContentSnapshot(document, "en", &title, &summary)
	require.NoError(t, err)
	require.Len(t, hash, 64)

	envelope, decoded, err := unmarshalPostVersionContentSnapshot(encoded)
	require.NoError(t, err)
	require.Equal(t, postVersionContentSnapshotSchemaVersion, envelope.SchemaVersion)
	require.Equal(t, "en", envelope.SourceLocale)
	require.Equal(t, title, *envelope.Title)
	require.Equal(t, summary, *envelope.Summary)
	require.True(t, proto.Equal(document, decoded))

	reencoded, rehash, err := marshalPostVersionContentSnapshot(
		decoded, envelope.SourceLocale, envelope.Title, envelope.Summary,
	)
	require.NoError(t, err)
	require.JSONEq(t, string(encoded), string(reencoded))
	require.Equal(t, hash, rehash)
}

func TestPostVersionContentSnapshotRejectsUnknownEnvelopeField(t *testing.T) {
	_, _, err := unmarshalPostVersionContentSnapshot(json.RawMessage(`{
		"schemaVersion":1,
		"sourceLocale":"en",
		"title":null,
		"summary":null,
		"document":{},
		"unknown":true
	}`))
	require.ErrorContains(t, err, "unknown field")
}

func testPostVersionLocalizedDocument() *contentv1.LocalizedRichTextDocument {
	blockID := "10000000-0000-4000-8000-000000000001"
	return &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		Locale:                  "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{
				Id: blockID,
				Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{
					Props: &contentv1.ParagraphProps{},
				}},
			},
			Placement: &contentv1.ContentBlockPlacement{},
		}}},
		LocaleOverlay: &contentv1.RichTextLocaleOverlay{
			Locale: "en",
			Blocks: []*contentv1.RichTextBlockLocale{{
				BlockId: blockID,
				Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
					Props: &contentv1.ParagraphLocaleProps{},
					Content: []*contentv1.RichTextInline{{
						Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "Hello"}},
					}},
				}},
			}},
		},
	}
}
