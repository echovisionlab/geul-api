package ai

import "github.com/echovisionlab/geul-api/internal/structured"

const metadataSuggestionKeySummary = "summary"

type metadataSuggestionDefinition struct {
	responseSchema structured.Fields
}

// metadataSuggestionRegistry owns the accepted keys and provider schema of
// metadata suggestions; connect_handler.go maps each key to its field.
var metadataSuggestionRegistry = map[string]metadataSuggestionDefinition{
	metadataSuggestionKeySummary: {
		responseSchema: structured.Fields{
			"type":        "string",
			"description": "Plain-text standalone synopsis. Begin with the primary subject, state what it is or does, include only the clearest distinguishing context from the source, and keep it suitable for search engines and AI answer systems.",
		},
	},
}
