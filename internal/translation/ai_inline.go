package translation

import "fmt"

// AI translates text and orders opaque code IDs. Native references and code
// attributes belong to the source document, not to the language model.
func aiSourceInline(source []XLIFFInline) []XLIFFInline {
	result := make([]XLIFFInline, len(source))
	for index, node := range source {
		result[index] = XLIFFInline{Kind: node.Kind, ID: node.ID, Text: node.Text}
		if node.Kind == XLIFFInlinePairedCode {
			result[index].Children = aiSourceInline(node.Children)
		}
	}
	return result
}

func restoreAISourceInline(source, target []XLIFFInline) ([]XLIFFInline, error) {
	codes := make(map[string]XLIFFInline)
	var collect func([]XLIFFInline)
	collect = func(nodes []XLIFFInline) {
		for _, node := range nodes {
			if node.Kind != XLIFFInlineText {
				codes[node.ID] = node
				collect(node.Children)
			}
		}
	}
	collect(source)
	var restore func([]XLIFFInline) ([]XLIFFInline, error)
	restore = func(nodes []XLIFFInline) ([]XLIFFInline, error) {
		result := make([]XLIFFInline, len(nodes))
		for index, node := range nodes {
			if node.Kind == XLIFFInlineText {
				result[index] = node
				continue
			}
			original, ok := codes[node.ID]
			if !ok || node.Kind != original.Kind {
				return nil, fmt.Errorf("AI inline code identity changed")
			}
			if node.DataRef != "" || node.DataRefStart != "" || node.DataRefEnd != "" || node.CanCopy != "" || node.CanDelete != "" {
				return nil, fmt.Errorf("AI inline code contains source-owned attributes")
			}
			children, err := restore(node.Children)
			if err != nil {
				return nil, err
			}
			original.Children = children
			result[index] = original
		}
		return result, nil
	}
	return restore(target)
}
