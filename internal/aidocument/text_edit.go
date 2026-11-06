package aidocument

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// TextEdit changes text selected by Find, or the links selected by FindLink,
// inside Block or else anywhere in the given nodes. It either replaces the text
// or changes its formatting. A selection must match exactly once unless
// AllOccurrences.
type TextEdit struct {
	Block          BlockID
	Find           string
	FindLink       string
	AllOccurrences bool
	Replace        *string
	Format         *InlineFormat
}

// TextEditMatch reports what one edit matched, in the order edits were given.
type TextEditMatch struct {
	Blocks      []BlockID
	Fields      []FieldTarget
	Occurrences int
}

// TextEditPlan is the field writes of a batch of edits, one per changed field
// holding all the edits to it, so the batch applies with one set of revisions.
type TextEditPlan struct {
	Operations []Operation
	Matches    []TextEditMatch
	Previews   []TextEditPreview
}

// TextEditPreview shows one changed field around its first difference.
type TextEditPreview struct {
	Block  BlockID `json:"block_id"`
	Field  FieldID `json:"field"`
	Before string  `json:"before"`
	After  string  `json:"after"`
}

// textEditPreviewRunes bounds each side of a preview, and textEditContextRunes
// how much unchanged text leads it.
const (
	textEditPreviewRunes = 200
	textEditContextRunes = 40
)

func newTextEditPreview(field *editableField) TextEditPreview {
	var before, after strings.Builder
	appendJoinedText(&before, field.original)
	appendJoinedText(&after, field.value)
	beforeRunes, afterRunes := []rune(before.String()), []rune(after.String())
	first := 0
	for first < len(beforeRunes) && first < len(afterRunes) && beforeRunes[first] == afterRunes[first] {
		first++
	}
	start := max(0, first-textEditContextRunes)
	return TextEditPreview{
		Block: field.target.Block, Field: field.target.Field,
		Before: previewRunes(beforeRunes, start), After: previewRunes(afterRunes, start),
	}
}

func previewRunes(text []rune, start int) string {
	end := min(len(text), start+textEditPreviewRunes)
	preview := string(text[min(start, end):end])
	if start > 0 {
		preview = "…" + preview
	}
	if end < len(text) {
		preview += "…"
	}
	return preview
}

// TextEditError names the edit of a batch that could not apply.
type TextEditError struct {
	Index   int
	Message string
}

func (e *TextEditError) Error() string { return fmt.Sprintf("edit %d: %s", e.Index, e.Message) }

// editableField is one locale-owned field value as the edits so far left it.
type editableField struct {
	target   FieldTarget
	original Value
	value    Value
	changed  bool
}

// PlanTextEdits computes edits one after another over the locale-owned text of
// nodes, keeping the marks, links, colors and breaks they do not change, and returns
// the writes for Apply. A later edit sees the text the earlier ones left. Nothing is
// returned if any edit fails.
func PlanTextEdits(nodes []Node, edits []TextEdit) (TextEditPlan, error) {
	fields := editableFields(nodes)
	plan := TextEditPlan{Matches: make([]TextEditMatch, 0, len(edits))}
	for index, edit := range edits {
		match, err := applyTextEdit(fields, edit)
		if err != nil {
			return TextEditPlan{}, &TextEditError{Index: index, Message: err.Error()}
		}
		plan.Matches = append(plan.Matches, match)
	}
	for _, field := range fields {
		if field.changed {
			plan.Operations = append(plan.Operations, Operation{Kind: OperationSetField, SetField: &SetField{Target: field.target, Value: field.value}})
			plan.Previews = append(plan.Previews, newTextEditPreview(field))
		}
	}
	return plan, nil
}

