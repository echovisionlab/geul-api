package ai

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
