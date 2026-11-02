package mcp

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInputSchemaKeepsPublishedAnnotationsAndOrder(t *testing.T) {
	type identity struct {
		ID string `json:"id"`
	}
	type item struct {
		Enabled bool `json:"enabled"`
	}
	type input struct {
		identity
		Items   []*item         `json:"items"`
		Payload json.RawMessage `json:"payload"`
	}
	annotations := json.RawMessage(`{"description":"Copy the observed ID unchanged.","type":"object","additionalProperties":false,"required":["id"],"properties":{"items":{"type":["array","null"],"maxItems":10,"items":{"type":"object","properties":{"enabled":{"type":"boolean","default":true}}}},"id":{"type":"string","format":"uuid"},"payload":{"oneOf":[{"type":"string"},{"type":"object","properties":{"custom":{"const":1}}}]}}}`)
	generated, err := generateInputSchema(annotations, reflect.TypeFor[input]())
	require.NoError(t, err)
	require.Equal(t, string(annotations), string(generated))
}

func TestInputSchemaRejectsDecoderDrift(t *testing.T) {
	type input struct {
		Limit int `json:"limit"`
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	tests := []struct{ name, schema, path string }{
		{"field removed", `{"properties":{"offset":{"type":"integer"}}}`, "$.offset has no registered input field"},
		{"kind changed", `{"properties":{"limit":{"type":"string"}}}`, "$.limit.type"},
		{"nested field removed", `{"properties":{"items":{"type":"array","items":{"properties":{"name":{"type":"string"}}}}}}`, "$.items[].name has no registered input field"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := generateInputSchema(json.RawMessage(test.schema), reflect.TypeFor[input]())
			require.ErrorContains(t, err, test.path)
		})
	}
}

func TestInputSchemaCombinesActionDecoderShapes(t *testing.T) {
	type begin struct {
		Action string `json:"action"`
		Size   int64  `json:"size"`
	}
	type status struct {
		Action string   `json:"action"`
		Handle []string `json:"handle"`
	}
	annotations := json.RawMessage(`{"type":"object","required":["action"],"properties":{"action":{"enum":["begin","status"]},"size":{"type":"integer","minimum":0},"handle":{"type":"array","items":{"type":"string"}}}}`)
	generated, err := generateInputSchema(annotations, reflect.TypeFor[begin](), reflect.TypeFor[status]())
	require.NoError(t, err)
	require.Equal(t, string(annotations), string(generated))
}