func editableFields(nodes []Node) []*editableField {
	var fields []*editableField
	add := func(target FieldTarget, value Value) {
		fields = append(fields, &editableField{target: target, original: value, value: value})
	}
	for _, node := range nodes {
		for _, field := range node.Localized {
			add(FieldTarget{Block: node.ID, Field: field.ID}, field.Value)
		}
		for _, relation := range node.Relations {
			for _, item := range relation.Items {
				for _, field := range item.Localized {
					add(FieldTarget{Block: node.ID, Relation: relation.ID, Item: item.ID, Field: field.ID}, field.Value)
				}
			}
		}
	}
	return fields
}

func applyTextEdit(fields []*editableField, edit TextEdit) (TextEditMatch, error) {
	if err := edit.validate(); err != nil {
		return TextEditMatch{}, err
	}
	match := TextEditMatch{}
	var matched []*editableField
	for _, field := range fields {
		if !edit.covers(field) {
			continue
		}
		occurrences := edit.count(field.value)
		if occurrences == 0 {
			continue
		}
		matched = append(matched, field)
		match.Fields = append(match.Fields, field.target)
		match.Occurrences += occurrences
		if !slices.Contains(match.Blocks, field.target.Block) {
			match.Blocks = append(match.Blocks, field.target.Block)
		}
	}
	if match.Occurrences == 0 {
		return TextEditMatch{}, edit.nothingMatched(fields)
	}
	if match.Occurrences > 1 && !edit.AllOccurrences {
		return TextEditMatch{}, fmt.Errorf("the selection occurs %d times (blocks %s); pass block_id or a longer find, or all_occurrences to change them all", match.Occurrences, joinBlocks(match.Blocks))
	}
	for _, field := range matched {
		value, err := edit.rewrite(field.value)
		if err != nil {
			return TextEditMatch{}, fmt.Errorf("block %s field %s: %w", field.target.Block, field.target.Field, err)
		}
		field.value, field.changed = value, true
	}
	return match, nil
}

func (edit TextEdit) validate() error {
	if (edit.Find == "") == (edit.FindLink == "") {
		return errors.New("pass exactly one of find and find_link")
	}
	if (edit.Replace == nil) == (edit.Format == nil) {
		return errors.New("an edit either replaces text or changes its formatting")
	}
	if edit.Replace != nil && (edit.FindLink != "" || edit.Find == *edit.Replace) {
		return errors.New("replace needs find and must differ from it")
	}
	if edit.Format != nil && edit.Format.empty() {
		return errors.New("name the formatting to change")
	}
	return nil
}

func (edit TextEdit) covers(field *editableField) bool {
	return edit.Block == "" || field.target.Block == edit.Block
}

func (edit TextEdit) selector() inlineSelector {
	return inlineSelector{text: edit.Find, link: edit.FindLink}
}

// count finds what the edit can change: replacing stays inside one formatted run,
// and formatting may span several.
func (edit TextEdit) count(value Value) int {
	if edit.Replace != nil {
		return countValueText(value, edit.Find)
	}
	return edit.selector().count(value)
}

func (edit TextEdit) rewrite(value Value) (Value, error) {
	if edit.Replace != nil {
		return replaceValueText(value, edit.Find, *edit.Replace), nil
	}
	return edit.selector().format(value, *edit.Format)
}

// nothingMatched explains why the edit selected nothing.
func (edit TextEdit) nothingMatched(fields []*editableField) error {
	if edit.FindLink != "" {
		return errors.New("no link in this locale has that exact address")
	}
	if edit.Replace != nil {
		for _, field := range fields {
			if !edit.covers(field) {
				continue
			}
			var text strings.Builder
			appendJoinedText(&text, field.value)
			if strings.Contains(text.String(), edit.Find) {
				return fmt.Errorf("find crosses a mark, link, color or break in block %s; replace a part inside one formatted run, or write the block's content with document_apply", field.target.Block)
			}
		}
	}
	return errors.New("find does not occur in this locale's text; copy it exactly from document_read (case and spacing matter)")
}

