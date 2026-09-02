package translation

import (
	"strings"
)

// SelectResponse projects provider output onto the requested unit ordering.
func SelectResponse(req ProviderRequest, response ProviderResponse) ProviderResponse {
	selectedByUnit := FlattenResponse(response)
	return ProviderResponse{Document: XLIFFWithTargets(req.Document, selectedByUnit)}
}

// NormalizeProtectedTerms trims and deduplicates exact protected spellings.
func NormalizeProtectedTerms(terms []string) []string {
	seen := make(map[string]struct{}, len(terms))
	normalized := make([]string, 0, len(terms))
	for _, term := range terms {
		trimmed := strings.TrimSpace(term)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; !ok {
			seen[trimmed] = struct{}{}
			normalized = append(normalized, trimmed)
		}
	}
	return normalized
}

func extractJSONObject(text string) string {
	trimmed := stripCodeFence(text)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return trimmed
	}
	start := strings.Index(trimmed, "{")
	if start == -1 {
		return trimmed
	}
	depth := 0
	inString := false
	escaped := false
	for index := start; index < len(trimmed); index++ {
		char := trimmed[index]
		if escaped {
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if char == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch char {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return trimmed[start : index+1]
			}
		}
	}
	return trimmed
}

func stripCodeFence(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) >= 2 {
		lines = lines[1:]
		if last := len(lines) - 1; strings.TrimSpace(lines[last]) == "```" {
			lines = lines[:last]
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
