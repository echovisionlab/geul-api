package aidocument

import (
	"reflect"
	"strings"
	"testing"
)

func textEditNodes() []Node {
	return []Node{
		{ID: "intro", Kind: "paragraph", Localized: []FieldValue{{ID: "content", Value: RichText(
			InlineText("Turn on "), Bold(InlineText("route recording")), InlineText(" before you start."),
		)}}},
		{ID: "outro", Kind: "paragraph", Localized: []FieldValue{{ID: "content", Value: Text("Stop when you finish.")}}},
	}
}

func replaceEdit(find, replace string) TextEdit {
	return TextEdit{Find: find, Replace: &replace}
}

func TestPlanTextEditsReplaceKeepsFormattingAroundTheMatch(t *testing.T) {
	result, err := PlanTextEdits(textEditNodes(), []TextEdit{replaceEdit("route recording", "path logging")})
	if err != nil {
		t.Fatal(err)
	}
	want := RichText(InlineText("Turn on "), Bold(InlineText("path logging")), InlineText(" before you start."))
	if len(result.Operations) != 1 {
		t.Fatalf("operations = %+v", result.Operations)
	}
	set := result.Operations[0].SetField
	if !reflect.DeepEqual(set.Target, FieldTarget{Block: "intro", Field: "content"}) || !reflect.DeepEqual(set.Value, want) {
		t.Fatalf("set = %+v", set)
	}

	emptied, err := PlanTextEdits(textEditNodes(), []TextEdit{replaceEdit("route recording", "")})
	if err != nil || !reflect.DeepEqual(emptied.Operations[0].SetField.Value, RichText(InlineText("Turn on "), InlineText(" before you start."))) {
		t.Fatalf("deleting a whole bold run left %+v, %v", emptied.Operations, err)
	}
}

func TestPlanTextEditsReplaceExplainsWhyNothingChanged(t *testing.T) {
	for find, message := range map[string]string{
		"Turn on route": "crosses a mark, link, color or break in block intro",
		"you":           "occurs 2 times (blocks intro, outro)",
		"missing":       "does not occur",
	} {
		if _, err := PlanTextEdits(textEditNodes(), []TextEdit{replaceEdit(find, "x")}); err == nil || !strings.Contains(err.Error(), message) {
			t.Fatalf("find %q error = %v, want %q", find, err, message)
		}
	}
}

// A second edit of the same field must build on the first, and the batch must
// write that field once: two writes of a whole value would lose the first edit.
func TestPlanTextEditsCombinesEditsOfOneFieldIntoOneWrite(t *testing.T) {
	result, err := PlanTextEdits(textEditNodes(), []TextEdit{
		replaceEdit("Turn on", "Enable"),
		replaceEdit("route recording", "path logging"),
		replaceEdit("Stop", "Pause"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Operations) != 2 {
		t.Fatalf("want one write per changed field, got %+v", result.Operations)
	}
	want := RichText(InlineText("Enable "), Bold(InlineText("path logging")), InlineText(" before you start."))
	if !reflect.DeepEqual(result.Operations[0].SetField.Value, want) {
		t.Fatalf("intro = %+v", result.Operations[0].SetField.Value)
	}
	if !reflect.DeepEqual(result.Operations[1].SetField.Value, Text("Pause when you finish.")) {
		t.Fatalf("outro = %+v", result.Operations[1].SetField.Value)
	}
}

func TestPlanTextEditsFailureReturnsNoOperations(t *testing.T) {
	result, err := PlanTextEdits(textEditNodes(), []TextEdit{replaceEdit("Turn on", "Enable"), replaceEdit("missing", "x")})
	if err == nil || !strings.Contains(err.Error(), "edit 1:") {
		t.Fatalf("err = %v", err)
	}
	if len(result.Operations) != 0 {
		t.Fatalf("a failed batch must write nothing, got %+v", result.Operations)
	}
}

func TestPlanTextEditsAllOccurrencesChangesEveryMatch(t *testing.T) {
	edit := replaceEdit("you", "they")
	edit.AllOccurrences = true
	result, err := PlanTextEdits(textEditNodes(), []TextEdit{edit})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Operations) != 2 || result.Matches[0].Occurrences != 2 || !reflect.DeepEqual(result.Matches[0].Blocks, []BlockID{"intro", "outro"}) {
		t.Fatalf("result = %+v", result)
	}
}

func TestPlanTextEditsBlockLimitsTheSelection(t *testing.T) {
	edit := replaceEdit("you", "they")
	edit.Block = "outro"
	result, err := PlanTextEdits(textEditNodes(), []TextEdit{edit})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Operations) != 1 || result.Operations[0].SetField.Target.Block != "outro" {
		t.Fatalf("result = %+v", result.Operations)
	}
}

func formatOne(t *testing.T, value Value, edit TextEdit) Value {
	t.Helper()
	result, err := PlanTextEdits([]Node{{ID: "b", Kind: "paragraph", Localized: []FieldValue{{ID: "content", Value: value}}}}, []TextEdit{edit})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Operations) != 1 {
		t.Fatalf("operations = %+v", result.Operations)
	}
	return result.Operations[0].SetField.Value
}