// countInValue adds up the counts of the plain and rich text inside value.
func countInValue(value Value, inText func(string) int, inInline func([]InlineItem) int) int {
	count := 0
	switch value.Kind {
	case ValueKindText:
		count = inText(value.Text)
	case ValueKindInline:
		count = inInline(value.Inline)
	case ValueKindList:
		for _, item := range value.List {
			count += countInValue(item.Value, inText, inInline)
		}
	case ValueKindObject:
		for _, field := range value.Object {
			count += countInValue(field.Value, inText, inInline)
		}
	}
	return count
}

func countValueText(value Value, find string) int {
	return countInValue(value, func(text string) int {
		return strings.Count(text, find)
	}, func(items []InlineItem) int {
		return countInlineText(items, find)
	})
}

func countInlineText(items []InlineItem, find string) int {
	count := 0
	for _, item := range items {
		if item.Kind == InlineKindText {
			count += strings.Count(item.Text, find)
		}
		count += countInlineText(item.Children, find)
	}
	return count
}

// replaceValueText returns a copy of value with find replaced. A text run
// left empty is dropped, and so is a mark or link left with no content.
func replaceValueText(value Value, find, replacement string) Value {
	return mapValueText(value, func(items []InlineItem) []InlineItem {
		return replaceInlineText(items, find, replacement)
	}, func(text string) string {
		return strings.ReplaceAll(text, find, replacement)
	})
}

// mapValueText rewrites the rich text and plain text inside value, keeping its
// lists and objects.
func mapValueText(value Value, inline func([]InlineItem) []InlineItem, text func(string) string) Value {
	switch value.Kind {
	case ValueKindText:
		return Value{Kind: ValueKindText, Text: text(value.Text)}
	case ValueKindInline:
		return Value{Kind: ValueKindInline, Inline: inline(value.Inline)}
	case ValueKindList:
		items := make([]ListItem, len(value.List))
		for index, item := range value.List {
			items[index] = ListItem{ID: item.ID, Value: mapValueText(item.Value, inline, text)}
		}
		return Value{Kind: ValueKindList, List: items}
	case ValueKindObject:
		fields := make([]ObjectField, len(value.Object))
		for index, field := range value.Object {
			fields[index] = ObjectField{ID: field.ID, Value: mapValueText(field.Value, inline, text)}
		}
		return Value{Kind: ValueKindObject, Object: fields}
	}
	return value
}

func replaceInlineText(items []InlineItem, find, replacement string) []InlineItem {
	result := make([]InlineItem, 0, len(items))
	for _, item := range items {
		hadChildren := len(item.Children) != 0
		if item.Kind == InlineKindText {
			item.Text = strings.ReplaceAll(item.Text, find, replacement)
			if item.Text == "" {
				continue
			}
		}
		if hadChildren {
			item.Children = replaceInlineText(item.Children, find, replacement)
			if len(item.Children) == 0 {
				continue
			}
		}
		result = append(result, item)
	}
	return result
}

func appendJoinedText(text *strings.Builder, value Value) {
	switch value.Kind {
	case ValueKindText:
		text.WriteString(value.Text)
	case ValueKindInline:
		appendJoinedInline(text, value.Inline)
	case ValueKindList:
		for _, item := range value.List {
			appendJoinedText(text, item.Value)
			text.WriteByte('\n')
		}
	case ValueKindObject:
		for _, field := range value.Object {
			appendJoinedText(text, field.Value)
			text.WriteByte('\n')
		}
	}
}

func appendJoinedInline(text *strings.Builder, items []InlineItem) {
	for _, item := range items {
		switch item.Kind {
		case InlineKindText:
			text.WriteString(item.Text)
		case InlineKindHardBreak:
			text.WriteByte('\n')
		}
		appendJoinedInline(text, item.Children)
	}
}

func joinBlocks(blocks []BlockID) string {
	names := make([]string, len(blocks))
	for index, block := range blocks {
		names[index] = string(block)
	}
	return strings.Join(names, ", ")
}
