package ai

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func parseMetadataSuggestionPayload(
	text string,
	requestedKeys []string,
) (map[string]string, error) {
	objectText := extractJSONObjectText(text)
	var raw structured.Value
	if err := json.Unmarshal([]byte(objectText), &raw); err != nil {
		return nil, fmt.Errorf("metadata AI response is not valid JSON: %w", err)
	}

	unwrapped := unwrapMetadataSuggestionObject(raw)
	record, ok := unwrapped.(structured.Fields)
	if !ok {
		return nil, fmt.Errorf("metadata AI response must be a JSON object")
	}

	allowed := map[string]struct{}{}
	for _, key := range requestedKeys {
		if _, ok := metadataSuggestionRegistry[key]; ok {
			allowed[key] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("metadata AI response requested_keys are empty")
	}

	normalized := map[string]string{}
	for rawKey, value := range record {
		normalizedKey := normalizeMetadataSuggestionKey(rawKey)
		if _, ok := allowed[normalizedKey]; !ok {
			continue
		}
		stringValue, ok := value.(string)
		if !ok {
			continue
		}
		stringValue = strings.TrimSpace(stringValue)
		if stringValue == "" {
			continue
		}
		normalized[normalizedKey] = stringValue
	}

	if len(normalized) == 0 {
		return nil, fmt.Errorf("metadata AI response did not include any requested fields")
	}
	return normalized, nil
}

func extractJSONObjectText(text string) string {
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
	for index := start; index < len(trimmed); index += 1 {
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
			depth += 1
		case '}':
			depth -= 1
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

	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```JSON")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	return strings.TrimSpace(trimmed)
}

func unwrapMetadataSuggestionObject(value structured.Value) structured.Value {
	record, ok := value.(structured.Fields)
	if !ok {
		return value
	}

	for _, key := range []string{"metadata", "suggestion", "result", "data", "output"} {
		candidate, exists := record[key]
		if !exists {
			continue
		}
		if _, ok := candidate.(structured.Fields); ok {
			return candidate
		}
	}

	return value
}

func normalizeMetadataSuggestionKey(key string) string {
	switch key {
	case "summary":
		return "summary"
	default:
		return key
	}
}

func buildMetadataSuggestionMessage(values map[string]string) *managev1.MetadataSuggestion {
	if len(values) == 0 {
		return nil
	}

	message := &managev1.MetadataSuggestion{}
	for key, value := range values {
		definition, ok := metadataSuggestionRegistry[key]
		if !ok {
			continue
		}
		if value = strings.TrimSpace(value); value != "" {
			definition.setProto(message, value)
		}
	}
	return message
}
