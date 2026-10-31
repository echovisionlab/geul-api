package main

import (
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// integrationSerialMarker exempts a test from running in parallel. The comment
// must give the reason, for example process-global state the test replaces.
const integrationSerialMarker = "integration:serial"

// Every integration test owns its database, SpiceDB datastore and buckets, so
// nothing stops it from running alongside the others.
func TestIntegrationTestsRunInParallel(t *testing.T) {
	t.Parallel()
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	err = filepath.WalkDir(repoRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if name := entry.Name(); name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		serial, err := serialIntegrationTests(path)
		if err != nil {
			return err
		}
		for _, name := range serial {
			relative, _ := filepath.Rel(repoRoot, path)
			missing = append(missing, relative+": "+name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) > 0 {
		t.Fatalf("integration tests must call t.Parallel() first, or carry a // %s <reason> comment (required for t.Setenv/t.Chdir):\n%s",
			integrationSerialMarker, strings.Join(missing, "\n"))
	}
}

// serialIntegrationTests lists the top-level tests in an integration-tagged
// file that break the parallel contract: no leading t.Parallel() without the
// serial marker, or process-global t.Setenv/t.Chdir without it (those panic
// once the test is parallel).
func serialIntegrationTests(path string) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	integration, err := hasIntegrationBuildTag(file)
	if err != nil || !integration {
		return nil, err
	}
	var violations []string
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil || function.Name.Name == "TestMain" ||
			!strings.HasPrefix(function.Name.Name, "Test") || function.Body == nil {
			continue
		}
		serial := hasSerialMarker(function.Doc)
		parallel := startsWithParallel(function)
		switch {
		case usesProcessGlobalTestState(function):
			if !serial || parallel {
				violations = append(violations, function.Name.Name+" (uses t.Setenv or t.Chdir)")
			}
		case !serial && !parallel:
			violations = append(violations, function.Name.Name)
		}
	}
	return violations, nil
}

// hasIntegrationBuildTag reports whether the file builds only with the
// integration tag, evaluating the //go:build expression.
func hasIntegrationBuildTag(file *ast.File) (bool, error) {
	for _, group := range file.Comments {
		if group.Pos() >= file.Package {
			return false, nil
		}
		for _, comment := range group.List {
			if !constraint.IsGoBuild(comment.Text) {
				continue
			}
			expression, err := constraint.Parse(comment.Text)
			if err != nil {
				return false, err
			}
			withTag := expression.Eval(func(tag string) bool { return tag == "integration" })
			withoutTag := expression.Eval(func(string) bool { return false })
			return withTag && !withoutTag, nil
		}
	}
	return false, nil
}

func hasSerialMarker(doc *ast.CommentGroup) bool {
	if doc == nil {
		return false
	}
	for _, comment := range doc.List {
		if strings.Contains(comment.Text, integrationSerialMarker) {
			return true
		}
	}
	return false
}

func testingParameter(function *ast.FuncDecl) string {
	if len(function.Type.Params.List) != 1 || len(function.Type.Params.List[0].Names) != 1 {
		return ""
	}
	return function.Type.Params.List[0].Names[0].Name
}

func startsWithParallel(function *ast.FuncDecl) bool {
	name := testingParameter(function)
	if name == "" || len(function.Body.List) == 0 {
		return false
	}
	statement, ok := function.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := statement.X.(*ast.CallExpr)
	return ok && len(call.Args) == 0 && isTestingCall(call, name, "Parallel")
}

func usesProcessGlobalTestState(function *ast.FuncDecl) bool {
	name := testingParameter(function)
	found := false
	ast.Inspect(function.Body, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok && name != "" &&
			(isTestingCall(call, name, "Setenv") || isTestingCall(call, name, "Chdir")) {
			found = true
		}
		return !found
	})
	return found
}

func isTestingCall(call *ast.CallExpr, receiver, method string) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != method {
		return false
	}
	identifier, ok := selector.X.(*ast.Ident)
	return ok && identifier.Name == receiver
}

func TestSerialIntegrationTestsEnforcesParallelContract(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		source string
		want   []string
	}{
		{
			name:   "parallel test passes",
			source: "//go:build integration\n\npackage p\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {\n\tt.Parallel()\n}\n",
		},
		{
			name:   "missing parallel is reported",
			source: "//go:build integration\n\npackage p\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {\n\t_ = t\n}\n",
			want:   []string{"TestA"},
		},
		{
			name:   "serial marker exempts",
			source: "//go:build integration\n\npackage p\n\nimport \"testing\"\n\n// integration:serial owns a fixed port.\nfunc TestA(t *testing.T) {\n\t_ = t\n}\n",
		},
		{
			name:   "setenv after parallel is reported",
			source: "//go:build integration\n\npackage p\n\nimport \"testing\"\n\n// integration:serial changes the environment.\nfunc TestA(t *testing.T) {\n\tt.Parallel()\n\tt.Setenv(\"K\", \"V\")\n}\n",
			want:   []string{"TestA (uses t.Setenv or t.Chdir)"},
		},
		{
			name:   "setenv needs the marker",
			source: "//go:build integration\n\npackage p\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {\n\tt.Setenv(\"K\", \"V\")\n}\n",
			want:   []string{"TestA (uses t.Setenv or t.Chdir)"},
		},
		{
			name:   "compound integration constraint is checked",
			source: "//go:build integration && !race\n\npackage p\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n",
			want:   []string{"TestA"},
		},
		{
			name:   "other tags are ignored",
			source: "//go:build integration_slow || !integration\n\npackage p\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "x_test.go")
			if err := os.WriteFile(path, []byte(testCase.source), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := serialIntegrationTests(path)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, testCase.want) {
				t.Fatalf("violations = %q, want %q", got, testCase.want)
			}
		})
	}
}
