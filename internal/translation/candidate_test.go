package translation

import (
	"testing"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

func TestCandidateRichTextLocaleMutationsKeepsExplicitEmptyUpsertAndOmittedDeleteAtomic(t *testing.T) {
	explicitEmptyID, omittedID := "block-empty", "block-omitted"
	candidate := &Candidate{
		ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{
			Locale: "en",
			Blocks: []*contentv1.RichTextBlockLocale{{
				BlockId: explicitEmptyID,
				Value: &contentv1.RichTextBlockLocale_Paragraph{
					Paragraph: &contentv1.ParagraphBlockLocale{},
				},
			}},
		},
		ContentBlockLocaleDeletes: []string{omittedID},
	}

	mutations := candidate.RichTextLocaleMutations()
	if len(mutations) != 2 {
		t.Fatalf("locale mutations = %d, want 2", len(mutations))
	}
	upsert := mutations[0].GetUpsert().GetBlock()
	if upsert.GetBlockId() != explicitEmptyID || upsert.GetParagraph() == nil {
		t.Fatalf("explicit-empty upsert = %+v", upsert)
	}
	if got := mutations[1].GetDelete().GetBlockId(); got != omittedID {
		t.Fatalf("omitted delete = %q, want %q", got, omittedID)
	}
}
