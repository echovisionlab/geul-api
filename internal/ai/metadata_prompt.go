package ai

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/structured"
)

const metadataAIProviderTimeout = 90 * time.Second

const metadataAISystemPrompt = "You generate metadata suggestions for a publishing system. The user message is a JSON object containing task and source data. Return only a single valid JSON object that matches the response schema. Never output HTML, markdown, or explanatory text. Keep wording restrained, precise, and factual, and do not invent unsupported claims."

var metadataJSONPropertyDefinitions = map[string]structured.Fields{
	"summary": {
		"type":        "string",
		"description": "Plain-text standalone synopsis. Begin with the primary subject, state what it is or does, include only the clearest distinguishing context from the source, and keep it suitable for search engines and AI answer systems.",
	},
}

type metadataContextPayload struct {
	Task struct {
		RequestedKeys []string `json:"requestedKeys"`
	} `json:"task"`
	Source struct {
		Title string `json:"title"`
	} `json:"source"`
}

func buildMetadataResponseJSONSchema(userPrompt string) structured.Fields {
	payload, err := parseMetadataContextPayload(userPrompt)
	if err != nil || len(payload.Task.RequestedKeys) == 0 {
		return nil
	}

	properties := structured.Fields{}
	required := make([]string, 0, len(payload.Task.RequestedKeys))
	for _, key := range payload.Task.RequestedKeys {
		property, ok := metadataJSONPropertyDefinitions[key]
		if !ok {
			continue
		}
		properties[key] = property
		required = append(required, key)
	}
	if len(required) == 0 {
		return nil
	}

	return structured.Fields{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

func parseMetadataContextPayload(userPrompt string) (*metadataContextPayload, error) {
	trimmed := strings.TrimSpace(userPrompt)
	if trimmed == "" {
		return nil, fmt.Errorf("metadata user prompt is empty")
	}

	var payload metadataContextPayload
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func metadataAIUserPrompt(contextText, prompt string) string {
	if strings.TrimSpace(contextText) != "" {
		return contextText
	}
	return prompt
}

func validateMetadataAIUserPrompt(userPrompt string) error {
	payload, err := parseMetadataContextPayload(userPrompt)
	if err != nil {
		return errs.InvalidArgument("context", "metadata-json requires a valid structured JSON payload")
	}
	if payload == nil || len(payload.Task.RequestedKeys) == 0 {
		return errs.InvalidArgument("context", "metadata-json requires at least one requested key")
	}
	for _, key := range payload.Task.RequestedKeys {
		if _, ok := metadataSuggestionResponseKeys[key]; !ok {
			return errs.InvalidArgument("context", fmt.Sprintf("metadata-json does not support requested key %q", key))
		}
	}
	return nil
}
