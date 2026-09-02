//go:build integration

package testutil

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// NewCreativeContentStore constructs the compact Rich Text store used by
// cross-domain integration scenarios.
func NewCreativeContentStore(t *testing.T, spiceDB *auth.SpiceDBClient) *contentblock.Store {
	t.Helper()
	_ = spiceDB
	store, err := contentblock.NewGeneratedStore(creativeContentNoFileReuseAuthorizer{})
	require.NoError(t, err)
	return store
}

type creativeContentNoFileReuseAuthorizer struct{}

func (creativeContentNoFileReuseAuthorizer) AuthorizeFileReuse(
	context.Context,
	*gorm.DB,
	contentblock.Document,
	contentblock.FullBlock,
	contentblock.FileReference,
	contentblock.File,
) error {
	return errors.New("cross-domain creative content fixture does not authorize File reuse")
}

// CreativeContentDocument returns a compact Rich Text source document.
func CreativeContentDocument(sourceLocale, text string) *contentv1.RichTextDocument {
	blockID := uuid.NewString()
	return &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT,
		SourceLocale:            sourceLocale,
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_Paragraph{
				Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}},
			}},
			Placement: &contentv1.ContentBlockPlacement{Index: 0},
		}}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{Locale: sourceLocale, Blocks: []*contentv1.RichTextBlockLocale{{
			BlockId: blockID,
			Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
				Props: &contentv1.ParagraphLocaleProps{},
				Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
					Text: &contentv1.RichTextStyledText{Text: text},
				}}},
			}},
		}}}},
	}
}

// AttachCreativeContentDocument creates and attaches a compact source
// document to an integration fixture root.
func AttachCreativeContentDocument(
	t *testing.T,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	sourceLocale string,
	text string,
) string {
	t.Helper()
	var revision string
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		created, err := store.CreateDocument(t.Context(), tx, contentblock.CreateInput{
			Profile: "compact", SourceLocale: sourceLocale,
		})
		if err != nil {
			return err
		}
		if err := tx.Table(entityType).
			Where("id = ?::uuid", entityID).
			Update("content_document_id", created.Document.ID).Error; err != nil {
			return err
		}
		replacement, err := contentblock.ReplaceFromRichTextProto(
			created.Document.ID,
			created.Document.Revision,
			CreativeContentDocument(sourceLocale, text),
		)
		if err != nil {
			return err
		}
		result, err := store.ReplaceSnapshot(
			t.Context(), tx, replacement,
			func(context.Context, *gorm.DB, uuid.UUID) (contentblock.DomainContext, error) {
				return contentblock.DomainContext{SourceLocale: sourceLocale}, nil
			},
		)
		if err != nil {
			return err
		}
		revision = result.DocumentRevision.String()
		return nil
	}))
	return revision
}
