package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// appIntegrationModuleRoot locates the API checkout from the process working
// directory, including Go's package test directories. Compiler source paths are
// deliberately unused: -trimpath rewrites them to module import paths.
func appIntegrationModuleRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		contents, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			for _, line := range strings.Split(string(contents), "\n") {
				fields := strings.Fields(line)
				if len(fields) >= 2 && fields[0] == "module" && fields[1] == "github.com/echovisionlab/geul-api" {
					return dir, nil
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("cannot locate geul-api go.mod from %s; run integration tests inside the API checkout", start)
		}
		dir = parent
	}
}

// AppIntegrationSiblingPath finds a sibling checkout, including siblings above
// a nested worktree. Missing checkouts retain their expected path for diagnostics.
func AppIntegrationSiblingPath(moduleRoot, name string) string {
	fallback := filepath.Join(filepath.Dir(moduleRoot), name)
	for workspace := filepath.Dir(moduleRoot); ; workspace = filepath.Dir(workspace) {
		candidate := filepath.Join(workspace, name)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		if filepath.Dir(workspace) == workspace {
			return fallback
		}
	}
}

// AppIntegrationSpiceDBSchemaPath uses Identity's canonical generated artifact.
func AppIntegrationSpiceDBSchemaPath(moduleRoot string) string {
	return filepath.Join(AppIntegrationSiblingPath(moduleRoot, "geul-identity"), "config", "spicedb", "schema.generated.zed")
}

func appIntegrationRepoPath(relativePath string) string {
	cwd, err := os.Getwd()
	if err != nil {
		panic(fmt.Errorf("resolve integration working directory: %w", err))
	}
	root, err := appIntegrationModuleRoot(cwd)
	if err != nil {
		panic(err)
	}
	if suffix, ok := legacyMonorepoRelativePath(relativePath); ok {
		if suffix == "" {
			return filepath.Clean(root)
		}
		if splitPath, ok := splitRepoPathForLegacySuffix(root, suffix); ok {
			return splitPath
		}
		return filepath.Clean(filepath.Join(root, filepath.FromSlash(suffix)))
	}
	return filepath.Clean(filepath.Join(root, "internal", "testutil", relativePath))
}

func splitRepoPathForLegacySuffix(moduleRoot, suffix string) (string, bool) {
	mappings := []struct {
		legacy string
		split  string
	}{
		{legacy: "apps/collab", split: "geul-editor-collab"},
		{legacy: "apps/web", split: "geul-web"},
		{legacy: "infra/kratos", split: "geul-identity/config/kratos"},
		{legacy: "infra/oathkeeper", split: "geul-identity/config/oathkeeper"},
		{legacy: "infra/spicedb", split: "geul-identity/config/spicedb"},
	}
	for _, mapping := range mappings {
		if suffix != mapping.legacy && !strings.HasPrefix(suffix, mapping.legacy+"/") {
			continue
		}
		remainder := strings.TrimPrefix(strings.TrimPrefix(suffix, mapping.legacy), "/")
		repo, subdir, _ := strings.Cut(mapping.split, "/")
		return filepath.Join(AppIntegrationSiblingPath(moduleRoot, repo), subdir, filepath.FromSlash(remainder)), true
	}
	return "", false
}

func legacyMonorepoRelativePath(relativePath string) (string, bool) {
	const prefix = "../../../../"
	slashPath := filepath.ToSlash(relativePath)
	if slashPath == "../../../.." {
		return "", true
	}
	if strings.HasPrefix(slashPath, prefix) {
		return strings.TrimPrefix(slashPath, prefix), true
	}
	return "", false
}
