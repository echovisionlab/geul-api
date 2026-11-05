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

func TestOutlineSearchKeepsMatchingBlocksAndEncodesCompactNodes(t *testing.T) {
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
		Query string            `json:"q"`
		Nodes []json.RawMessage `json:"n"`
	}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Query != "SEE   this" || len(decoded.Nodes) != 1 || string(decoded.Nodes[0]) != `["paragraph-a","paragraph","root",0,"First See thislink x^2"]` {
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
	if !strings.Contains(string(wire), `["root","document","",0,"Hello",[["credits",[["credit-primary","credit",0],["credit-secondary","credit",1]]]]]`) ||
		strings.Contains(string(wire), "null,null") {
		t.Fatalf("outline nodes are not compact: %s", wire)
	}

	if _, err := service.Read(t.Context(), ReadRequest{Document: port.document.Identity, Locale: "ko", Mode: ReadBlocks, Blocks: []BlockID{"root"}, Query: "x"}); err == nil {
		t.Fatal("a search outside an outline read was accepted")
	}
}
