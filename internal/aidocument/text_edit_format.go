package aidocument

import (
	"errors"
	"slices"
	"strings"
)

// InlineFormat changes the marks on selected text. A nil Link or color leaves
// it as it is; an empty one removes it.
type InlineFormat struct {
	AddMarks        []InlineKind
	RemoveMarks     []InlineKind
	Link            *string
	TextColor       *string
	BackgroundColor *string
}

func (f InlineFormat) empty() bool {
	return len(f.AddMarks) == 0 && len(f.RemoveMarks) == 0 && f.Link == nil && f.TextColor == nil && f.BackgroundColor == nil
}

// inlineSelector picks the text a format edit changes: the occurrences of text,
// which may span marks, or the runs inside links to link.
type inlineSelector struct {
	text string
	link string
}

func (s inlineSelector) count(value Value) int {
	return countInValue(value, func(text string) int {
		if s.text == "" {
			return 0
		}
		return strings.Count(text, s.text)
	}, func(items []InlineItem) int {
		leaves := flattenInline(items)
		if s.link != "" {
			return len(linkOrigins(leaves, s.link))
		}
		return len(textRanges(joinLeaves(leaves), s.text))
	})
}

func (s inlineSelector) format(value Value, format InlineFormat) (Value, error) {
	var plainText bool
	formatted := mapValueText(value, func(items []InlineItem) []InlineItem {
		return s.formatInline(items, format)
	}, func(text string) string {
		if s.text != "" && strings.Contains(text, s.text) {
			plainText = true
		}
		return text
	})
	if plainText {
		return Value{}, errors.New("this field is plain text and cannot hold formatting")
	}
	return formatted, nil
}

func (s inlineSelector) formatInline(items []InlineItem, format InlineFormat) []InlineItem {
	leaves := flattenInline(items)
	if s.link != "" {
		for index := range leaves {
			if leaves[index].insideLink(s.link) {
				leaves[index].wrappers = format.apply(leaves[index].wrappers)
			}
		}
		return rebuildInline(leaves, 0)
	}
	return rebuildInline(formatTextRanges(leaves, textRanges(joinLeaves(leaves), s.text), format), 0)
}

// inlineWrapper is one mark around a leaf. origin tells two adjacent links with
// the same address apart while counting them.
type inlineWrapper struct {
	kind   InlineKind
	target string
	origin int
}

// inlineLeaf is one childless inline item and the marks around it, outermost
// first.
type inlineLeaf struct {
	item     InlineItem
	wrappers []inlineWrapper
}

func (l inlineLeaf) insideLink(address string) bool {
	return slices.ContainsFunc(l.wrappers, func(wrapper inlineWrapper) bool {
		return wrapper.kind == InlineKindLink && wrapper.target == address
	})
}

func flattenInline(items []InlineItem) []inlineLeaf {
	var leaves []inlineLeaf
	origin := 0
	var visit func(items []InlineItem, wrappers []inlineWrapper)
	visit = func(items []InlineItem, wrappers []inlineWrapper) {
		for _, item := range items {
			switch item.Kind {
			case InlineKindBold, InlineKindItalic, InlineKindUnderline, InlineKindStrike, InlineKindCode, InlineKindTextColor, InlineKindBackground, InlineKindLink:
				origin++
				visit(item.Children, append(slices.Clone(wrappers), inlineWrapper{kind: item.Kind, target: item.Target, origin: origin}))
			default:
				leaves = append(leaves, inlineLeaf{item: item, wrappers: wrappers})
			}
		}
	}
	visit(items, nil)
	return leaves
}

func linkOrigins(leaves []inlineLeaf, address string) map[int]struct{} {
	origins := map[int]struct{}{}
	for _, leaf := range leaves {
		for _, wrapper := range leaf.wrappers {
			if wrapper.kind == InlineKindLink && wrapper.target == address {
				origins[wrapper.origin] = struct{}{}
			}
		}
	}
	return origins
}

// nonTextLeaf stands in for a break, variable or math item in joined text, so a
// selection cannot span it.
const nonTextLeaf = "￼"

func joinLeaves(leaves []inlineLeaf) string {
	var joined strings.Builder
	for _, leaf := range leaves {
		if leaf.item.Kind == InlineKindText {
			joined.WriteString(leaf.item.Text)
		} else {
			joined.WriteString(nonTextLeaf)
		}
	}
	return joined.String()
}

