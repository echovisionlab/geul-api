package architecture

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var updateRatchets = flag.Bool("update", false, "lower ratchet baselines under testdata/ratchet to the observed counts; never raises one")

// requireRatchet compares observed violation counts with the checked-in
// baseline testdata/ratchet/<name>.txt, one "<count> <key>" line per key. A
// key that appears or grows fails as a new violation. A key that shrinks or
// disappears also fails, so the baseline stays tight: run the test with
// -update to lower it.
func requireRatchet(t *testing.T, name string, observed map[string]int) {
	t.Helper()
	path := filepath.Join("testdata", "ratchet", name+".txt")
	baseline := readRatchetBaseline(t, path)
	var grown, shrunk []string
	for _, key := range slices.Sorted(maps.Keys(observed)) {
		if observed[key] > baseline[key] {
			grown = append(grown, fmt.Sprintf("%s: %d (baseline %d)", key, observed[key], baseline[key]))
		}
	}
	for _, key := range slices.Sorted(maps.Keys(baseline)) {
		if observed[key] < baseline[key] {
			shrunk = append(shrunk, fmt.Sprintf("%s: %d (baseline %d)", key, observed[key], baseline[key]))
		}
	}
	if len(grown) != 0 {
		t.Fatalf("new %s violations; fix them instead of raising the baseline:\n  %s", name, strings.Join(grown, "\n  "))
	}
	if len(shrunk) == 0 {
		return
	}
	if *updateRatchets {
		writeRatchetBaseline(t, path, observed)
		return
	}
	t.Fatalf("%s violations dropped; lower the baseline with `go test ./internal/architecture -run %s -update`:\n  %s",
		name, t.Name(), strings.Join(shrunk, "\n  "))
}

func readRatchetBaseline(t *testing.T, path string) map[string]int {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ratchet baseline: %v", err)
	}
	baseline := map[string]int{}
	for number, line := range strings.Split(string(content), "\n") {
		if line = strings.TrimSpace(line); line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		count, key, ok := strings.Cut(line, " ")
		value, err := strconv.Atoi(count)
		if !ok || err != nil || value <= 0 {
			t.Fatalf("%s:%d: want \"<count> <key>\", got %q", path, number+1, line)
		}
		baseline[key] = value
	}
	return baseline
}

func writeRatchetBaseline(t *testing.T, path string, observed map[string]int) {
	t.Helper()
	header, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ratchet baseline: %v", err)
	}
	var out strings.Builder
	for _, line := range strings.Split(string(header), "\n") {
		if strings.HasPrefix(line, "#") {
			out.WriteString(line + "\n")
		}
	}
	for _, key := range slices.Sorted(maps.Keys(observed)) {
		if observed[key] > 0 {
			fmt.Fprintf(&out, "%d %s\n", observed[key], key)
		}
	}
	if err := os.WriteFile(path, []byte(out.String()), 0o644); err != nil {
		t.Fatalf("write ratchet baseline: %v", err)
	}
}

// productionFile is one parsed non-test Go file, with its path and package
// directory relative to the repository root.
type productionFile struct {
	path    string
	pkg     string
	syntax  *ast.File
	imports map[string]string
}

// productionFiles parses every non-test Go file under the roots. Test
// support packages and this package are not production code.
func productionFiles(t *testing.T, roots ...string) []productionFile {
	t.Helper()
	repositoryRoot := findRepositoryRoot(t)
	var files []productionFile
	fileSet := token.NewFileSet()
	for _, root := range roots {
		err := filepath.WalkDir(filepath.Join(repositoryRoot, root), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, err := filepath.Rel(repositoryRoot, path)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if entry.IsDir() {
				// A package named *test (audittest, authztest) is test support.
				if relative == "internal/testutil" || strings.HasPrefix(relative, "internal/testutil/") ||
					relative == "internal/architecture" || strings.HasSuffix(entry.Name(), "test") || entry.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			syntax, err := parser.ParseFile(fileSet, path, nil, 0)
			if err != nil {
				return err
			}
			imports := map[string]string{}
			for _, spec := range syntax.Imports {
				importPath, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					return err
				}
				name := importPath[strings.LastIndex(importPath, "/")+1:]
				if spec.Name != nil {
					name = spec.Name.Name
				}
				imports[name] = importPath
			}
			files = append(files, productionFile{path: relative, pkg: filepath.ToSlash(filepath.Dir(relative)), syntax: syntax, imports: imports})
			return nil
		})
		if err != nil {
			t.Fatalf("parse production files under %s: %v", root, err)
		}
	}
	return files
}

// importedAs reports whether expression is a selector of the package the
// file imports from importPath, such as connect.NewRequest.
func (f productionFile) importedAs(expression ast.Expr, importPath, name string) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != name {
		return false
	}
	ident, ok := selector.X.(*ast.Ident)
	return ok && f.imports[ident.Name] == importPath
}

func underRoot(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}
