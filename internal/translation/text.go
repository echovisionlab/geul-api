package translation

import (
	"strings"
	"unicode"

	"golang.org/x/net/html/atom"
)

// IsPurePlaceholderText reports whether the complete visible text is one
// protected template placeholder.
func IsPurePlaceholderText(value string) bool {
	placeholders := ExtractPlaceholders(value)
	return len(placeholders) == 1 && placeholders[0] == value
}

// IsExcludedHTMLElement reports whether visible text below an HTML element
// is outside the Translation input contract.
func IsExcludedHTMLElement(element atom.Atom) bool {
	switch element {
	case atom.Head, atom.Script, atom.Style, atom.Title, atom.Noscript, atom.Template:
		return true
	default:
		return false
	}
}

// PreserveSourceEdgeWhitespace applies translated text while retaining the
// source token's leading and trailing Unicode whitespace.
func PreserveSourceEdgeWhitespace(sourceText string, translatedText string) string {
	trimmed := strings.TrimSpace(translatedText)
	if trimmed == "" {
		return ""
	}
	return leadingWhitespace(sourceText) + trimmed + trailingWhitespace(sourceText)
}

func leadingWhitespace(value string) string {
	runes := []rune(value)
	index := 0
	for index < len(runes) && unicode.IsSpace(runes[index]) {
		index++
	}
	return string(runes[:index])
}

func trailingWhitespace(value string) string {
	runes := []rune(value)
	index := len(runes)
	for index > 0 && unicode.IsSpace(runes[index-1]) {
		index--
	}
	return string(runes[index:])
}
