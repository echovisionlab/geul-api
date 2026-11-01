package architecture

import (
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

func TestPortVocabularyDoesNotHideTransportMessages(t *testing.T) {
	tests := []struct {
		name, path, source string
		transport          bool
	}{
		{"policy descriptors", "internal/post/service.go", `package sample; import v "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"; var actor v.Actor`, false},
		{"Connect boundary", "internal/post/connect_handler.go", `package sample; import v "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"; var asset v.AssetRef`, false},
		{"shared query vocabulary", "internal/post/service.go", `package sample; import q "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"; type Query struct { Filters []*q.FilterSpec; Sorts []*q.SortSpec }; var op = q.FilterOp_FILTER_OP_EQ`, false},
		{"mixed query and transport", "internal/post/service.go", `package sample; import q "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"; type Query struct { Filters []*q.FilterSpec; Asset *q.AssetRef }`, true},
		{"domain error reasons", "internal/post/content.go", `package sample; import v "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"; var reason = v.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED`, false},
		{"error message remains transport", "internal/post/content.go", `package sample; import v "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"; var detail v.CollaborationConflictDetail`, true},
		{"queue decoder", "internal/worker/job_messages.go", `package sample; import v "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"; var event v.SendEmailEvent`, false},
		{"queue business handler", "internal/worker/email.go", `package sample; import v "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"; var event v.SendEmailEvent`, true},
		{"dot import is not vocabulary proof", "internal/post/service.go", `package sample; import . "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"; var asset AssetRef`, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			syntax, err := parser.ParseFile(token.NewFileSet(), test.path, test.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			imports := map[string]string{}
			for _, spec := range syntax.Imports {
				imported, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				imports[spec.Name.Name] = imported
			}
			file := productionFile{path: test.path, syntax: syntax, imports: imports}
			for _, imported := range imports {
				if got := hasTransportContractReferences(file, imported); got != test.transport {
					t.Fatalf("transport references = %v, want %v", got, test.transport)
				}
			}
		})
	}
}
