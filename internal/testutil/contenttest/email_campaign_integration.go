//go:build integration

package contenttest

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

type emailCampaignNoFileReuseAuthorizer struct{}

func (emailCampaignNoFileReuseAuthorizer) AuthorizeFileReuse(context.Context, *gorm.DB, contentblock.Document, contentblock.FullBlock, contentblock.FileReference, contentblock.File) error {
	return nil
}

func NewEmailContentBlockStore(t *testing.T) *contentblock.Store {
	t.Helper()
	store, err := contentblock.NewGeneratedStore(emailCampaignNoFileReuseAuthorizer{})
	require.NoError(t, err)
	return store
}

func NewParagraphBatch(document *contentv1.RichTextDocument, expectedRevision, locale, text string, contributors []string) *contentv1.RichTextBlockMutationBatch {
	return NewParagraphBlockBatch(document, expectedRevision, locale, uuid.NewString(), text, contributors)
}

// NewParagraphBlockBatch upserts paragraph blockID at the first position, so
// repeated batches for one block rewrite its text instead of adding blocks.
func NewParagraphBlockBatch(document *contentv1.RichTextDocument, expectedRevision, locale, blockID, text string, contributors []string) *contentv1.RichTextBlockMutationBatch {
	return &contentv1.RichTextBlockMutationBatch{BlockCatalogFingerprint: document.GetBlockCatalogFingerprint(), Profile: document.GetProfile(), ExpectedRevision: expectedRevision, ContributorMemberIds: contributors, BaseMutations: []*contentv1.RichTextBlockMutation{{Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{Node: &contentv1.RichTextBlockNode{Block: &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}}}}, Placement: &contentv1.ContentBlockPlacement{Index: 0}}}}}}, LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{Locale: locale, Mutations: []*contentv1.RichTextBlockLocaleMutation{{Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{Block: &contentv1.RichTextBlockLocale{BlockId: blockID, Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{Props: &contentv1.ParagraphLocaleProps{}, Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: text}}}}}}}}}}}}}}
}