type byteRange struct{ start, end int }

func textRanges(joined, find string) []byteRange {
	var ranges []byteRange
	for offset := 0; find != ""; {
		index := strings.Index(joined[offset:], find)
		if index < 0 {
			break
		}
		start := offset + index
		ranges = append(ranges, byteRange{start, start + len(find)})
		offset = start + len(find)
	}
	return ranges
}

// formatTextRanges splits text leaves at the selection's edges and formats the
// parts inside it.
func formatTextRanges(leaves []inlineLeaf, ranges []byteRange, format InlineFormat) []inlineLeaf {
	result := make([]inlineLeaf, 0, len(leaves))
	offset := 0
	for _, leaf := range leaves {
		if leaf.item.Kind != InlineKindText {
			offset += len(nonTextLeaf)
			result = append(result, leaf)
			continue
		}
		start, end := offset, offset+len(leaf.item.Text)
		offset = end
		cursor := start
		emit := func(from, to int, selected bool) {
			if from >= to {
				return
			}
			part := leaf
			part.item.Text = leaf.item.Text[from-start : to-start]
			if selected {
				part.wrappers = format.apply(leaf.wrappers)
			}
			result = append(result, part)
		}
		for _, selection := range ranges {
			from, to := max(selection.start, start), min(selection.end, end)
			if from >= to {
				continue
			}
			emit(cursor, from, false)
			emit(from, to, true)
			cursor = to
		}
		emit(cursor, end, false)
	}
	return result
}

// apply returns wrappers with the format's changes. New marks go innermost; a
// link goes outermost, where the editor keeps it.
func (f InlineFormat) apply(wrappers []inlineWrapper) []inlineWrapper {
	result := make([]inlineWrapper, 0, len(wrappers)+len(f.AddMarks)+1)
	for _, wrapper := range wrappers {
		switch {
		case slices.Contains(f.RemoveMarks, wrapper.kind),
			wrapper.kind == InlineKindLink && f.Link != nil,
			wrapper.kind == InlineKindTextColor && f.TextColor != nil,
			wrapper.kind == InlineKindBackground && f.BackgroundColor != nil:
			continue
		}
		result = append(result, wrapper)
	}
	has := func(kind InlineKind) bool {
		return slices.ContainsFunc(result, func(wrapper inlineWrapper) bool { return wrapper.kind == kind })
	}
	for _, mark := range f.AddMarks {
		if !has(mark) {
			result = append(result, inlineWrapper{kind: mark})
		}
	}
	if f.TextColor != nil && *f.TextColor != "" {
		result = append(result, inlineWrapper{kind: InlineKindTextColor, target: *f.TextColor})
	}
	if f.BackgroundColor != nil && *f.BackgroundColor != "" {
		result = append(result, inlineWrapper{kind: InlineKindBackground, target: *f.BackgroundColor})
	}
	if f.Link != nil && *f.Link != "" {
		result = append([]inlineWrapper{{kind: InlineKindLink, target: *f.Link}}, result...)
	}
	return result
}

// rebuildInline nests leaves back into marks, sharing a mark across neighbours
// that both have it at this depth and joining neighbouring text runs.
func rebuildInline(leaves []inlineLeaf, depth int) []InlineItem {
	result := make([]InlineItem, 0, len(leaves))
	for index := 0; index < len(leaves); {
		leaf := leaves[index]
		if len(leaf.wrappers) == depth {
			if count := len(result); count > 0 && leaf.item.Kind == InlineKindText && result[count-1].Kind == InlineKindText {
				result[count-1].Text += leaf.item.Text
			} else {
				result = append(result, leaf.item)
			}
			index++
			continue
		}
		wrapper := leaf.wrappers[depth]
		end := index + 1
		for end < len(leaves) && len(leaves[end].wrappers) > depth &&
			leaves[end].wrappers[depth].kind == wrapper.kind && leaves[end].wrappers[depth].target == wrapper.target {
			end++
		}
		result = append(result, InlineItem{Kind: wrapper.kind, Target: wrapper.target, Children: rebuildInline(leaves[index:end], depth+1)})
		index = end
	}
	return result
}
