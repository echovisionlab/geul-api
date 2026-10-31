package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
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
		t.Fatalf("integration tests must call t.Parallel() first or carry a // %s <reason> comment:\n%s",
			integrationSerialMarker, strings.Join(missing, "\n"))
	}
}

// serialIntegrationTests lists the top-level tests in an integration-tagged
// file that neither start with t.Parallel() nor carry the serial marker.
func serialIntegrationTests(path string) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	if !hasIntegrationBuildTag(file) {
		return nil, nil
	}
	var serial []string
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil || function.Name.Name == "TestMain" ||
			!strings.HasPrefix(function.Name.Name, "Test") || function.Body == nil {
			continue
		}
		if function.Doc != nil && strings.Contains(function.Doc.Text(), integrationSerialMarker) {
			continue
		}
		if !startsWithParallel(function) {
			serial = append(serial, function.Name.Name)
		}
	}
	return serial, nil
}

func hasIntegrationBuildTag(file *ast.File) bool {
	for _, group := range file.Comments {
		if group.Pos() >= file.Package {
			return false
		}
		for _, comment := range group.List {
			if strings.HasPrefix(comment.Text, "//go:build") && strings.Contains(comment.Text, "integration") &&
				!strings.Contains(comment.Text, "!integration") {
				return true
			}
		}
	}
	return false
}

func startsWithParallel(function *ast.FuncDecl) bool {
	if len(function.Type.Params.List) != 1 || len(function.Body.List) == 0 {
		return false
	}
	names := function.Type.Params.List[0].Names
	if len(names) != 1 {
		return false
	}
	statement, ok := function.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := statement.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Parallel" {
		return false
	}
	receiver, ok := selector.X.(*ast.Ident)
	return ok && receiver.Name == names[0].Name
}
