package translation

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

func normalizeXLIFFUnitInline(unit XLIFFUnit) (XLIFFUnit, error) {
	normalized := unit
	if len(normalized.SourceInline) == 0 {
		data, inline := xliffInlineFromPlaceholderText(normalized.Source, nil)
		normalized.OriginalData = data
		normalized.SourceInline = inline
	}
	if unit.Target != nil && len(normalized.TargetInline) == 0 {
		_, inline := xliffInlineFromPlaceholderText(*unit.Target, normalized.OriginalData)
		normalized.TargetInline = inline
	}
	return normalized, validateNormalizedXLIFFUnitInline(normalized, unit.Target != nil)
}

func validateXLIFFUnitInline(unit XLIFFUnit, requireTarget bool) error {
	normalized, err := normalizeXLIFFUnitInline(unit)
	if err != nil {
		return err
	}
	if requireTarget && normalized.Target == nil {
		return fmt.Errorf("target inline content is required")
	}
	return nil
}

func validateNormalizedXLIFFUnitInline(unit XLIFFUnit, hasTarget bool) error {
	data := make(map[string]string, len(unit.OriginalData))
	for _, original := range unit.OriginalData {
		if strings.TrimSpace(original.ID) == "" {
			return fmt.Errorf("original data id is required")
		}
		if _, duplicate := data[original.ID]; duplicate {
			return fmt.Errorf("duplicate original data id %q", original.ID)
		}
		data[original.ID] = original.Value
	}

	sourceProjection, sourceCodes, err := projectAndValidateXLIFFInline(unit.SourceInline, data, "")
	if err != nil {
		return fmt.Errorf("invalid source inline content: %w", err)
	}
	if sourceProjection != unit.Source {
		return fmt.Errorf("source inline projection changed")
	}
	if !hasTarget {
		if len(unit.TargetInline) != 0 {
			return fmt.Errorf("target inline content exists without a target")
		}
		return nil
	}
	if unit.Target == nil {
		return fmt.Errorf("target is required")
	}
	targetProjection, targetCodes, err := projectAndValidateXLIFFInline(unit.TargetInline, data, "")
	if err != nil {
		return fmt.Errorf("invalid target inline content: %w", err)
	}
	if targetProjection != *unit.Target {
		return fmt.Errorf("target inline projection changed")
	}
	if !reflect.DeepEqual(sourceCodes, targetCodes) {
		return fmt.Errorf("inline code identity or nesting changed")
	}
	if err := validateProtectedXLIFFPartOrder(unit.SourceInline, unit.TargetInline); err != nil {
		return err
	}
	allowedTextParents := make(map[string]struct{})
	collectXLIFFTextParents(unit.SourceInline, "", allowedTextParents)
	if err := validateXLIFFTextParents(unit.TargetInline, "", allowedTextParents); err != nil {
		return err
	}
	return nil
}

func validateProtectedXLIFFPartOrder(source, target []XLIFFInline) error {
	sourceEvents := xliffLeafEvents(source)
	targetEvents := xliffLeafEvents(target)
	expected := make(map[int][]string)
	for _, event := range sourceEvents {
		group, _, ok := parseProtectedXLIFFPartID(event)
		if ok {
			expected[group] = append(expected[group], event)
		}
	}
	for group, expectedParts := range expected {
		positions := make([]int, 0, len(expectedParts))
		actualParts := make([]string, 0, len(expectedParts))
		for position, event := range targetEvents {
			eventGroup, _, ok := parseProtectedXLIFFPartID(event)
			if ok && eventGroup == group {
				positions = append(positions, position)
				actualParts = append(actualParts, event)
			}
		}
		if !reflect.DeepEqual(expectedParts, actualParts) {
			return fmt.Errorf("protected term parts changed order")
		}
		for index := 1; index < len(positions); index++ {
			if positions[index] != positions[index-1]+1 {
				return fmt.Errorf("protected term parts are no longer adjacent")
			}
		}
	}
	return nil
}

