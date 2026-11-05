package translation

import (
	"fmt"
	"slices"
	"strings"
)

// XLIFFUnitMarkup is one translation unit with its source, and current target
// when one exists, written as XLIFF 2.2 inline markup. Codes carry only their
// id; a placeholder also shows the text it stands for in equiv.
type XLIFFUnitMarkup struct {
	ID      string
	GroupID string
	Context *string
	Source  string
	Target  *string
}

// XLIFFUnitsMarkup renders every unit of document as inline markup.
func XLIFFUnitsMarkup(document XLIFFDocument) ([]XLIFFUnitMarkup, error) {
	emailTemplate := isEmailTemplateFile(document.File.ID)
	var units []XLIFFUnitMarkup
	for _, group := range document.File.Groups {
		for _, unit := range group.TranslationUnit {
			normalized, err := normalizeXLIFFUnitInline(unit, emailTemplate)
			if err != nil {
				return nil, fmt.Errorf("XLIFF unit %q: %w", unit.ID, err)
			}
			data := originalDataValues(normalized.OriginalData)
			markup := XLIFFUnitMarkup{
				ID: unit.ID, GroupID: group.ID, Context: unit.Context,
				Source: renderXLIFFInlineMarkup(normalized.SourceInline, data),
			}
			if normalized.Target != nil {
				target := renderXLIFFInlineMarkup(normalized.TargetInline, data)
				markup.Target = &target
			}
			units = append(units, markup)
		}
	}
	return units, nil
}

// XLIFFDocumentFromMarkup returns document reduced to the units named in
// targets, each carrying its parsed target. Every code in a target must be a
// source code of the same unit; the interchange validation that import runs
// still decides whether the target keeps the source structure.
func XLIFFDocumentFromMarkup(document XLIFFDocument, targets map[string]string) (XLIFFDocument, error) {
	emailTemplate := isEmailTemplateFile(document.File.ID)
	remaining := make(map[string]string, len(targets))
	for handle, markup := range targets {
		remaining[handle] = markup
	}
	result := document
	result.File.Groups = nil
	for _, group := range document.File.Groups {
		next := group
		next.TranslationUnit = nil
		for _, unit := range group.TranslationUnit {
			markup, requested := remaining[unit.ID]
			if !requested {
				continue
			}
			delete(remaining, unit.ID)
			translated, err := xliffUnitWithMarkupTarget(unit, markup, emailTemplate)
			if err != nil {
				return XLIFFDocument{}, fmt.Errorf("unit %q: %w", unit.ID, err)
			}
			next.TranslationUnit = append(next.TranslationUnit, translated)
		}
		if len(next.TranslationUnit) != 0 {
			result.File.Groups = append(result.File.Groups, next)
		}
	}
	if len(remaining) != 0 {
		unknown := make([]string, 0, len(remaining))
		for handle := range remaining {
			unknown = append(unknown, handle)
		}
		slices.Sort(unknown)
		return XLIFFDocument{}, fmt.Errorf("unit %q is not a current translation unit", unknown[0])
	}
	return result, nil
}

func xliffUnitWithMarkupTarget(unit XLIFFUnit, markup string, emailTemplate bool) (XLIFFUnit, error) {
	normalized, err := normalizeXLIFFUnitInline(unit, emailTemplate)
	if err != nil {
		return XLIFFUnit{}, err
	}
	nodes, err := decodeXLIFFInlineContent(markup)
	if err != nil {
		return XLIFFUnit{}, fmt.Errorf("invalid target markup: %w", err)
	}
	sourceCodes := make(map[string]XLIFFInline)
	collectXLIFFCodes(normalized.SourceInline, sourceCodes)
	if err := adoptSourceCodeReferences(nodes, sourceCodes); err != nil {
		return XLIFFUnit{}, err
	}
	target, err := ProjectXLIFFInline(nodes, normalized.OriginalData)
	if err != nil {
		return XLIFFUnit{}, fmt.Errorf("invalid target markup: %w", err)
	}
	normalized.Target = &target
	normalized.TargetInline = nodes
	return normalized, nil
}

func collectXLIFFCodes(nodes []XLIFFInline, codes map[string]XLIFFInline) {
	for _, node := range nodes {
		if node.Kind == XLIFFInlineText {
			continue
		}
		codes[node.ID] = node
		collectXLIFFCodes(node.Children, codes)
	}
}

// adoptSourceCodeReferences gives each target code the data references and
// edit permissions of the source code with the same id and kind, so callers
// write only ids.
func adoptSourceCodeReferences(nodes []XLIFFInline, sourceCodes map[string]XLIFFInline) error {
	for index := range nodes {
		node := &nodes[index]
		if node.Kind == XLIFFInlineText {
			continue
		}
		source, ok := sourceCodes[node.ID]
		if !ok || source.Kind != node.Kind {
			return fmt.Errorf("target code %s %q does not match a source code", node.Kind, node.ID)
		}
		node.DataRef, node.DataRefStart, node.DataRefEnd = source.DataRef, source.DataRefStart, source.DataRefEnd
		node.CanCopy, node.CanDelete = source.CanCopy, source.CanDelete
		if err := adoptSourceCodeReferences(node.Children, sourceCodes); err != nil {
			return err
		}
	}
	return nil
}

func originalDataValues(originalData []XLIFFOriginalData) map[string]string {
	values := make(map[string]string, len(originalData))
	for _, original := range originalData {
		values[original.ID] = original.Value
	}
	return values
}

// markupTextEscaper and markupAttributeEscaper escape only what XML requires,
// so line breaks and quotes in text stay readable.
var (
	markupTextEscaper      = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	markupAttributeEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
)

func renderXLIFFInlineMarkup(nodes []XLIFFInline, data map[string]string) string {
	var output strings.Builder
	writeXLIFFInlineMarkup(&output, nodes, data)
	return output.String()
}

func writeXLIFFInlineMarkup(output *strings.Builder, nodes []XLIFFInline, data map[string]string) {
	for _, node := range nodes {
		switch node.Kind {
		case XLIFFInlineText:
			output.WriteString(markupTextEscaper.Replace(node.Text))
		case XLIFFInlinePlaceholder:
			output.WriteString(`<ph id="`)
			output.WriteString(markupAttributeEscaper.Replace(node.ID))
			if equiv := data[node.DataRef]; equiv != "" {
				output.WriteString(`" equiv="`)
				output.WriteString(markupAttributeEscaper.Replace(equiv))
			}
			output.WriteString(`"/>`)
		case XLIFFInlinePairedCode:
			output.WriteString(`<pc id="`)
			output.WriteString(markupAttributeEscaper.Replace(node.ID))
			output.WriteString(`">`)
			writeXLIFFInlineMarkup(output, node.Children, data)
			output.WriteString(`</pc>`)
		}
	}
}
