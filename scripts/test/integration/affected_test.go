package main

import (
	"slices"
	"testing"
)

const affectedTestRoot = "/repo"

var affectedTestPackageDirs = []string{
	"example.com/api/cmd/server|/repo/cmd/server",
	"example.com/api/internal/email|/repo/internal/email",
	"example.com/api/internal/member|/repo/internal/member",
	"example.com/api/internal/post|/repo/internal/post",
	"example.com/api/internal/textutil|/repo/internal/textutil",
	"example.com/api/scripts/test/integration|/repo/scripts/test/integration",
}

func TestChangedImportPathsAttributesPackageFilesAndIgnoresDocs(t *testing.T) {
	t.Parallel()

	changed, full, err := changedImportPaths(affectedTestRoot, []string{
		"CHANGELOG.md",
		"docs/logging.md",
		".github/workflows/release.yml",
		"internal/email/templates/welcome.html",
		"internal/member/member.go",
	}, affectedTestPackageDirs)
	if err != nil {
		t.Fatalf("changedImportPaths: %v", err)
	}
	if full {
		t.Fatal("package-local changes selected the full catalog")
	}
	want := map[string]bool{"example.com/api/internal/email": true, "example.com/api/internal/member": true}
	if len(changed) != len(want) {
		t.Fatalf("changed = %v, want %v", changed, want)
	}
	for importPath := range want {
		if !changed[importPath] {
			t.Fatalf("changed = %v, missing %s", changed, importPath)
		}
	}
}

func TestChangedImportPathsSelectsFullCatalogForUnattributedChanges(t *testing.T) {
	t.Parallel()

	for _, file := range []string{
		"go.mod",
		".github/workflows/ci.yml",
		"media/og/src/render.ts",
		"internal/removed/package.go",
		"scripts/test/integration/testdata/fixture.json",
		"scripts/test/integration/main.go",
	} {
		_, full, err := changedImportPaths(affectedTestRoot, []string{"internal/member/member.go", file}, affectedTestPackageDirs)
		if err != nil {
			t.Fatalf("changedImportPaths(%s): %v", file, err)
		}
		if !full {
			t.Fatalf("change to %s did not select the full catalog", file)
		}
	}
}

func TestPackagesImportingAnyFollowsTestBinaryDependencies(t *testing.T) {
	t.Parallel()

	packageDirs := append([]string(nil), affectedTestPackageDirs...)
	for _, packagePath := range catalogPackagePaths() {
		dir := packagePath[len("./"):]
		packageDirs = append(packageDirs, "example.com/api/"+dir+"|/repo/"+dir)
	}
	dependencies := []string{
		"example.com/api/internal/post|example.com/api/internal/textutil",
		"example.com/api/internal/post [example.com/api/internal/post.test]|example.com/api/internal/textutil",
		"example.com/api/internal/post_test [example.com/api/internal/post.test]|example.com/api/internal/email [example.com/api/internal/post.test]",
		"example.com/api/internal/post.test|example.com/api/internal/post [example.com/api/internal/post.test]",
		"example.com/api/internal/member|example.com/api/internal/textutil",
		"example.com/api/cmd/server|example.com/api/internal/member",
	}

	selected, err := packagesImportingAny(affectedTestRoot, packageDirs, dependencies, map[string]bool{
		"example.com/api/internal/email": true,
	})
	if err != nil {
		t.Fatalf("packagesImportingAny: %v", err)
	}
	if want := []string{"./internal/post"}; !slices.Equal(selected, want) {
		t.Fatalf("selected = %v, want %v (external test imports count)", selected, want)
	}

	selected, err = packagesImportingAny(affectedTestRoot, packageDirs, dependencies, map[string]bool{
		"example.com/api/internal/member": true,
	})
	if err != nil {
		t.Fatalf("packagesImportingAny: %v", err)
	}
	if want := []string{"./cmd/server", "./internal/member"}; !slices.Equal(selected, want) {
		t.Fatalf("selected = %v, want %v (a changed package selects itself and its importers)", selected, want)
	}
}

func TestBandsForPackagesKeepsCatalogOrderAndSemantics(t *testing.T) {
	t.Parallel()

	bands := bandsForPackages([]string{"./internal/testutil/pgtest", "./internal/post", "./internal/member", "./internal/email/postgres"})
	if len(bands) != 3 {
		t.Fatalf("bands = %#v, want db, ory and serial", bands)
	}
	for index, want := range []struct {
		name     string
		packages []string
	}{
		{name: "db", packages: []string{"./internal/email/postgres"}},
		{name: "ory", packages: []string{"./internal/member", "./internal/post"}},
		{name: "serial", packages: []string{"./internal/testutil/pgtest"}},
	} {
		catalogBand, _ := bandByName(want.name)
		band := bands[index]
		if band.Name != want.name || !slices.Equal(band.Packages, want.packages) ||
			band.Resources != catalogBand.Resources || band.ParallelPackages != catalogBand.ParallelPackages {
			t.Fatalf("band %d = %#v, want %s with %v and catalog semantics", index, band, want.name, want.packages)
		}
	}
	if bands := bandsForPackages(nil); len(bands) != 0 {
		t.Fatalf("empty selection = %#v, want no bands", bands)
	}
}

func TestParseOptionsChangedFromExcludesExplicitSelection(t *testing.T) {
	t.Parallel()

	options, err := parseOptions([]string{"--changed-from", " origin/main ", "--list"})
	if err != nil {
		t.Fatalf("parse changed-from: %v", err)
	}
	if options.ChangedFrom != "origin/main" {
		t.Fatalf("changed-from = %q, want origin/main", options.ChangedFrom)
	}
	for _, args := range [][]string{
		{"--changed-from", "origin/main", "--band", "db"},
		{"--changed-from", "origin/main", "--package", "./internal/member"},
	} {
		if _, err := parseOptions(args); err == nil {
			t.Fatalf("parseOptions(%q) succeeded, want error", args)
		}
	}
}
