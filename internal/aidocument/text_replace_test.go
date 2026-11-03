package aidocument

import (
	"reflect"
	"strings"
	"testing"
)

func TestReplaceTextOnceKeepsFormattingAroundTheMatch(t *testing.T) {
	nodes := []Node{
		{ID: "intro", Kind: "paragraph", Localized: []FieldValue{{ID: "content", Value: RichText(
			InlineText("Turn on "), Bold(InlineText("route recording")), InlineText(" before you start."),
		)}}},
		{ID: "outro", Kind: "paragraph", Localized: []FieldValue{{ID: "content", Value: Text("Stop when you finish.")}}},
	}

	replaced, err := ReplaceTextOnce(nodes, "route recording", "path logging")
	if err != nil {
		t.Fatal(err)
	}
	want := RichText(InlineText("Turn on "), Bold(InlineText("path logging")), InlineText(" before you start."))
	set := replaced.Operation.SetField
	if replaced.Block != "intro" || !reflect.DeepEqual(set.Target, FieldTarget{Block: "intro", Field: "content"}) || !reflect.DeepEqual(set.Value, want) {
		t.Fatalf("replacement = %+v %+v", replaced, set)
	}

	emptied, err := ReplaceTextOnce(nodes, "route recording", "")
	if err != nil || !reflect.DeepEqual(emptied.Operation.SetField.Value, RichText(InlineText("Turn on "), InlineText(" before you start."))) {
		t.Fatalf("deleting a whole bold run left %+v, %v", emptied.Operation.SetField, err)
	}

	for find, message := range map[string]string{
		"Turn on route": "crosses a mark, link, color or break in block intro",
		"you":           "occurs 2 times (blocks intro, outro)",
		"missing":       "does not occur",
	} {
		if _, err := ReplaceTextOnce(nodes, find, "x"); err == nil || !strings.Contains(err.Error(), message) {
			t.Fatalf("find %q error = %v, want %q", find, err, message)
		}
	}
}
