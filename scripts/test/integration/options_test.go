package main

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func TestParseOptionsUsesMinIOEnvironmentDefaultAndExplicitOverride(t *testing.T) {
	t.Setenv(pgtest.AppIntegrationMinIOImageEnv, "environment-minio:local")

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
		{"--package", "./internal/filemedia", "--run", "^TestFileDownloadAccessPolicyIntegration$"},
		{"--package", "./internal/member"},
		{"--package", "./internal/testutil/pgtest"},
		{"--go-work", "/workspace/go.work"},
		{"--minio-image", "geul-test-minio:local"},
	} {
		if _, err := parseOptions(args); err != nil {
			t.Fatalf("parseOptions(%q): %v", args, err)
		}
	}

	for _, args := range [][]string{
		{"--band", "db", "--jobs", "0"},
		{"--band", "db", "--jobs", "17"},
		{"--band", "unknown"},
		{"--band", "db", "--package", "./internal/translation"},
		{"--run", "^TestFileDownloadAccessPolicyIntegration$"},
		{"--band", "ory", "--run", "^TestFileDownloadAccessPolicyIntegration$"},
		{"--package", "./internal/not-cataloged"},
		{"--package", "./internal/filemedia", "--run", "["},
		{"--package", "./internal/filemedia", "--run", ""},
		{"--cdn-image", ""},
		{"--minio-image", ""},
		{"--go-work", "../go.work"},
		{"--list", "--list-images"},
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
		"test", "-p", "3", "-parallel", "3", "-timeout", "30m",
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
	if serialArgs[4] != "1" {
		t.Fatalf("shared backend test concurrency = %q, want 1", serialArgs[4])
	}
}

func TestPackageGoTestArgumentsPreserveDefaultAndApplyOptionalRun(t *testing.T) {
	t.Parallel()

	packagePath := "./internal/filemedia"
	withoutFilter := packageGoTestArguments(packagePath, "", integrationRuntimeExclusive, 3)
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

	filter := "^(TestFileDownloadAccessPolicyIntegration|TestGetFileDownloadPolicyLocksPostAndProgramEventInRepeatableReadIntegration)$"
	withFilter := packageGoTestArguments(packagePath, filter, integrationRuntimeExclusive, 3)
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

func TestSelectedPackageTestConcurrencyMatchesOwnedResources(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		packagePath  string
		wantParallel string
	}{
		{packagePath: "./internal/mq", wantParallel: "2"},
		{packagePath: "./internal/telemetry", wantParallel: "2"},
		{packagePath: "./internal/transcode/application", wantParallel: "2"},
		{packagePath: "./internal/member", wantParallel: "2"},
		{packagePath: "./internal/filemedia/runtimetest", wantParallel: "1"},
		{packagePath: "./internal/testutil/pgtest", wantParallel: "1"},
	} {
		t.Run(testCase.packagePath, func(t *testing.T) {
			t.Parallel()
			bands, err := selectedIntegrationBands("", testCase.packagePath)
			if err != nil {
				t.Fatal(err)
			}
			arguments := packageGoTestArguments(testCase.packagePath, "^TestSelected$", bands[0].Resources, 2)
			if arguments[2] != "1" || arguments[4] != testCase.wantParallel {
				t.Fatalf("package/test concurrency = %q/%q, want 1/%s", arguments[2], arguments[4], testCase.wantParallel)
			}
			if arguments[len(arguments)-3] != "-run" || arguments[len(arguments)-2] != "^TestSelected$" || arguments[len(arguments)-1] != testCase.packagePath {
				t.Fatalf("selected package/filter changed: %q", arguments)
			}
		})
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
		{packagePath: "./internal/testutil/pgtest", bandName: "serial", resources: integrationRuntimeExclusive},
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
