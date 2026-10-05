//go:build ignore

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type violation struct {
	path     string
	position token.Position
}

type nestedIfVisitor struct {
	fileSet    *token.FileSet
	path       string
	ifDepth    int
	violations *[]violation
}

func main() {
	roots := os.Args[1:]
	if len(roots) == 0 {
		roots = []string{"."}
	}

	var violations []violation
	for _, root := range roots {
		if err := inspectRoot(root, &violations); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	sort.Slice(violations, func(left, right int) bool {
		if violations[left].path == violations[right].path {
			return violations[left].position.Offset < violations[right].position.Offset
		}
		return violations[left].path < violations[right].path
	})
	for _, finding := range violations {
		fmt.Fprintf(
			os.Stderr,
			"%s:%d:%d: nested if statement; use a guard clause or extract the inner decision\n",
			finding.path,
			finding.position.Line,
			finding.position.Column,
		)
	}
	if len(violations) > 0 {
		os.Exit(1)
	}
}

func inspectRoot(root string, violations *[]violation) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if shouldSkipDirectory(root, path, entry) {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		if !isGoFile(path) {
			return nil
		}
		return inspectFile(root, path, violations)
	})
}

func shouldSkipDirectory(root, path string, entry fs.DirEntry) bool {
	return entry.IsDir() && path != root && ignoredDirectory(entry.Name())
}

func ignoredDirectory(name string) bool {
	switch name {
	case ".git", "coverage", "dist", "node_modules", "vendor":
		return true
	default:
		return false
	}
}

func isGoFile(path string) bool {
	return strings.HasSuffix(filepath.Base(path), ".go")
}

func inspectFile(root, path string, violations *[]violation) error {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if ast.IsGenerated(file) {
		return nil
	}
	displayPath, err := filepath.Rel(root, path)
	if err != nil {
		displayPath = path
	}
	ast.Walk(nestedIfVisitor{fileSet: fileSet, path: displayPath, violations: violations}, file)
	return nil
}

func (visitor nestedIfVisitor) Visit(node ast.Node) ast.Visitor {
	if node == nil {
		return nil
	}
	switch typed := node.(type) {
	case *ast.FuncDecl:
		ast.Walk(visitor.withDepth(0), typed.Body)
		return nil
	case *ast.FuncLit:
		ast.Walk(visitor.withDepth(0), typed.Body)
		return nil
	case *ast.IfStmt:
		visitor.inspectIf(typed)
		return nil
	default:
		return visitor
	}
}

func (visitor nestedIfVisitor) inspectIf(statement *ast.IfStmt) {
	if visitor.ifDepth > 0 {
		*visitor.violations = append(*visitor.violations, violation{
			path:     visitor.path,
			position: visitor.fileSet.Position(statement.If),
		})
	}
	nested := visitor.withDepth(visitor.ifDepth + 1)
	ast.Walk(nested, statement.Init)
	ast.Walk(nested, statement.Cond)
	ast.Walk(nested, statement.Body)
	if elseIf, ok := statement.Else.(*ast.IfStmt); ok {
		ast.Walk(visitor, elseIf)
		return
	}
	ast.Walk(nested, statement.Else)
}

func (visitor nestedIfVisitor) withDepth(depth int) nestedIfVisitor {
	visitor.ifDepth = depth
	return visitor
}
