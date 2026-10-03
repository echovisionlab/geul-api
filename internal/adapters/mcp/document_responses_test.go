package mcp

import (
	"encoding/json"
	"reflect"
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
)

func TestAcceptedResponseWireContract(t *testing.T) {
	targetRevision := core.Revision("target-revision")
	emptyRevision := core.Revision("")
	tests := []struct {
		name    string
		result  core.ApplyResult
		created core.BlockID
		want    string
	}{
		{
			name:   "no changes or target revision",
			result: core.ApplyResult{DocumentRevision: "document-revision"},
			want:   `{"c":[],"dr":"document-revision"}`,
		},
		{
			name: "created block with target revision",
			result: core.ApplyResult{DocumentRevision: "document-revision", TargetRevision: &targetRevision, Changes: []core.Change{
				{Operation: 0, Kind: core.OperationInsertBlock, AffectedHandles: []string{"block-a", "section-a"}},
				{Operation: 1, Kind: core.OperationSetField},
			}},
			created: "block-a",
			want:    `{"block_id":"block-a","c":[[0,"bi",["block-a","section-a"]],[1,"fs",null]],"dr":"document-revision","tr":"target-revision"}`,
		},
		{
			name: "empty handles remain null and supplied empty revision remains present",
			result: core.ApplyResult{DocumentRevision: "document-revision", TargetRevision: &emptyRevision, Changes: []core.Change{
				{Operation: 0, Kind: core.OperationDeleteBlock, AffectedHandles: []string{}},
			}},
			want: `{"c":[[0,"bd",null]],"dr":"document-revision","tr":""}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := encodeFocusedAccepted(test.result, test.created)
			if err != nil || string(encoded) != test.want {
				t.Fatalf("accepted response = %s, %v; want %s", encoded, err, test.want)
			}
			var wantStructured map[string]any
			if err := json.Unmarshal([]byte(test.want), &wantStructured); err != nil {
				t.Fatal(err)
			}
			for _, isError := range []bool{false, true} {
				response, err := structuredResult(encoded, isError)
				if err != nil {
					t.Fatal(err)
				}
				if response.IsError != isError || len(response.Content) != 1 || response.Content[0]["text"] != test.want ||
					!reflect.DeepEqual(response.StructuredContent, wantStructured) {
					t.Fatalf("response text/structured/error parity lost: %+v", response)
				}
			}
		})
	}
}

func TestAcceptedResponseFailsClosedBeforeAddingCreatedBlock(t *testing.T) {
	tests := []struct {
		name   string
		result core.ApplyResult
		want   string
	}{
		{name: "missing revision", want: "application returned an accepted mutation without document revision"},
		{
			name:   "negative operation",
			result: core.ApplyResult{DocumentRevision: "revision", Changes: []core.Change{{Operation: -1, Kind: core.OperationInsertBlock}}},
			want:   "application returned a negative accepted operation index",
		},
		{
			name:   "unknown kind",
			result: core.ApplyResult{DocumentRevision: "revision", Changes: []core.Change{{Kind: "unsupported"}}},
			want:   `application returned unsupported accepted operation kind "unsupported"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, created := range []core.BlockID{"", "block-a"} {
				encoded, err := encodeFocusedAccepted(test.result, created)
				if encoded != nil || err == nil || err.Error() != test.want {
					t.Fatalf("accepted response (%q) = %s, %v; want no output and %s", created, encoded, err, test.want)
				}
				response, err := mustAIDocumentTools(t, &recordingAIDocumentApplication{applyResult: test.result}).applyRequest(t.Context(), core.ApplyRequest{}, created)
				if err == nil || err.Error() != "encode MCP accepted mutation: "+test.want || response.Content != nil || response.StructuredContent != nil {
					t.Fatalf("invalid application result leaked through response: %+v, %v", response, err)
				}
			}
		})
	}
}
