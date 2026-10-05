package ai

import (
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const metadataSuggestionKeySummary = "summary"

type metadataSuggestionDefinition struct {
	responseSchema structured.Fields
	setProto       func(*managev1.MetadataSuggestion, string)
}

// metadataSuggestionRegistry owns the accepted keys, provider schema and
// explicit typed protobuf mapping for metadata suggestions.
var metadataSuggestionRegistry = map[string]metadataSuggestionDefinition{
	metadataSuggestionKeySummary: {
		responseSchema: structured.Fields{
			"type":        "string",
			"description": "Plain-text standalone synopsis. Begin with the primary subject, state what it is or does, include only the clearest distinguishing context from the source, and keep it suitable for search engines and AI answer systems.",
		},
		setProto: func(message *managev1.MetadataSuggestion, value string) {
			message.Summary = &value
		},
	},
}
