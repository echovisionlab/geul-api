package translation

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func parseHTMLFragment(contentHTML string) (*html.Node, error) {
	root := &html.Node{Type: html.ElementNode, DataAtom: atom.Div, Data: "div"}
	nodes, err := html.ParseFragment(strings.NewReader(contentHTML), root)
	if err != nil {
		return nil, err
	}
	for _, node := range nodes {
		root.AppendChild(node)
	}
	return root, nil
}

func isExcludedHTMLTextNode(node *html.Node) bool {
	for current := node.Parent; current != nil; current = current.Parent {
		if current.Type == html.ElementNode && IsExcludedHTMLElement(current.DataAtom) {
			return true
		}
	}
	return false
}

func htmlPathID(path []int) string {
	if len(path) == 0 {
		return "root"
	}
	parts := make([]string, 0, len(path))
	for _, index := range path {
		parts = append(parts, fmt.Sprintf("%d", index))
	}
	return strings.Join(parts, ".")
}

// ApplyHTMLCandidate overlays translated DOM-path units and renders the exact
// source fragment structure.
func ApplyHTMLCandidate(
	contentHTML string,
	resultByUnit map[string]UnitResult,
) (*string, *string, error) {
	root, err := parseHTMLFragment(contentHTML)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse source content_html: %w", err)
	}
	applyHTMLResults(root, nil, resultByUnit)

	var buffer bytes.Buffer
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if err := html.Render(&buffer, child); err != nil {
			return nil, nil, fmt.Errorf("failed to render translated html fragment: %w", err)
		}
	}
	rendered := strings.TrimSpace(buffer.String())
	text := extractHTMLText(root)
	return nonEmptyHTMLString(rendered), nonEmptyHTMLString(text), nil
}

func applyHTMLResults(node *html.Node, path []int, results map[string]UnitResult) {
	childIndex := 0
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		childPath := append(append([]int(nil), path...), childIndex)
		if child.Type == html.TextNode {
			trimmed := strings.TrimSpace(child.Data)
			if trimmed != "" && !IsPurePlaceholderText(trimmed) {
				if result, ok := results["html:text:"+htmlPathID(childPath)]; ok {
					child.Data = PreserveSourceEdgeWhitespace(child.Data, result.TranslatedText)
				}
			}
		}
		applyHTMLResults(child, childPath, results)
		childIndex++
	}
	normalizeHTMLBoundaryWhitespace(node)
}

func extractHTMLText(root *html.Node) string {
	parts := make([]string, 0)
	var collect func(*html.Node)
	collect = func(node *html.Node) {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.TextNode {
				text := strings.TrimSpace(child.Data)
				if text != "" && !IsPurePlaceholderText(text) && !isExcludedHTMLTextNode(child) {
					parts = append(parts, text)
				}
			}
			collect(child)
		}
	}
	collect(root)
	return strings.Join(parts, "\n")
}

func normalizeHTMLBoundaryWhitespace(node *html.Node) {
	for child := node.FirstChild; child != nil && child.NextSibling != nil; child = child.NextSibling {
		next := child.NextSibling
		if !isInlineHTMLBoundaryNode(child) || !isInlineHTMLBoundaryNode(next) {
			continue
		}
		leftLeaf := rightmostHTMLTextNode(child)
		rightLeaf := leftmostHTMLTextNode(next)
		if leftLeaf == nil || rightLeaf == nil || !htmlBoundaryNeedsWhitespace(leftLeaf.Data, rightLeaf.Data) {
			continue
		}
		if child.Type == html.TextNode {
			leftLeaf.Data += " "
		} else {
			rightLeaf.Data = " " + rightLeaf.Data
		}
	}
}

func isInlineHTMLBoundaryNode(node *html.Node) bool {
	if node == nil {
		return false
	}
	if node.Type == html.TextNode {
		trimmed := strings.TrimSpace(node.Data)
		return trimmed != "" && !IsPurePlaceholderText(trimmed)
	}
	if node.Type != html.ElementNode {
		return false
	}
	switch node.DataAtom {
	case atom.A, atom.Span, atom.Strong, atom.Em, atom.B, atom.I, atom.U,
		atom.Code, atom.Mark, atom.Small, atom.Sub, atom.Sup:
		return true
	default:
		return false
	}
}

func leftmostHTMLTextNode(node *html.Node) *html.Node {
	if node == nil {
		return nil
	}
	if node.Type == html.TextNode {
		trimmed := strings.TrimSpace(node.Data)
		if trimmed == "" || IsPurePlaceholderText(trimmed) {
			return nil
		}
		return node
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if leaf := leftmostHTMLTextNode(child); leaf != nil {
			return leaf
		}
	}
	return nil
}

func rightmostHTMLTextNode(node *html.Node) *html.Node {
	if node == nil {
		return nil
	}
	if node.Type == html.TextNode {
		trimmed := strings.TrimSpace(node.Data)
		if trimmed == "" || IsPurePlaceholderText(trimmed) {
			return nil
		}
		return node
	}
	for child := node.LastChild; child != nil; child = child.PrevSibling {
		if leaf := rightmostHTMLTextNode(child); leaf != nil {
			return leaf
		}
	}
	return nil
}

func htmlBoundaryNeedsWhitespace(leftText, rightText string) bool {
	if strings.TrimSpace(leftText) == "" || strings.TrimSpace(rightText) == "" {
		return false
	}
	if strings.TrimRightFunc(leftText, unicode.IsSpace) != leftText ||
		strings.TrimLeftFunc(rightText, unicode.IsSpace) != rightText {
		return false
	}
	leftRunes := []rune(strings.TrimSpace(leftText))
	rightRunes := []rune(strings.TrimSpace(rightText))
	if len(leftRunes) == 0 || len(rightRunes) == 0 {
		return false
	}
	return runePrefersExplicitWordSpacing(leftRunes[len(leftRunes)-1]) &&
		runePrefersExplicitWordSpacing(rightRunes[0])
}

func runePrefersExplicitWordSpacing(value rune) bool {
	if unicode.In(value, unicode.Han, unicode.Hangul, unicode.Hiragana, unicode.Katakana) {
		return false
	}
	if unicode.IsLetter(value) || unicode.IsNumber(value) {
		return true
	}
	return value == '{' || value == '}'
}

func nonEmptyHTMLString(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}
