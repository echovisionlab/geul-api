package aidocument

import (
	"strings"
	"unicode/utf8"
)

// outlinePreviewRunes bounds each outline preview so an outline of every
// block stays small enough to read in one page.
const outlinePreviewRunes = 80

// outlinePreviews returns the start of each node's locale-owned text, so an
// outline can locate the block to read or edit without reading every block.
// Shared values such as alignment, sizes and URLs are not human text and are
// left out; nodes without locale text have no preview.
func outlinePreviews(nodes []Node) map[BlockID]string {
	previews := make(map[BlockID]string)
	for _, node := range nodes {
		var text strings.Builder
		for _, field := range node.Localized {
			appendValueText(&text, field.Value)
			if utf8.RuneCountInString(text.String()) > outlinePreviewRunes {
				break
			}
		}
		if preview := truncateRunes(strings.Join(strings.Fields(text.String()), " "), outlinePreviewRunes); preview != "" {
			previews[node.ID] = preview
		}
	}
	if len(previews) == 0 {
		return nil
	}
	return previews
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
