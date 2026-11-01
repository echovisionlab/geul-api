//go:build integration

package legal_test

import (
	"github.com/google/uuid"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

func legalPolicyDocumentFixture(sourceLocale string, text string) *contentv1.RichTextDocument {
	blockID := uuid.NewString()
	return &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY,
		SourceLocale:            sourceLocale,
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{
				Id: blockID,
				Value: &contentv1.RichTextBlock_Paragraph{
					Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}},
				},
			},
			Placement: &contentv1.ContentBlockPlacement{Index: 0},
		}}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{
			Locale: sourceLocale,
			Blocks: []*contentv1.RichTextBlockLocale{{
				BlockId: blockID,
				Value: &contentv1.RichTextBlockLocale_Paragraph{
					Paragraph: &contentv1.ParagraphBlockLocale{
						Props: &contentv1.ParagraphLocaleProps{},
						Content: []*contentv1.RichTextInline{{
							Value: &contentv1.RichTextInline_Text{
								Text: &contentv1.RichTextStyledText{Text: text},
							},
						}},
					},
				},
			}},
		}},
	}
}
