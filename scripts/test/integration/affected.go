package main

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"path"
	"slices"
	"strings"
)

// affectedIntegrationBands narrows the catalog to the packages whose test
// binaries import a package changed since base. A change the import graph cannot
// attribute (module files, the harness itself, non-Go assets outside a package)
// selects the whole catalog instead of guessing.
func affectedIntegrationBands(ctx context.Context, repoRoot, goWork, base string) ([]integrationBand, error) {
	changedFiles, err := commandOutputLines(ctx, repoRoot, goWork, "git", "diff", "--name-only", "--no-renames", base, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("list files changed since %s: %w", base, err)
	}
	packageDirs, err := commandOutputLines(ctx, repoRoot, goWork, "go", "list", "-tags=integration", "-f", "{{.ImportPath}}|{{.Dir}}", "./...")
	if err != nil {
		return nil, fmt.Errorf("list module packages: %w", err)
	}
	changed, full, err := changedImportPaths(repoRoot, changedFiles, packageDirs)
	if err != nil {
		return nil, err
	}
	if full {
		return append([]integrationBand(nil), integrationCatalog...), nil
	}
	if len(changed) == 0 {
		return nil, nil
	}
	arguments := append([]string{"list", "-tags=integration", "-test", "-f", "{{.ImportPath}}|{{join .Deps \",\"}}"}, catalogPackagePaths()...)
	dependencies, err := commandOutputLines(ctx, repoRoot, goWork, "go", arguments...)
	if err != nil {
		return nil, fmt.Errorf("list integration test dependencies: %w", err)
	}
	selected, err := packagesImportingAny(repoRoot, packageDirs, dependencies, changed)
	if err != nil {
		return nil, err
	}
	for packagePath, binaries := range integrationProcessDependencies {
		if slices.Contains(selected, packagePath) {
			continue
		}
		arguments := append([]string{"list", "-tags=integration", "-deps", "-f", "{{.ImportPath}}"}, binaries...)
		processDeps, err := commandOutputLines(ctx, repoRoot, goWork, "go", arguments...)
		if err != nil {
			return nil, fmt.Errorf("list %s process dependencies: %w", packagePath, err)
		}
		if slices.ContainsFunc(processDeps, func(dep string) bool { return changed[dep] }) {
			selected = append(selected, packagePath)
		}
	}
	slices.Sort(selected)
	return bandsForPackages(selected), nil
}

// integrationIgnoredPath reports files that cannot change integration behavior.
func integrationIgnoredPath(file string) bool {
	switch {
	case file == ".github/workflows/ci.yml":
		// It pins the schema, images and sibling revisions the suite runs against.
		return false
	case strings.HasPrefix(file, "docs/"), strings.HasPrefix(file, ".github/"):
		return true
	case strings.HasSuffix(file, ".md"):
		return true
	}
	switch file {
	case ".dockerignore", ".gitignore", ".golangci.yml", ".release-please-manifest.json",
		"Dockerfile", "LICENSE", "release-please-config.json":
		return true
	}
	return false
}

// changedImportPaths maps changed files to the import paths of their packages.
// full is true when a change cannot be attributed to a single package.
func changedImportPaths(repoRoot string, changedFiles, packageDirs []string) (changed map[string]bool, full bool, err error) {
	importPathByDir, err := importPathsByRelativeDir(repoRoot, packageDirs)
	if err != nil {
		return nil, false, err
	}
	harnessDir := strings.TrimPrefix(integrationHarnessPackage, "./")
	changed = make(map[string]bool)
	for _, file := range changedFiles {
		if integrationIgnoredPath(file) {
			continue
		}
		dir, importPath, ok := owningPackage(importPathByDir, path.Dir(file))
		if !ok || dir == harnessDir {
			return nil, true, nil
		}
		changed[importPath] = true
	}
	return changed, false, nil
}

// owningPackage finds the nearest package directory containing dir. Embedded
// assets live below their package, so a subdirectory change belongs to it.
func owningPackage(importPathByDir map[string]string, dir string) (string, string, bool) {
	for ; dir != "." && dir != "/"; dir = path.Dir(dir) {
		if importPath, ok := importPathByDir[dir]; ok {
			return dir, importPath, true
		}
	}
	return "", "", false
}

// packagesImportingAny returns the cataloged packages whose test binaries
// (internal and external tests) depend on, or are, one of the changed packages.
func packagesImportingAny(repoRoot string, packageDirs, dependencies []string, changed map[string]bool) ([]string, error) {
	importPathByDir, err := importPathsByRelativeDir(repoRoot, packageDirs)
	if err != nil {
		return nil, err
	}
	catalogByImportPath := make(map[string]string)
	for _, packagePath := range catalogPackagePaths() {
		importPath, ok := importPathByDir[strings.TrimPrefix(packagePath, "./")]
		if !ok {
			return nil, fmt.Errorf("cataloged integration package %s is not in the module", packagePath)
		}
		catalogByImportPath[importPath] = packagePath
	}
	selected := make(map[string]bool)
	for _, line := range dependencies {
		importPath, deps, ok := strings.Cut(line, "|")
		if !ok {
			return nil, fmt.Errorf("parse go list dependency output %q", line)
		}
		packagePath, ok := catalogByImportPath[testVariantOwner(importPath)]
		if !ok || selected[packagePath] {
			continue
		}
		candidates := append([]string{importPath}, strings.Split(deps, ",")...)
		if slices.ContainsFunc(candidates, func(dep string) bool { return changed[testVariantOwner(dep)] }) {
			selected[packagePath] = true
		}
	}
	packages := make([]string, 0, len(selected))
	for packagePath := range selected {
		packages = append(packages, packagePath)
	}
	slices.Sort(packages)
	return packages, nil
}

// testVariantOwner maps go list -test variants ("p [p.test]", "p_test", "p.test")
// back to the package they test.
func testVariantOwner(importPath string) string {
	importPath, _, _ = strings.Cut(importPath, " ")
	importPath = strings.TrimSuffix(importPath, ".test")
	return strings.TrimSuffix(importPath, "_test")
}

func importPathsByRelativeDir(repoRoot string, packageDirs []string) (map[string]string, error) {
	byDir := make(map[string]string, len(packageDirs))
	root := strings.TrimSuffix(repoRoot, "/") + "/"
	for _, line := range packageDirs {
		importPath, dir, ok := strings.Cut(line, "|")
		if !ok {
			return nil, fmt.Errorf("parse go list package output %q", line)
		}
		relative := "."
		if dir != strings.TrimSuffix(root, "/") {
			if !strings.HasPrefix(dir, root) {
				return nil, fmt.Errorf("package %s is outside the repository root", importPath)
			}
			relative = strings.TrimPrefix(dir, root)
		}
		byDir[relative] = importPath
	}
	return byDir, nil
}

// bandsForPackages keeps catalog order and band semantics, dropping bands with
// no selected package.
func bandsForPackages(packages []string) []integrationBand {
	var bands []integrationBand
	for _, band := range integrationCatalog {
		var selected []string
		for _, packagePath := range band.Packages {
			if slices.Contains(packages, packagePath) {
				selected = append(selected, packagePath)
			}
		}
		if len(selected) == 0 {
			continue
		}
		band.Packages = selected
		bands = append(bands, band)
	}
	return bands
}

func commandOutputLines(ctx context.Context, repoRoot, goWork, name string, arguments ...string) ([]string, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Dir = repoRoot
	command.Env = integrationCommandEnvironment(goWork)
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			lines = append(lines, line)
		}
	}
	return lines, scanner.Err()
}
