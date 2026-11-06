package aidocument

import (
	"reflect"
	"strings"
	"testing"
)

func TestPlanTextEditsFormat(t *testing.T) {
	url := "https://new.example"
	none := ""
	red := "red"
	cases := []struct {
		name  string
		value Value
		edit  TextEdit
		want  Value
	}{
		{
			name:  "marks part of one run",
			value: RichText(InlineText("read the docs now")),
			edit:  TextEdit{Find: "the docs", Format: &InlineFormat{AddMarks: []InlineKind{InlineKindBold}}},
			want:  RichText(InlineText("read "), Bold(InlineText("the docs")), InlineText(" now")),
		},
		{
			name:  "selection spans a mark boundary",
			value: RichText(InlineText("one "), Bold(InlineText("two")), InlineText(" three")),
			edit:  TextEdit{Find: "one two", Format: &InlineFormat{AddMarks: []InlineKind{InlineKindItalic}}},
			want:  RichText(Italic(InlineText("one ")), Bold(Italic(InlineText("two"))), InlineText(" three")),
		},
		{
			name:  "removes a mark from part of a run and keeps the rest",
			value: RichText(Bold(InlineText("keep drop keep"))),
			edit:  TextEdit{Find: "drop", Format: &InlineFormat{RemoveMarks: []InlineKind{InlineKindBold}}},
			want:  RichText(Bold(InlineText("keep ")), InlineText("drop"), Bold(InlineText(" keep"))),
		},
		{
			name:  "changes a link address and keeps its text and marks",
			value: RichText(InlineText("see "), Link("https://old.example", Bold(InlineText("docs")))),
			edit:  TextEdit{FindLink: "https://old.example", Format: &InlineFormat{Link: &url}},
			want:  RichText(InlineText("see "), Link(url, Bold(InlineText("docs")))),
		},
		{
			name:  "adds a link to text",
			value: RichText(InlineText("see docs here")),
			edit:  TextEdit{Find: "docs", Format: &InlineFormat{Link: &url}},
			want:  RichText(InlineText("see "), Link(url, InlineText("docs")), InlineText(" here")),
		},
		{
			name:  "empty link removes it",
			value: RichText(Link(url, InlineText("docs"))),
			edit:  TextEdit{FindLink: url, Format: &InlineFormat{Link: &none}},
			want:  RichText(InlineText("docs")),
		},
		{
			name:  "sets and clears a color",
			value: RichText(TextColor("blue", InlineText("sky")), InlineText(" sea")),
			edit:  TextEdit{Find: "sky", Format: &InlineFormat{TextColor: &red}},
			want:  RichText(TextColor("red", InlineText("sky")), InlineText(" sea")),
		},
		{
			name:  "a break stays where it was",
			value: RichText(InlineText("a"), HardBreak(), InlineText("b")),
			edit:  TextEdit{Find: "b", Format: &InlineFormat{AddMarks: []InlineKind{InlineKindCode}}},
			want:  RichText(InlineText("a"), HardBreak(), InlineCode(InlineText("b"))),
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := formatOne(t, testCase.value, testCase.edit); !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("got  %+v\nwant %+v", got, testCase.want)
			}
		})
	}
}

func TestPlanTextEditsFormatRefusals(t *testing.T) {
	url := "https://new.example"
	bold := &InlineFormat{AddMarks: []InlineKind{InlineKindBold}}
	rich := RichText(InlineText("a"), HardBreak(), InlineText("b"))
	twoLinks := RichText(Link("https://old.example", InlineText("x")), InlineText(" "), Link("https://old.example", InlineText("y")))
	for name, testCase := range map[string]struct {
		value   Value
		edit    TextEdit
		message string
	}{
		"plain text field":       {Text("a word"), TextEdit{Find: "word", Format: bold}, "plain text"},
		"selection over a break": {rich, TextEdit{Find: "ab", Format: bold}, "does not occur"},
		"two links without all":  {twoLinks, TextEdit{FindLink: "https://old.example", Format: &InlineFormat{Link: &url}}, "occurs 2 times"},
		"unknown link":           {twoLinks, TextEdit{FindLink: "https://nope.example", Format: &InlineFormat{Link: &url}}, "no link"},
		"nothing to change":      {rich, TextEdit{Find: "a", Format: &InlineFormat{}}, "name the formatting"},
		"find and find_link":     {rich, TextEdit{Find: "a", FindLink: url, Format: bold}, "exactly one"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := PlanTextEdits([]Node{{ID: "b", Kind: "paragraph", Localized: []FieldValue{{ID: "content", Value: testCase.value}}}}, []TextEdit{testCase.edit})
			if err == nil || !strings.Contains(err.Error(), testCase.message) {
				t.Fatalf("err = %v, want %q", err, testCase.message)
			}
		})
	}
}

func TestPlanTextEditsFormatAllLinks(t *testing.T) {
	url := "https://new.example"
	got := formatOne(t,
		RichText(Link("https://old.example", InlineText("x")), InlineText(" "), Link("https://old.example", InlineText("y"))),
		TextEdit{FindLink: "https://old.example", AllOccurrences: true, Format: &InlineFormat{Link: &url}},
	)
	want := RichText(Link(url, InlineText("x")), InlineText(" "), Link(url, InlineText("y")))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}