func xliffLeafEvents(nodes []XLIFFInline) []string {
	events := make([]string, 0)
	var collect func([]XLIFFInline)
	collect = func(children []XLIFFInline) {
		for _, node := range children {
			switch node.Kind {
			case XLIFFInlineText:
				if node.Text != "" {
					events = append(events, "#text")
				}
			case XLIFFInlinePlaceholder:
				events = append(events, node.ID)
			case XLIFFInlinePairedCode:
				collect(node.Children)
			}
		}
	}
	collect(nodes)
	return events
}

func parseProtectedXLIFFPartID(id string) (int, int, bool) {
	if !strings.HasPrefix(id, protectedXLIFFCodePrefix) {
		return 0, 0, false
	}
	groupAndPart := strings.Split(strings.TrimPrefix(id, protectedXLIFFCodePrefix), "Part")
	if len(groupAndPart) != 2 {
		return 0, 0, false
	}
	group, groupErr := strconv.Atoi(groupAndPart[0])
	part, partErr := strconv.Atoi(groupAndPart[1])
	return group, part, groupErr == nil && partErr == nil && group > 0 && part > 0
}

func collectXLIFFTextParents(nodes []XLIFFInline, parent string, parents map[string]struct{}) {
	for _, node := range nodes {
		if node.Kind == XLIFFInlineText && node.Text != "" {
			parents[parent] = struct{}{}
		}
		nextParent := parent
		if node.Kind == XLIFFInlinePairedCode {
			nextParent = node.ID
		}
		collectXLIFFTextParents(node.Children, nextParent, parents)
	}
}

func validateXLIFFTextParents(nodes []XLIFFInline, parent string, allowed map[string]struct{}) error {
	for _, node := range nodes {
		if node.Kind == XLIFFInlineText && node.Text != "" {
			if _, ok := allowed[parent]; !ok {
				return fmt.Errorf("target text moved outside a source text-bearing inline parent")
			}
		}
		nextParent := parent
		if node.Kind == XLIFFInlinePairedCode {
			nextParent = node.ID
		}
		if err := validateXLIFFTextParents(node.Children, nextParent, allowed); err != nil {
			return err
		}
	}
	return nil
}

func xliffInlineFromPlaceholderText(value string, existing []XLIFFOriginalData) ([]XLIFFOriginalData, []XLIFFInline) {
	data := append([]XLIFFOriginalData(nil), existing...)
	available := make(map[string][]XLIFFOriginalData)
	for _, original := range existing {
		available[original.Value] = append(available[original.Value], original)
	}
	used := make(map[string]int)
	inline := make([]XLIFFInline, 0, len(ExtractPlaceholders(value))*2+1)
	remaining := value
	for len(remaining) > 0 {
		location := translationPlaceholderPattern.FindStringIndex(remaining)
		if location == nil {
			inline = appendXLIFFText(inline, remaining)
			break
		}
		inline = appendXLIFFText(inline, remaining[:location[0]])
		placeholder := remaining[location[0]:location[1]]
		occurrence := used[placeholder]
		used[placeholder]++
		var original XLIFFOriginalData
		if occurrence < len(available[placeholder]) {
			original = available[placeholder][occurrence]
		} else {
			original = XLIFFOriginalData{ID: fmt.Sprintf("d%d", len(data)+1), Value: placeholder}
			data = append(data, original)
		}
		inline = append(inline, XLIFFInline{
			Kind: XLIFFInlinePlaceholder, ID: "p" + strings.TrimPrefix(original.ID, "d"),
			DataRef: original.ID, CanCopy: "no", CanDelete: "no",
		})
		remaining = remaining[location[1]:]
	}
	if len(inline) == 0 {
		inline = []XLIFFInline{{Kind: XLIFFInlineText, Text: value}}
	}
	return data, inline
}

// XLIFFInlineFromText protects placeholders while constructing typed inline
// content owned by a domain extractor.
func XLIFFInlineFromText(value string, existing []XLIFFOriginalData) ([]XLIFFOriginalData, []XLIFFInline) {
	return xliffInlineFromPlaceholderText(value, existing)
}

