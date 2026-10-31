package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// genericFailureMessages are the messages of the per-handler failure logs
// that repeated request.completed. A failure that a request, tool call or
// queue message records once must not be logged again under one of them.
var genericFailureMessages = map[string]bool{
	"Operation failed":                    true,
	"content operation failed":            true,
	"RPC failed":                          true,
	"Media RPC failed":                    true,
	"Content document operation rejected": true,
}

// TestFailuresAreNotLoggedBesideTheirTerminalRecord keeps the single failure
// record of docs/logging.md: a handler returns its failure with the cause on
// the error instead of deferring a terminal logger or logging a generic
// failure line.
func TestFailuresAreNotLoggedBesideTheirTerminalRecord(t *testing.T) {
	repositoryRoot := findRepositoryRoot(t)
	for _, root := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(repositoryRoot, root), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fileSet := token.NewFileSet()
			file, err := parser.ParseFile(fileSet, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(node ast.Node) bool {
				switch typed := node.(type) {
				case *ast.DeferStmt:
					if literal, ok := typed.Call.Fun.(*ast.FuncLit); ok && callsSelector(literal.Body, "OperationLogLevel") {
						t.Errorf("%s: deferred terminal failure logger; return the cause on the error instead", fileSet.Position(typed.Pos()))
					}
				case *ast.CallExpr:
					if message, ok := slogMessage(typed); ok && genericFailureMessages[message] {
						t.Errorf("%s: generic failure log %q; return the cause on the error or log a specific event", fileSet.Position(typed.Pos()), message)
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("inspect failure logging root %s: %v", root, err)
		}
	}
}

func callsSelector(node ast.Node, name string) bool {
	found := false
	ast.Inspect(node, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok {
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == name {
				found = true
			}
		}
		return !found
	})
	return found
}

// slogMessage returns the literal message of a log/slog call.
func slogMessage(call *ast.CallExpr) (string, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	if receiver, ok := selector.X.(*ast.Ident); !ok || receiver.Name != "slog" {
		return "", false
	}
	position := 0
	switch selector.Sel.Name {
	case "Debug", "Info", "Warn", "Error":
	case "DebugContext", "InfoContext", "WarnContext", "ErrorContext":
		position = 1
	case "Log", "LogAttrs":
		position = 2
	default:
		return "", false
	}
	if len(call.Args) <= position {
		return "", false
	}
	literal, ok := call.Args[position].(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	message, err := strconv.Unquote(literal.Value)
	return message, err == nil
}
