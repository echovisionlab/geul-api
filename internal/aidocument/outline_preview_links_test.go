package aidocument

import (
	"strings"
	"testing"
)

// A link inside a table cell or a relation item must be found like any other, or a
// site-wide link change would say it applied and leave these behind.
func TestNodeLinkAddressesReachesListsObjectsAndRelations(t *testing.T) {
	link := func(address string) Value {
		return Value{Kind: ValueKindInline, Inline: []InlineItem{{Kind: InlineKindLink, Target: address, Children: []InlineItem{InlineText("x")}}}}
	}
	node := Node{
		Localized: []FieldValue{
			{ID: "content", Value: link("https://top.example")},
			{ID: "cells", Value: Value{Kind: ValueKindList, List: []ListItem{{Value: Value{Kind: ValueKindObject, Object: []ObjectField{{ID: "content", Value: link("https://cell.example")}}}}}}},
		},
		Relations: []Relation{{Items: []RelationItem{{Localized: []FieldValue{{ID: "caption", Value: link("https://item.example")}}}}}},
	}

	addresses := nodeLinkAddresses(node)

	for _, want := range []string{"https://top.example", "https://cell.example", "https://item.example"} {
		if !strings.Contains(addresses, want) {
			t.Errorf("%s is missing from %q", want, addresses)
		}
	}
}
