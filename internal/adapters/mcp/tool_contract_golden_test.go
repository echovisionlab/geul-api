package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files under testdata")

const toolContractGoldenPath = "testdata/tools.golden.json"

// allToolProviders lists every provider the server composition registers.
// Definitions are static package values, so zero-value providers list the
// same catalog as the composed server.
func allToolProviders() []ToolProvider {
	return []ToolProvider{
		&DocumentDiscoveryTools{}, &DocumentSearchTools{}, &MenuDiscoveryTools{}, &FileListTools{}, &ReferenceSearchTools{}, &DocumentOperationTools{},
		&ContentManagementTools{}, &LegalPolicyTools{}, &EmailLayoutSourceTools{},
		&ProgramEventManagementTools{}, &ProgramEventMediaTools{}, &ReleaseManagementTools{}, &ReleaseRelationTools{},
		&TrackManagementTools{}, &MapPlaceManagementTools{}, &MapThemeManagementTools{}, &MemberAdminTools{},
		&AdminSettingsTools{}, &OAuthAppTools{}, &ContentRelatedTools{}, &AIDocumentTools{}, &DocumentTextTools{},
		&DocumentCatalogTools{}, &TranslationTools{}, &FileTools{}, &FileBlockTools{}, &FileManagerTools{},
	}
}

func listAllTools(t *testing.T) []mcpserver.Tool {
	t.Helper()
	set, err := NewToolSet(allToolProviders()...)
	if err != nil {
		t.Fatalf("NewToolSet: %v", err)
	}
	tools, err := set.ListTools(context.Background(), mcpserver.Principal{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools
}

// TestToolContractGolden pins the complete tools/list contract: names,
// titles, descriptions, input and output schemas, annotations and security
// metadata. Any schema change shows up as a deliberate golden diff; run with
// -update to accept it.
func TestToolContractGolden(t *testing.T) {
	encoded, err := json.MarshalIndent(listAllTools(t), "", "  ")
	if err != nil {
		t.Fatalf("marshal tools: %v", err)
	}
	encoded = append(encoded, '\n')
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(toolContractGoldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(toolContractGoldenPath, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(toolContractGoldenPath)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if !bytes.Equal(encoded, want) {
		t.Fatalf("tools/list contract differs from %s; run go test -run TestToolContractGolden -update and review the diff", toolContractGoldenPath)
	}
}

// TestAllToolProvidersCoversEveryProvider keeps allToolProviders and the
// package's ToolProvider implementations in step, so a new provider cannot
// escape the golden and argument contract checks.
func TestAllToolProvidersCoversEveryProvider(t *testing.T) {
	declared := map[string]bool{}
	for _, file := range parsePackageSource(t) {
		for _, decl := range file.Decls {
			function, ok := decl.(*ast.FuncDecl)
			if !ok || function.Recv == nil || function.Name.Name != "ToolNames" {
				continue
			}
			receiver := function.Recv.List[0].Type
			if star, ok := receiver.(*ast.StarExpr); ok {
				receiver = star.X
			}
			if ident, ok := receiver.(*ast.Ident); ok && ident.Name != "ToolSet" {
				declared[ident.Name] = true
			}
		}
	}
	listed := map[string]bool{}
	for _, provider := range allToolProviders() {
		listed[reflect.TypeOf(provider).Elem().Name()] = true
	}
	for name := range declared {
		if !listed[name] {
			t.Errorf("provider %s is missing from allToolProviders", name)
		}
	}
	for name := range listed {
		if !declared[name] {
			t.Errorf("allToolProviders lists unknown provider %s", name)
		}
	}
}

// parsePackageSource parses this package's non-test Go files.
func parsePackageSource(t *testing.T) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files := token.NewFileSet()
	var parsed []*ast.File
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(files, entry.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, file)
	}
	return parsed
}
