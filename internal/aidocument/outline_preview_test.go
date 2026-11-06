package aidocument

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSearchNodesFindsALinkByItsAddress(t *testing.T) {
	linked := Node{ID: "linked", Localized: []FieldValue{{ID: "content", Value: RichText(Link("/products/topofon/support", InlineText("지원 문의")))}}}
	plain := Node{ID: "plain", Localized: []FieldValue{{ID: "content", Value: RichText(InlineText("지원 문의"))}}}

	matches := searchNodes([]Node{linked, plain}, "/products/TOPOFON")

	if len(matches) != 1 || !strings.Contains(matches["linked"], "지원 문의") || !strings.Contains(matches["linked"], "/products/topofon/support") {
		t.Fatalf("matches = %v", matches)
	}
}

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

func TestOutlineSearchKeepsMatchingBlocksAndEncodesNamedBlocks(t *testing.T) {
	port := &fakePort{document: testDocument("ko", true)}
	service, err := NewService(port)
	if err != nil {
		t.Fatal(err)
	}
	found, err := service.Read(t.Context(), ReadRequest{Document: port.document.Identity, Locale: "ko", Mode: ReadOutline, Query: "SEE   this"})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := EncodeProjection(found)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Query string            `json:"query"`
		Nodes []json.RawMessage `json:"blocks"`
	}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Query != "SEE   this" || len(decoded.Nodes) != 1 || string(decoded.Nodes[0]) != `{"block_id":"paragraph-a","kind":"paragraph","parent_block_id":"root","order":0,"preview":"First See thislink x^2"}` {
		t.Fatalf("search outline = %s", wire)
	}

	all, err := service.Read(t.Context(), ReadRequest{Document: port.document.Identity, Locale: "ko", Mode: ReadOutline})
	if err != nil {
		t.Fatal(err)
	}
	wire, err = EncodeProjection(all)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wire), `{"block_id":"root","kind":"document","order":0,"preview":"Hello","relations":[{"relation_id":"credits","items":[{"item_id":"credit-primary","kind":"credit","order":0},{"item_id":"credit-secondary","kind":"credit","order":1}]}]}`) ||
		strings.Contains(string(wire), "null") && !strings.Contains(string(wire), `"next_cursor":null`) {
		t.Fatalf("outline blocks are not named objects without empties: %s", wire)
	}

	if _, err := service.Read(t.Context(), ReadRequest{Document: port.document.Identity, Locale: "ko", Mode: ReadBlocks, Blocks: []BlockID{"root"}, Query: "x"}); err == nil {
		t.Fatal("a search outside an outline read was accepted")
	}
}
