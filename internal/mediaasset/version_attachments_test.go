package mediaasset

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

func TestLoadUnavailableVersionAttachmentKindsUsesFileAuthority(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE file (
		id TEXT PRIMARY KEY,
		mime_type TEXT NOT NULL,
		delete_requested_at DATETIME
	)`).Error)

	availableID := uuid.New()
	pendingID := uuid.New()
	missingID := uuid.New()
	require.NoError(t, db.Exec(
		`INSERT INTO file (id, mime_type, delete_requested_at) VALUES (?, 'image/png', NULL), (?, 'audio/wav', ?)`,
		availableID.String(), pendingID.String(), time.Now().UTC(),
	).Error)

	document := versionAttachmentTestDocument(availableID, pendingID, missingID)
	unavailable, err := LoadUnavailableVersionAttachmentKinds(t.Context(), db, document)
	require.NoError(t, err)
	require.NotContains(t, unavailable, availableID)
	require.Equal(
		t,
		contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_AUDIO,
		unavailable[pendingID],
	)
	require.Equal(
		t,
		contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_FILE,
		unavailable[missingID],
	)
}

func versionAttachmentTestDocument(fileIDs ...uuid.UUID) *contentv1.LocalizedRichTextDocument {
	nodes := make([]*contentv1.RichTextBlockNode, 0, len(fileIDs))
	locales := make([]*contentv1.RichTextBlockLocale, 0, len(fileIDs))
	for index, fileID := range fileIDs {
		blockID := uuid.NewString()
		nodes = append(nodes, &contentv1.RichTextBlockNode{
			Block: &contentv1.RichTextBlock{
				Id: blockID,
				Value: &contentv1.RichTextBlock_File{File: &contentv1.FileBlock{Props: &contentv1.FileProps{
					Attachment: &contentv1.FileAttachment{State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: fileID.String()}},
					Name:       versionAttachmentStringPointer("attachment"),
				}}},
			},
			Placement: &contentv1.ContentBlockPlacement{Index: uint32(index)},
		})
		locales = append(locales, &contentv1.RichTextBlockLocale{
			BlockId: blockID,
			Value: &contentv1.RichTextBlockLocale_File{File: &contentv1.FileBlockLocale{Props: &contentv1.FileLocaleProps{
				Alt: versionAttachmentStringPointer("alt"), Caption: versionAttachmentStringPointer("caption"),
			}}},
		})
	}
	return &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		Locale:                  "en",
		Base:                    &contentv1.RichTextBlockGraph{Nodes: nodes},
		LocaleOverlay:           &contentv1.RichTextLocaleOverlay{Locale: "en", Blocks: locales},
	}
}

func versionAttachmentStringPointer(value string) *string { return &value }
