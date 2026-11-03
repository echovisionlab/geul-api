package aidocument

import (
	"fmt"
	"strings"
)

// TextReplacement is the one field change ReplaceTextOnce found.
type TextReplacement struct {
	Block     BlockID
	Operation Operation
}

// ReplaceTextOnce replaces find with replacement where find occurs exactly
// once in the locale-owned text of nodes, keeping the marks, links, colors
// and breaks around it. It returns the field write for the whole changed
// value, so the caller applies it with the document's expected revisions.
// A match must sit inside one text run; text that crosses a formatting
// boundary is reported instead of being rewritten.
func ReplaceTextOnce(nodes []Node, find, replacement string) (TextReplacement, error) {
	if find == "" {
		return TextReplacement{}, &InputError{Message: "find must not be empty"}
	}
	if find == replacement {
		return TextReplacement{}, &InputError{Message: "replace must differ from find"}
	}
	total := 0
	var matchedBlocks []BlockID
	var found TextReplacement
	consider := func(block BlockID, target FieldTarget, value Value) {
		count := countValueText(value, find)
		if count == 0 {
			return
		}
		total += count
		if len(matchedBlocks) == 0 || matchedBlocks[len(matchedBlocks)-1] != block {
			matchedBlocks = append(matchedBlocks, block)
		}
		if total == 1 {
			found = TextReplacement{Block: block, Operation: Operation{Kind: OperationSetField, SetField: &SetField{
				Target: target, Value: replaceValueText(value, find, replacement),
			}}}
		}
	}
	for _, node := range nodes {
		for _, field := range node.Localized {
			consider(node.ID, FieldTarget{Block: node.ID, Field: field.ID}, field.Value)
		}
		for _, relation := range node.Relations {
			for _, item := range relation.Items {
				for _, field := range item.Localized {
					consider(node.ID, FieldTarget{Block: node.ID, Relation: relation.ID, Item: item.ID, Field: field.ID}, field.Value)
				}
			}
		}
	}
	switch {
	case total == 1:
		return found, nil
	case total > 1:
		return TextReplacement{}, &InputError{Message: fmt.Sprintf(
			"find occurs %d times (blocks %s); pass block_id or a longer find that occurs exactly once", total, joinBlocks(matchedBlocks))}
	}
	if block, crosses := findAcrossFormatting(nodes, find); crosses {
		return TextReplacement{}, &InputError{Message: "find crosses a mark, link, color or break in block " + string(block) +
			"; replace a part inside one formatted run, or write the block's full inline content with document_apply"}
	}
	return TextReplacement{}, &InputError{Message: "find does not occur in this locale's text; copy it exactly from document_read (case and spacing matter)"}
}

func countValueText(value Value, find string) int {
	switch value.Kind {
	case ValueKindText:
		return strings.Count(value.Text, find)
	case ValueKindInline:
		return countInlineText(value.Inline, find)
	case ValueKindList:
		count := 0
		for _, item := range value.List {
			count += countValueText(item.Value, find)
		}
		return count
	case ValueKindObject:
		count := 0
		for _, field := range value.Object {
			count += countValueText(field.Value, find)
		}
		return count
	}
	return 0
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
	switch value.Kind {
	case ValueKindText:
		return Value{Kind: ValueKindText, Text: strings.ReplaceAll(value.Text, find, replacement)}
	case ValueKindInline:
		return Value{Kind: ValueKindInline, Inline: replaceInlineText(value.Inline, find, replacement)}
	case ValueKindList:
		items := make([]ListItem, len(value.List))
		for index, item := range value.List {
			items[index] = ListItem{ID: item.ID, Value: replaceValueText(item.Value, find, replacement)}
		}
		return Value{Kind: ValueKindList, List: items}
	case ValueKindObject:
		fields := make([]ObjectField, len(value.Object))
		for index, field := range value.Object {
			fields[index] = ObjectField{ID: field.ID, Value: replaceValueText(field.Value, find, replacement)}
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

// findAcrossFormatting reports the first block whose field text contains find
// only when its text runs are joined.
func findAcrossFormatting(nodes []Node, find string) (BlockID, bool) {
	for _, node := range nodes {
		values := make([]Value, 0, len(node.Localized))
		for _, field := range node.Localized {
			values = append(values, field.Value)
		}
		for _, relation := range node.Relations {
			for _, item := range relation.Items {
				for _, field := range item.Localized {
					values = append(values, field.Value)
				}
			}
		}
		for _, value := range values {
			var text strings.Builder
			appendJoinedText(&text, value)
			if strings.Contains(text.String(), find) {
				return node.ID, true
			}
		}
	}
	return "", false
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
