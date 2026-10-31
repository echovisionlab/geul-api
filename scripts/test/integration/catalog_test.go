package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestIntegrationCatalogHasExactResourceSemantics(t *testing.T) {
	t.Parallel()

	wantBands := []struct {
		name             string
		resources        integrationResourceMode
		parallelPackages bool
	}{
		{name: "db", resources: integrationPostgresOnly, parallelPackages: true},
		{name: "ory", resources: integrationSharedBackend, parallelPackages: true},
		{name: "serial", resources: integrationRuntimeExclusive},
	}
	if len(integrationCatalog) != len(wantBands) {
		t.Fatalf("resource band count = %d, want %d", len(integrationCatalog), len(wantBands))
	}
	seen := make(map[string]string)
	for index, band := range integrationCatalog {
		want := wantBands[index]
		if band.Name != want.name || band.Resources != want.resources || band.ParallelPackages != want.parallelPackages {
			t.Fatalf("resource band %d = %#v, want name=%q resources=%q parallel=%t", index, band, want.name, want.resources, want.parallelPackages)
		}
		if len(band.Packages) == 0 {
			t.Fatalf("resource band %s has no packages", band.Name)
		}
		if !slices.IsSorted(band.Packages) {
			t.Fatalf("band %s packages are not sorted", band.Name)
		}
		for _, packagePath := range band.Packages {
			if packagePath == integrationHarnessPackage {
				t.Fatalf("catalog must not include its own harness package %s", packagePath)
			}
			if previous, ok := seen[packagePath]; ok {
				t.Fatalf("package %s assigned to both %s and %s", packagePath, previous, band.Name)
			}
			seen[packagePath] = band.Name
		}
	}
}

func TestIntegrationResourceModesDriveBackendAndConcurrency(t *testing.T) {
	t.Parallel()

	db, ok := bandByName("db")
	if !ok || !db.ParallelPackages || integrationBandsNeedBackend([]integrationBand{db}) {
		t.Fatalf("PostgreSQL-only band semantics = %#v", db)
	}
	ory, ok := bandByName("ory")
	if !ok || !ory.ParallelPackages || !integrationBandsNeedBackend([]integrationBand{ory}) {
		t.Fatalf("isolated backend band semantics = %#v", ory)
	}
	serial, ok := bandByName("serial")
	if !ok || serial.ParallelPackages || !integrationBandsNeedBackend([]integrationBand{serial}) {
		t.Fatalf("runtime-exclusive band semantics = %#v", serial)
	}
}

func TestRepositoryIntegrationPackagesExactlyMatchCatalog(t *testing.T) {
	t.Parallel()

	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	discoveredPackages, err := discoverIntegrationPackages(t.Context(), repoRoot, integrationGoWorkOff)
	if err != nil {
		t.Fatalf("discover integration packages: %v", err)
	}
	if err := verifyIntegrationCatalog(discoveredPackages); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyIntegrationCatalogRejectsDrift(t *testing.T) {
	t.Parallel()

	discoveredPackages := catalogPackagePaths()
	if err := verifyIntegrationCatalog(discoveredPackages); err != nil {
		t.Fatalf("verify exact catalog: %v", err)
	}
	if err := verifyIntegrationCatalog(discoveredPackages[1:]); err == nil {
		t.Fatal("expected stale catalog entry to fail")
	}
	if err := verifyIntegrationCatalog(append(discoveredPackages, "./internal/newintegration")); err == nil {
		t.Fatal("expected unclassified integration package to fail")
	}
}

// Tests that start application processes depend on those binaries too; the
// affected-package selection only sees them through integrationProcessDependencies.
func TestRuntimeStackPackagesDeclareProcessDependencies(t *testing.T) {
	t.Parallel()
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	runtimeHelper := regexp.MustCompile(`testutil\.Setup(Shared[A-Za-z]*)?(Direct[A-Za-z]*)?RuntimeStack[A-Za-z]*\(`)
	var missing []string
	err = filepath.WalkDir(filepath.Join(repoRoot, "internal"), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !runtimeHelper.Match(contents) {
			return nil
		}
		relative, err := filepath.Rel(repoRoot, filepath.Dir(path))
		if err != nil {
			return err
		}
		packagePath := "./" + filepath.ToSlash(relative)
		if _, ok := integrationProcessDependencies[packagePath]; !ok && !slices.Contains(missing, packagePath) {
			missing = append(missing, packagePath)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) > 0 {
		t.Fatalf("packages using runtime stacks must list their process dependencies: %v", missing)
	}
}
