package aidocument

import (
	"strings"
	"testing"
)

func TestOutlinePreviewsUseLocaleTextOnly(t *testing.T) {
	long := strings.Repeat("가", outlinePreviewRunes+5)
	previews := outlinePreviews([]Node{
		{ID: "heading", Localized: []FieldValue{{ID: "content", Value: Value{Kind: ValueKindInline, Inline: []InlineItem{
			{Kind: InlineKindText, Text: "Setup "},
			{Kind: InlineKindBold, Children: []InlineItem{{Kind: InlineKindText, Text: "guide"}}},
			{Kind: InlineKindHardBreak},
			{Kind: InlineKindSiteVariable, Text: "site.title"},
		}}}}},
		{ID: "aligned", Shared: []FieldValue{{ID: "textAlignment", Value: Value{Kind: ValueKindText, Text: "center"}}}},
		{ID: "long", Localized: []FieldValue{{ID: "content", Value: Value{Kind: ValueKindText, Text: long}}}},
	})
	if previews["heading"] != "Setup guide {site.title}" {
		t.Fatalf("heading preview = %q", previews["heading"])
	}
	if _, ok := previews["aligned"]; ok {
		t.Fatal("shared values such as alignment are not previews")
	}
	if got := []rune(previews["long"]); len(got) != outlinePreviewRunes+1 || got[len(got)-1] != '…' {
		t.Fatalf("long preview = %q", previews["long"])
	}
}

func TestInlineFormattingAndExistingTargets(t *testing.T) {
	plain := Value{Kind: ValueKindInline, Inline: []InlineItem{{Kind: InlineKindText, Text: "plain"}}}
	linked := Value{Kind: ValueKindInline, Inline: []InlineItem{{Kind: InlineKindLink, Target: "https://example.com", Children: []InlineItem{{Kind: InlineKindText, Text: "x"}}}}}
	if HasInlineFormatting(plain) || !HasInlineFormatting(linked) {
		t.Fatal("only non-plain inline content counts as formatting")
	}
	blocks := ExistingTargetBlocks([]Operation{
		InsertBlockOperation("new", "paragraph", "section", ""),
		SetFieldOperation("new", "content", plain),
		SetFieldOperation("existing", "content", plain),
		DeleteBlockOperation("removed"),
		SetFieldOperation("existing", "content", plain),
	})
	if strings.Join([]string{string(blocks[0]), string(blocks[1])}, ",") != "existing,removed" || len(blocks) != 2 {
		t.Fatalf("targets = %v", blocks)
	}
}
