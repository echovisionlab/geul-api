package architecture

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/echovisionlab/geul-api"

var migratedDomainRoots = []string{
	"internal/account",
	"internal/ai",
	"internal/authentication",
	"internal/audience",
	"internal/campaign",
	"internal/collaboration",
	"internal/emailauthoring",
	"internal/emaildelivery",
	"internal/filemedia",
	"internal/form",
	"internal/legal",
	"internal/maptheme",
	"internal/member",
	"internal/menu",
	"internal/post",
	"internal/page",
	"internal/programevent",
	"internal/referencecatalog",
	"internal/series",
	"internal/sharelink",
	"internal/sitemap",
	"internal/sitesettings",
	"internal/work",
}

var guardedPackageRoots = []string{
	"internal/account",
	"internal/ai",
	"internal/aidocument",
	"internal/authentication",
	"internal/audience",
	"internal/authorizationtarget",
	"internal/campaign",
	"internal/collaboration",
	"internal/email",
	"internal/emailauthoring",
	"internal/emaildelivery",
	"internal/form",
	"internal/favicon",
	"internal/filemedia",
	"internal/legal",
	"internal/maptheme",
	"internal/mediaasset",
	"internal/mcp",
	"internal/member",
	"internal/menu",
	"internal/og",
	"internal/page",
	"internal/post",
	"internal/programevent",
	"internal/referencecatalog",
	"internal/routeregistry",
	"internal/securityaccess",
	"internal/series",
	"internal/sharelink",
	"internal/sitemap",
	"internal/sitesettings",
	"internal/localization",
	"internal/translation",
	"internal/work",
}

var forbiddenDependencyRoots = []string{
	"internal/adapters",
	"internal/handler",
	"internal/scheduler",
	"internal/service",
	"internal/worker",
	"cmd",
}

func TestMigratedPackagesDoNotDependOnLegacyOrRuntimePackages(t *testing.T) {
	repositoryRoot := findRepositoryRoot(t)
	for _, ownerRoot := range guardedPackageRoots {
		ownerRoot := ownerRoot
		t.Run(strings.TrimPrefix(ownerRoot, "internal/"), func(t *testing.T) {
			visited := make(map[string]bool)
			for _, packagePath := range productionPackagePathsUnder(t, repositoryRoot, ownerRoot) {
				visitLocalDependencies(t, repositoryRoot, ownerRoot, packagePath, visited)
			}
		})
	}
}

func productionPackagePathsUnder(t *testing.T, repositoryRoot string, domainRoot string) []string {
	t.Helper()
	paths := make([]string, 0)
	root := filepath.Join(repositoryRoot, domainRoot)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, child := range entries {
			if !child.IsDir() && strings.HasSuffix(child.Name(), ".go") && !strings.HasSuffix(child.Name(), "_test.go") {
				relative, err := filepath.Rel(repositoryRoot, path)
				if err != nil {
					return err
				}
				paths = append(paths, filepath.ToSlash(relative))
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("discover packages under %s: %v", domainRoot, err)
	}
	return paths
}

func visitLocalDependencies(
	t *testing.T,
	repositoryRoot string,
	ownerRoot string,
	packagePath string,
	visited map[string]bool,
) {
	t.Helper()
	if visited[packagePath] {
		return
	}
	visited[packagePath] = true

	for _, imported := range productionImports(t, filepath.Join(repositoryRoot, packagePath)) {
		localPath, ok := strings.CutPrefix(imported, modulePath+"/")
		if !ok {
			continue
		}
		for _, forbidden := range forbiddenDependencyRoots {
			if localPath == forbidden || strings.HasPrefix(localPath, forbidden+"/") {
				t.Fatalf("%s depends on forbidden package %s through %s", ownerRoot, localPath, packagePath)
			}
		}
		for _, otherDomain := range migratedDomainRoots {
			if otherDomain == ownerRoot {
				continue
			}
			if localPath == otherDomain || strings.HasPrefix(localPath, otherDomain+"/") {
				t.Fatalf("%s depends on domain %s through %s", ownerRoot, otherDomain, packagePath)
			}
		}
		visitLocalDependencies(t, repositoryRoot, ownerRoot, localPath, visited)
	}
}

func productionImports(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read package %s: %v", directory, err)
	}
	imports := make([]string, 0)
	files := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		parsed, err := parser.ParseFile(files, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse imports from %s: %v", path, err)
		}
		for _, imported := range parsed.Imports {
			value, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatalf("parse import in %s: %v", path, err)
			}
			imports = append(imports, value)
		}
	}
	return imports
}

func findRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}
