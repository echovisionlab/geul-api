package main

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil"
)

func TestParseOptionsUsesMinIOEnvironmentDefaultAndExplicitOverride(t *testing.T) {
	t.Setenv(testutil.AppIntegrationMinIOImageEnv, "environment-minio:local")

	defaults, err := parseOptions(nil)
	if err != nil {
		t.Fatalf("parse default options: %v", err)
	}
	if defaults.MinIOImage != "environment-minio:local" {
		t.Fatalf("default MinIO image = %q, want environment-minio:local", defaults.MinIOImage)
	}

	override, err := parseOptions([]string{"--minio-image", " geul-test-minio:local "})
	if err != nil {
		t.Fatalf("parse explicit MinIO image: %v", err)
	}
	if override.MinIOImage != "geul-test-minio:local" {
		t.Fatalf("explicit MinIO image = %q, want geul-test-minio:local", override.MinIOImage)
	}
}

func TestParseOptionsAcceptsFullAndNamedBands(t *testing.T) {
	t.Parallel()
	defaults, err := parseOptions(nil)
	if err != nil {
		t.Fatalf("parse default options: %v", err)
	}

	if defaults.GoWork != integrationGoWorkOff {
		t.Fatalf("default go.work = %q, want %q", defaults.GoWork, integrationGoWorkOff)
	}

	for _, args := range [][]string{
		{},
		{"--band", "db", "--jobs", "4"},
		{"--band", "ory"},
		{"--band", "serial"},
		{"--package", "./internal/translation"},
		{"--package", "./internal/filemedia", "--run", "^TestFileDownloadAudiencePolicyIntegration$"},
		{"--package", "./internal/member"},
		{"--package", "./internal/testutil"},
		{"--go-work", "/workspace/go.work"},
		{"--minio-image", "geul-test-minio:local"},
	} {
		if _, err := parseOptions(args); err != nil {
			t.Fatalf("parseOptions(%q): %v", args, err)
		}
	}

	for _, args := range [][]string{
		{"--band", "db", "--jobs", "0"},
		{"--band", "db", "--jobs", "5"},
		{"--band", "unknown"},
		{"--band", "db", "--package", "./internal/translation"},
		{"--run", "^TestFileDownloadAudiencePolicyIntegration$"},
		{"--band", "ory", "--run", "^TestFileDownloadAudiencePolicyIntegration$"},
		{"--package", "./internal/not-cataloged"},
		{"--package", "./internal/filemedia", "--run", "["},
		{"--package", "./internal/filemedia", "--run", ""},
		{"--cdn-image", ""},
		{"--minio-image", ""},
		{"--go-work", "../go.work"},
	} {
		if _, err := parseOptions(args); err == nil {
			t.Fatalf("parseOptions(%q) succeeded, want error", args)
		}
	}
}

func TestBandGoTestArgumentsBoundPackageConcurrency(t *testing.T) {
	t.Parallel()

	db, _ := bandByName("db")
	args := bandGoTestArguments(db, 3)
	wantPrefix := []string{
		"test", "-p", "3", "-parallel", "1", "-timeout", "30m",
		"-count=1", "-tags=integration",
	}
	if len(args) < len(wantPrefix) {
		t.Fatalf("arguments too short: %q", args)
	}
	for index := range wantPrefix {
		if args[index] != wantPrefix[index] {
			t.Fatalf("argument %d = %q, want %q", index, args[index], wantPrefix[index])
		}
	}
	serial, _ := bandByName("serial")
	serialArgs := bandGoTestArguments(serial, 3)
	if serialArgs[2] != "1" {
		t.Fatalf("serial package concurrency = %q, want 1", serialArgs[2])
	}
}

func TestPackageGoTestArgumentsPreserveDefaultAndApplyOptionalRun(t *testing.T) {
	t.Parallel()

	packagePath := "./internal/filemedia"
	withoutFilter := packageGoTestArguments(packagePath, "")
	wantWithoutFilter := []string{
		"test", "-p", "1", "-parallel", "1", "-timeout", "30m",
		"-count=1", "-tags=integration", packagePath,
	}
	if len(withoutFilter) != len(wantWithoutFilter) {
		t.Fatalf("unfiltered arguments = %q, want %q", withoutFilter, wantWithoutFilter)
	}
	for index := range wantWithoutFilter {
		if withoutFilter[index] != wantWithoutFilter[index] {
			t.Fatalf("unfiltered argument %d = %q, want %q", index, withoutFilter[index], wantWithoutFilter[index])
		}
	}

	filter := "^(TestFileDownloadAudiencePolicyIntegration|TestGetFileDownloadPolicyLocksPostAndProgramEventInRepeatableReadIntegration)$"
	withFilter := packageGoTestArguments(packagePath, filter)
	wantWithFilter := []string{
		"test", "-p", "1", "-parallel", "1", "-timeout", "30m",
		"-count=1", "-tags=integration", "-run", filter, packagePath,
	}
	if len(withFilter) != len(wantWithFilter) {
		t.Fatalf("filtered arguments = %q, want %q", withFilter, wantWithFilter)
	}
	for index := range wantWithFilter {
		if withFilter[index] != wantWithFilter[index] {
			t.Fatalf("filtered argument %d = %q, want %q", index, withFilter[index], wantWithFilter[index])
		}
	}
}

func TestSelectedIntegrationBandsPreserveCatalogOrder(t *testing.T) {
	t.Parallel()

	bands, err := selectedIntegrationBands("", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(bands) != 3 || bands[0].Name != "db" || bands[1].Name != "ory" || bands[2].Name != "serial" {
		t.Fatalf("full bands = %#v", bands)
	}
}

func TestSelectedIntegrationPackagePreservesOwningResourcesAndRunsAlone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		packagePath string
		bandName    string
		resources   integrationResourceMode
	}{
		{packagePath: "./internal/translation", bandName: "db", resources: integrationPostgresOnly},
		{packagePath: "./internal/member", bandName: "ory", resources: integrationSharedBackend},
		{packagePath: "./internal/testutil", bandName: "serial", resources: integrationRuntimeExclusive},
	}
	for _, test := range tests {
		test := test
		t.Run(test.bandName, func(t *testing.T) {
			t.Parallel()
			bands, err := selectedIntegrationBands("", test.packagePath)
			if err != nil {
				t.Fatal(err)
			}
			if len(bands) != 1 {
				t.Fatalf("selected bands = %#v, want one", bands)
			}
			band := bands[0]
			if band.Name != test.bandName || band.Resources != test.resources {
				t.Fatalf("selected band = %#v, want name=%q resources=%q", band, test.bandName, test.resources)
			}
			if band.ParallelPackages {
				t.Fatalf("selected package retained parallel package mode: %#v", band)
			}
			if len(band.Packages) != 1 || band.Packages[0] != test.packagePath {
				t.Fatalf("selected packages = %q, want only %q", band.Packages, test.packagePath)
			}
		})
	}
}

func TestSelectedIntegrationPackageRejectsNonCatalogedPath(t *testing.T) {
	t.Parallel()

	if _, err := selectedIntegrationBands("", "./internal/not-cataloged"); err == nil {
		t.Fatal("selectedIntegrationBands accepted non-cataloged package")
	}
}
