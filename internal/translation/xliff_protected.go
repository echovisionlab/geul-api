package translation

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const protectedXLIFFCodePrefix = "protectedTerm"

type protectedXLIFFOccurrence struct {
	start int
	end   int
	group int
}

type xliffTextRange struct {
	start int
	end   int
}

// ProtectXLIFFTerms replaces every exact case-sensitive occurrence with
// non-copyable, non-deletable ph parts. One occurrence may span multiple pc
// runs; deterministic group/part IDs let validation prove those parts remain
// adjacent and ordered while the original rich-text marks remain intact.
func ProtectXLIFFTerms(document *XLIFFDocument, terms []string) error {
	if document == nil {
		return nil
	}
	terms = NormalizeProtectedTerms(terms)
	sort.SliceStable(terms, func(i, j int) bool {
		if len(terms[i]) == len(terms[j]) {
			return terms[i] < terms[j]
		}
		return len(terms[i]) > len(terms[j])
	})
	if len(terms) == 0 {
		return nil
	}
	for groupIndex := range document.File.Groups {
		for unitIndex := range document.File.Groups[groupIndex].TranslationUnit {
			unit := &document.File.Groups[groupIndex].TranslationUnit[unitIndex]
			normalized, err := normalizeXLIFFUnitInline(*unit)
			if err != nil {
				return err
			}
			occurrences := protectedXLIFFOccurrences(normalized.Source, terms)
			if len(occurrences) == 0 {
				*unit = normalized
				continue
			}
			dataValues := xliffOriginalDataValues(normalized.OriginalData)
			textRanges := make([]xliffTextRange, 0)
			offset := 0
			collectXLIFFTextRanges(normalized.SourceInline, dataValues, &offset, &textRanges)
			for _, occurrence := range occurrences {
				if !protectedOccurrenceCoveredByText(occurrence, textRanges) {
					return fmt.Errorf("protected term occurrence cannot be represented by XLIFF text parts")
				}
			}
			usedCodeIDs := make(map[string]struct{})
			collectXLIFFInlineCodeIDs(normalized.SourceInline, usedCodeIDs)
			for id := range usedCodeIDs {
				if strings.HasPrefix(id, protectedXLIFFCodePrefix) {
					return fmt.Errorf("source inline code uses the reserved protected-term prefix")
				}
			}
			for _, original := range normalized.OriginalData {
				if strings.HasPrefix(original.ID, "protectedData") {
					return fmt.Errorf("source original data uses the reserved protected-term prefix")
				}
			}
			partByGroup := make(map[int]int)
			offset = 0
			normalized.SourceInline = protectXLIFFInlineOccurrences(
				normalized.SourceInline, occurrences, dataValues, &normalized.OriginalData,
				&offset, partByGroup, usedCodeIDs,
			)
			*unit = normalized
			if err := validateXLIFFUnitInline(*unit, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func protectedXLIFFOccurrences(source string, terms []string) []protectedXLIFFOccurrence {
	occurrences := make([]protectedXLIFFOccurrence, 0)
	for offset := 0; offset < len(source); {
		location, term := firstProtectedTerm(source[offset:], terms)
		if location < 0 {
			break
		}
		start := offset + location
		occurrences = append(occurrences, protectedXLIFFOccurrence{
			start: start, end: start + len(term), group: len(occurrences) + 1,
		})
		offset = start + len(term)
	}
	return occurrences
}

func collectXLIFFTextRanges(
	nodes []XLIFFInline,
	data map[string]string,
	offset *int,
	ranges *[]xliffTextRange,
) {
	for _, node := range nodes {
		switch node.Kind {
		case XLIFFInlineText:
			start := *offset
			*offset += len(node.Text)
			*ranges = append(*ranges, xliffTextRange{start: start, end: *offset})
		case XLIFFInlinePlaceholder:
			*offset += len(data[node.DataRef])
		case XLIFFInlinePairedCode:
			*offset += len(data[node.DataRefStart])
			collectXLIFFTextRanges(node.Children, data, offset, ranges)
			*offset += len(data[node.DataRefEnd])
		}
	}
}

func protectedOccurrenceCoveredByText(occurrence protectedXLIFFOccurrence, ranges []xliffTextRange) bool {
	covered := 0
	for _, textRange := range ranges {
		start := max(occurrence.start, textRange.start)
		end := min(occurrence.end, textRange.end)
		if start < end {
			covered += end - start
		}
	}
	return covered == occurrence.end-occurrence.start
}

func protectXLIFFInlineOccurrences(
	nodes []XLIFFInline,
	occurrences []protectedXLIFFOccurrence,
	dataValues map[string]string,
	data *[]XLIFFOriginalData,
	offset *int,
	partByGroup map[int]int,
	usedCodeIDs map[string]struct{},
) []XLIFFInline {
	result := make([]XLIFFInline, 0, len(nodes))
	for _, node := range nodes {
		switch node.Kind {
		case XLIFFInlineText:
			nodeStart := *offset
			nodeEnd := nodeStart + len(node.Text)
			cursor := nodeStart
			for _, occurrence := range occurrences {
				start := max(nodeStart, occurrence.start)
				end := min(nodeEnd, occurrence.end)
				if start >= end {
					continue
				}
				result = appendXLIFFText(result, node.Text[cursor-nodeStart:start-nodeStart])
				partByGroup[occurrence.group]++
				part := partByGroup[occurrence.group]
				codeID := protectedXLIFFPartID(occurrence.group, part)
				usedCodeIDs[codeID] = struct{}{}
				dataID := "protectedData" + strconv.Itoa(occurrence.group) + "Part" + strconv.Itoa(part)
				value := node.Text[start-nodeStart : end-nodeStart]
				*data = append(*data, XLIFFOriginalData{ID: dataID, Value: value})
				dataValues[dataID] = value
				result = append(result, XLIFFInline{
					Kind: XLIFFInlinePlaceholder, ID: codeID, DataRef: dataID,
					CanCopy: "no", CanDelete: "no",
				})
				cursor = end
			}
			result = appendXLIFFText(result, node.Text[cursor-nodeStart:])
			*offset = nodeEnd
		case XLIFFInlinePlaceholder:
			result = append(result, node)
			*offset += len(dataValues[node.DataRef])
		case XLIFFInlinePairedCode:
			*offset += len(dataValues[node.DataRefStart])
			node.Children = protectXLIFFInlineOccurrences(
				node.Children, occurrences, dataValues, data, offset, partByGroup, usedCodeIDs,
			)
			*offset += len(dataValues[node.DataRefEnd])
			result = append(result, node)
		}
	}
	return result
}

func protectedXLIFFPartID(group, part int) string {
	return protectedXLIFFCodePrefix + strconv.Itoa(group) + "Part" + strconv.Itoa(part)
}

func xliffOriginalDataValues(data []XLIFFOriginalData) map[string]string {
	values := make(map[string]string, len(data))
	for _, original := range data {
		values[original.ID] = original.Value
	}
	return values
}

func collectXLIFFInlineCodeIDs(nodes []XLIFFInline, used map[string]struct{}) {
	for _, node := range nodes {
		if node.Kind != XLIFFInlineText && node.ID != "" {
			used[node.ID] = struct{}{}
		}
		collectXLIFFInlineCodeIDs(node.Children, used)
	}
}

func firstProtectedTerm(value string, terms []string) (int, string) {
	location := -1
	selected := ""
	for _, term := range terms {
		index := strings.Index(value, term)
		if index < 0 || (location >= 0 && index > location) {
			continue
		}
		if location < 0 || index < location || len(term) > len(selected) {
			location, selected = index, term
		}
	}
	return location, selected
}
