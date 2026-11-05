package aidocument

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// outlinePreviewRunes bounds each outline preview so an outline of every
// block stays small enough to read in one page.
const outlinePreviewRunes = 80

// searchContextRunes is how much text a search preview keeps before a match.
const searchContextRunes = 20

// MaxReadQueryRunes bounds an outline search query.
const MaxReadQueryRunes = 200

// outlinePreviews returns the start of each node's locale-owned text, so an
// outline can locate the block to read or edit without reading every block.
// Shared values such as alignment, sizes and URLs are not human text and are
// left out; nodes without locale text have no preview.
func outlinePreviews(nodes []Node) map[BlockID]string {
	previews := make(map[BlockID]string)
	for _, node := range nodes {
		if preview := truncateRunes(nodeLocaleText(node, outlinePreviewRunes), outlinePreviewRunes); preview != "" {
			previews[node.ID] = preview
		}
	}
	if len(previews) == 0 {
		return nil
	}
	return previews
}

// searchNodes returns, for each node whose locale-owned text contains query,
// a preview that starts shortly before the first match. Matching ignores case
// and collapses whitespace, so text copied from a preview finds its block.
func searchNodes(nodes []Node, query string) map[BlockID]string {
	needle := lowerRunes([]rune(strings.Join(strings.Fields(query), " ")))
	matches := make(map[BlockID]string)
	for _, node := range nodes {
		text := []rune(nodeLocaleText(node, 0))
		index := runeIndex(lowerRunes(text), needle)
		if index < 0 {
			// A link's address is not text a reader sees, but finding every link
			// to a page starts from it.
			if linked := nodeLinkAddresses(node); runeIndex(lowerRunes([]rune(linked)), needle) >= 0 {
				text, index = []rune(strings.TrimSpace(string(text)+" "+linked)), 0
			} else {
				continue
			}
		}
		start := max(0, index-searchContextRunes)
		end := min(len(text), start+outlinePreviewRunes)
		preview := strings.TrimSpace(string(text[start:end]))
		if start > 0 {
			preview = "…" + preview
		}
		if end < len(text) {
			preview += "…"
		}
		matches[node.ID] = preview
	}
	return matches
}

// lowerRunes lowers each rune on its own so indexes into the result are
// indexes into the original text.
func lowerRunes(text []rune) []rune {
	lowered := make([]rune, len(text))
	for index, r := range text {
		lowered[index] = unicode.ToLower(r)
	}
	return lowered
}

func runeIndex(haystack, needle []rune) int {
	if len(needle) == 0 {
		return -1
	}
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if slices.Equal(haystack[index:index+len(needle)], needle) {
			return index
		}
	}
	return -1
}

// nodeLocaleText joins a node's locale-owned text, including relation item
// text, with whitespace collapsed. A positive limit stops collecting once the
// text is longer than limit runes.
func nodeLocaleText(node Node, limit int) string {
	var text strings.Builder
	full := func() bool { return limit > 0 && utf8.RuneCountInString(text.String()) > limit }
	for _, field := range node.Localized {
		if full() {
			break
		}
		appendValueText(&text, field.Value)
	}
	for _, relation := range node.Relations {
		for _, item := range relation.Items {
			for _, field := range item.Localized {
				if full() {
					break
				}
				appendValueText(&text, field.Value)
			}
		}
	}
	return strings.Join(strings.Fields(text.String()), " ")
}

func nodeLinkAddresses(node Node) string {
	var addresses []string
	var visit func(items []InlineItem)
	visit = func(items []InlineItem) {
		for _, item := range items {
			if item.Kind == InlineKindLink && item.Target != "" {
				addresses = append(addresses, item.Target)
			}
			visit(item.Children)
		}
	}
	for _, field := range node.Localized {
		visit(field.Value.Inline)
	}
	return strings.Join(addresses, " ")
}

func appendValueText(text *strings.Builder, value Value) {
	switch value.Kind {
	case ValueKindText:
		text.WriteString(value.Text)
		text.WriteByte(' ')
	case ValueKindInline:
		appendInlineText(text, value.Inline)
		text.WriteByte(' ')
	case ValueKindList:
		for _, item := range value.List {
			appendValueText(text, item.Value)
		}
	case ValueKindObject:
		for _, field := range value.Object {
			appendValueText(text, field.Value)
		}
	}
}

func appendInlineText(text *strings.Builder, items []InlineItem) {
	for _, item := range items {
		switch item.Kind {
		case InlineKindText, InlineKindMath:
			text.WriteString(item.Text)
		case InlineKindHardBreak:
			text.WriteByte(' ')
		case InlineKindSiteVariable:
			text.WriteString("{" + item.Text + "}")
		}
		appendInlineText(text, item.Children)
	}
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit])) + "…"
}

// HasInlineFormatting reports whether a value carries anything a plain-text
// replacement would drop: marks, links, colors, breaks, math, placeholders
// or site variables.
func HasInlineFormatting(value Value) bool {
	if value.Kind != ValueKindInline {
		return false
	}
	for _, item := range value.Inline {
		if item.Kind != InlineKindText || len(item.Children) != 0 {
			return true
		}
	}
	return false
}

// ExistingTargetBlocks returns the blocks that operations change in place,
// in first-use order. Blocks inserted by the same batch are left out, so the
// result names blocks that already existed when the batch was prepared.
func ExistingTargetBlocks(operations []Operation) []BlockID {
	inserted := make(map[BlockID]bool)
	for _, operation := range operations {
		if operation.Kind == OperationInsertBlock {
			inserted[operation.InsertBlock.Block] = true
		}
	}
	seen := make(map[BlockID]bool)
	var blocks []BlockID
	add := func(block BlockID) {
		if block != "" && !inserted[block] && !seen[block] {
			seen[block] = true
			blocks = append(blocks, block)
		}
	}
	for _, operation := range operations {
		switch operation.Kind {
		case OperationSetField:
			add(operation.SetField.Target.Block)
		case OperationUnsetField:
			add(operation.UnsetField.Target.Block)
		case OperationAttachFile:
			add(operation.AttachFile.Target.Block)
		case OperationDetachFile:
			add(operation.DetachFile.Target.Block)
		case OperationDeleteBlock:
			add(operation.DeleteBlock.Block)
		case OperationMoveBlock:
			add(operation.MoveBlock.Block)
		case OperationReplaceBlockKind:
			add(operation.ReplaceBlockKind.Block)
		case OperationInsertRelationItem:
			add(operation.InsertRelationItem.Block)
		case OperationDeleteRelationItem:
			add(operation.DeleteRelationItem.Block)
		case OperationMoveRelationItem:
			add(operation.MoveRelationItem.Block)
			add(operation.MoveRelationItem.TargetBlock)
		}
	}
	return blocks
}
