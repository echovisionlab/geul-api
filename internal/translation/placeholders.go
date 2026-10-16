package translation

import (
	"regexp"
	"strings"
)

var translationPlaceholderPattern = regexp.MustCompile(`\{\{[^}]+\}\}`)

func SamePlaceholders(sourceText string, translatedText string) bool {
	source := ExtractPlaceholders(sourceText)
	translated := ExtractPlaceholders(translatedText)
	return samePlaceholderMultiset(source, translated)
}

func samePlaceholderMultiset(source, translated []string) bool {
	if len(source) != len(translated) {
		return false
	}
	expected := make(map[string]int, len(source))
	for _, placeholder := range source {
		expected[placeholder]++
	}
	for _, placeholder := range translated {
		if expected[placeholder] == 0 {
			return false
		}
		expected[placeholder]--
	}
	return true
}

func ExtractPlaceholders(value string) []string {
	return translationPlaceholderPattern.FindAllString(value, -1)
}

func CountLineBreaks(value string) int {
	return strings.Count(value, "\n")
}
