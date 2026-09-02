package main

import (
	"path/filepath"
	"slices"
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
		{name: "ory", resources: integrationSharedBackend},
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
	for _, name := range []string{"ory", "serial"} {
		band, ok := bandByName(name)
		if !ok || band.ParallelPackages || !integrationBandsNeedBackend([]integrationBand{band}) {
			t.Fatalf("backend band %s semantics = %#v", name, band)
		}
	}
}

func TestRepositoryIntegrationPackagesExactlyMatchCatalog(t *testing.T) {
	t.Parallel()

	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	actual, err := discoverIntegrationPackages(repoRoot, integrationGoWorkOff)
	if err != nil {
		t.Fatalf("discover integration packages: %v", err)
	}
	if err := verifyIntegrationCatalog(actual); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyIntegrationCatalogRejectsDrift(t *testing.T) {
	t.Parallel()

	actual := catalogPackagePaths()
	if err := verifyIntegrationCatalog(actual); err != nil {
		t.Fatalf("verify exact catalog: %v", err)
	}
	if err := verifyIntegrationCatalog(actual[1:]); err == nil {
		t.Fatal("expected stale catalog entry to fail")
	}
	if err := verifyIntegrationCatalog(append(actual, "./internal/newintegration")); err == nil {
		t.Fatal("expected unclassified integration package to fail")
	}
}