// ProjectXLIFFInline returns the visible text represented by typed inline
// content after validating every original-data reference.
func ProjectXLIFFInline(inline []XLIFFInline, originalData []XLIFFOriginalData) (string, error) {
	data := make(map[string]string, len(originalData))
	for _, original := range originalData {
		if _, duplicate := data[original.ID]; duplicate {
			return "", fmt.Errorf("duplicate original data id %q", original.ID)
		}
		data[original.ID] = original.Value
	}
	projection, _, err := projectAndValidateXLIFFInline(inline, data, "")
	return projection, err
}

func appendXLIFFText(inline []XLIFFInline, value string) []XLIFFInline {
	if value == "" {
		return inline
	}
	if len(inline) > 0 && inline[len(inline)-1].Kind == XLIFFInlineText {
		inline[len(inline)-1].Text += value
		return inline
	}
	return append(inline, XLIFFInline{Kind: XLIFFInlineText, Text: value})
}

type xliffCodeIdentity struct {
	Kind         XLIFFInlineKind
	ID           string
	DataRef      string
	DataRefStart string
	DataRefEnd   string
	ParentID     string
}

func projectAndValidateXLIFFInline(
	inline []XLIFFInline,
	data map[string]string,
	parentID string,
) (string, []xliffCodeIdentity, error) {
	var projection strings.Builder
	codes := make([]xliffCodeIdentity, 0)
	seenIDs := make(map[string]struct{})
	var visit func([]XLIFFInline, string) error
	visit = func(nodes []XLIFFInline, parent string) error {
		for _, node := range nodes {
			switch node.Kind {
			case XLIFFInlineText:
				if node.ID != "" || node.DataRef != "" || node.DataRefStart != "" || node.DataRefEnd != "" || len(node.Children) > 0 {
					return fmt.Errorf("text inline node has code fields")
				}
				projection.WriteString(node.Text)
			case XLIFFInlinePlaceholder:
				if err := validateXLIFFInlineID(node, seenIDs); err != nil {
					return err
				}
				if node.DataRef == "" || node.DataRefStart != "" || node.DataRefEnd != "" || len(node.Children) > 0 {
					return fmt.Errorf("placeholder %q has invalid data references", node.ID)
				}
				value, ok := data[node.DataRef]
				if !ok {
					return fmt.Errorf("placeholder %q references unknown original data", node.ID)
				}
				projection.WriteString(value)
				codes = append(codes, xliffCodeIdentity{Kind: node.Kind, ID: node.ID, DataRef: node.DataRef, ParentID: parent})
			case XLIFFInlinePairedCode:
				if err := validateXLIFFInlineID(node, seenIDs); err != nil {
					return err
				}
				if node.DataRef != "" || node.DataRefStart == "" || node.DataRefEnd == "" {
					return fmt.Errorf("paired code %q has invalid data references", node.ID)
				}
				start, startOK := data[node.DataRefStart]
				end, endOK := data[node.DataRefEnd]
				if !startOK || !endOK {
					return fmt.Errorf("paired code %q references unknown original data", node.ID)
				}
				projection.WriteString(start)
				codes = append(codes, xliffCodeIdentity{
					Kind: node.Kind, ID: node.ID, DataRefStart: node.DataRefStart,
					DataRefEnd: node.DataRefEnd, ParentID: parent,
				})
				if err := visit(node.Children, node.ID); err != nil {
					return err
				}
				projection.WriteString(end)
			default:
				return fmt.Errorf("unsupported inline kind %q", node.Kind)
			}
		}
		return nil
	}
	if err := visit(inline, parentID); err != nil {
		return "", nil, err
	}
	sort.Slice(codes, func(i, j int) bool {
		if codes[i].ID == codes[j].ID {
			return codes[i].Kind < codes[j].Kind
		}
		return codes[i].ID < codes[j].ID
	})
	return projection.String(), codes, nil
}

func validateXLIFFInlineID(node XLIFFInline, seen map[string]struct{}) error {
	if strings.TrimSpace(node.ID) == "" {
		return fmt.Errorf("inline code id is required")
	}
	if _, duplicate := seen[node.ID]; duplicate {
		return fmt.Errorf("duplicate inline code id %q", node.ID)
	}
	seen[node.ID] = struct{}{}
	return nil
}
