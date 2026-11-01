package ai

import (
	"fmt"
	"testing"

	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestParseMetadataSuggestionPayload(t *testing.T) {
	t.Parallel()

	t.Run("recovers wrapped JSON and snake_case keys", func(t *testing.T) {
		t.Parallel()

		suggestion, err := parseMetadataSuggestionPayload(
			`Here is the result: {"metadata":{"summary":"Quiet summary","notes":"ignore me"}}`,
			[]string{"summary"},
		)
		require.NoError(t, err)
		assert.Equal(t, map[string]string{
			"summary": "Quiet summary",
		}, suggestion)
	})

	t.Run("rejects payloads without requested fields", func(t *testing.T) {
		t.Parallel()

		suggestion, err := parseMetadataSuggestionPayload(
			`{"notes":"Measured title"}`,
			[]string{"summary"},
		)
		require.Error(t, err)
		assert.Nil(t, suggestion)
	})
}

func TestValidateMetadataAIUserPromptRequiresSupportedRequestedKeys(t *testing.T) {
	t.Parallel()

	require.NoError(t, validateMetadataAIUserPrompt(`{"task":{"requestedKeys":["summary"]},"source":{"title":"Page"}}`))
	require.Error(t, validateMetadataAIUserPrompt(`{"task":{"requestedKeys":[]},"source":{"title":"Page"}}`))
	require.Error(t, validateMetadataAIUserPrompt(`{"task":{"requestedKeys":["title"]},"source":{"title":"Page"}}`))
}

func TestMetadataSuggestionRegistryKeepsSchemaValidationAndProtoMappingInParity(t *testing.T) {
	t.Parallel()

	require.NotEmpty(t, metadataSuggestionRegistry)
	descriptor := (&managev1.MetadataSuggestion{}).ProtoReflect().Descriptor()
	for key, definition := range metadataSuggestionRegistry {
		t.Run(key, func(t *testing.T) {
			field := descriptor.Fields().ByJSONName(key)
			require.NotNil(t, field, "accepted metadata keys must map to a protobuf field")
			require.Equal(t, field.Kind().String(), definition.responseSchema["type"])
			require.NotNil(t, suggestionFields[key], "accepted metadata keys must map to a wire field")

			prompt := fmt.Sprintf(`{"task":{"requestedKeys":[%q]}}`, key)
			require.NoError(t, validateMetadataAIUserPrompt(prompt))
			schema := buildMetadataResponseJSONSchema(t.Context(), prompt)
			properties, ok := schema["properties"].(structured.Fields)
			require.True(t, ok)
			require.Contains(t, properties, key)
			require.Equal(t, []string{key}, schema["required"])

			parsed, err := parseMetadataSuggestionPayload(fmt.Sprintf(`{"%s":" mapped value "}`, key), []string{key})
			require.NoError(t, err)
			require.Equal(t, "mapped value", parsed[key])

			message := suggestionMessage(parsed)
			reflected := message.ProtoReflect()
			require.True(t, reflected.Has(field), "accepted metadata keys must map to a present protobuf field")
			require.Equal(t, parsed[key], reflected.Get(field).String())
		})
	}
}
