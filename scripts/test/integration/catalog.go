package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

type integrationBand struct {
	Name             string
	Resources        integrationResourceMode
	ParallelPackages bool
	Packages         []string
}

type integrationResourceMode string

const (
	integrationPostgresOnly     integrationResourceMode = "postgres-only"
	integrationSharedBackend    integrationResourceMode = "shared-backend"
	integrationRuntimeExclusive integrationResourceMode = "runtime-exclusive"
)

const integrationHarnessPackage = "./scripts/test/integration"

// integrationProcessDependencies lists packages whose tests start other module
// binaries; a change to such a binary's dependencies selects them as well.
var integrationProcessDependencies = map[string][]string{
	"./cmd/server/runtimetest":         {"./cmd/server"},
	"./internal/filemedia":             {"./cmd/server"},
	"./internal/filemedia/runtimetest": {"./cmd/server"},
}

var integrationCatalog = []integrationBand{
	{
		// PostgreSQL or in-process SQLite only. Each package receives an
		// independent logical database and can run with bounded package parallelism.
		// Tests opt into t.Parallel only when they own an independent fixture;
		// legacy package-scoped database fixtures remain sequential.
		Name:             "db",
		Resources:        integrationPostgresOnly,
		ParallelPackages: true,
		Packages: []string{
			"./cmd/server",
			"./internal/account/postgres",
			"./internal/adapters/geoip",
			"./internal/adapters/legal",
			"./internal/adapters/memberpat",
			"./internal/adapters/referencecatalog",
			"./internal/adapters/release",
			"./internal/adapters/sitemap",
			"./internal/adapters/sitesettings",
			"./internal/admin/postgres",
			"./internal/ai/postgres",
			"./internal/artist/postgres",
			"./internal/audience/postgres",
			"./internal/authentication/postgres",
			"./internal/contentblock",
			"./internal/email/postgres",
			"./internal/emailauthoring/postgres",
			"./internal/emaildelivery/postgres",
			"./internal/form/postgres",
			"./internal/form/public/postgres",
			"./internal/geoip",
			"./internal/label/postgres",
			"./internal/legal/postgres",
			"./internal/legal/public/postgres",
			"./internal/localization",
			"./internal/maptheme/postgres",
			"./internal/member/postgres",
			"./internal/member/public/postgres",
			"./internal/menu/postgres",
			"./internal/mq",
			"./internal/og/postgres",
			"./internal/page/postgres",
			"./internal/page/public/postgres",
			"./internal/pageaccess/postgres",
			"./internal/post/postgres",
			"./internal/post/public/postgres",
			"./internal/postgreslock",
			"./internal/programevent/postgres",
			"./internal/programevent/public/postgres",
			"./internal/referencecatalog/postgres",
			"./internal/referencecatalog/public/postgres",
			"./internal/release/postgres",
			"./internal/scheduler",
			"./internal/series/postgres",
			"./internal/sharelink/postgres",
			"./internal/sitemap/public",
			"./internal/sitesettings/postgres",
			"./internal/telemetry",
			"./internal/transcode/application",
			"./internal/transcode/application/postgres",
			"./internal/transcode/postgres",
			"./internal/transcoding/mq",
			"./internal/translation",
			"./internal/translation/postgres",
			"./internal/work/postgres",
			"./internal/work/public/postgres",
			"./internal/worker/postgres",
		},
	},
	{
		// Uses the orchestrator-owned Kratos, SpiceDB, Oathkeeper or MinIO. Every
		// test clones its own database and SpiceDB datastore, so packages and
		// tests run concurrently on the shared services.
		Name:             "ory",
		Resources:        integrationSharedBackend,
		ParallelPackages: true,
		Packages: []string{
			"./internal/account",
			"./internal/adapters/collaboration",
			"./internal/adapters/og",
			"./internal/artist",
			"./internal/artist/public",
			"./internal/audience",
			"./internal/authentication",
			"./internal/authorizationtarget",
			"./internal/campaign",
			"./internal/emailauthoring",
			"./internal/filemedia",
			"./internal/filemedia/public",
			"./internal/form",
			"./internal/identitystate",
			"./internal/label",
			"./internal/label/public",
			"./internal/legal",
			"./internal/maptheme",
			"./internal/mediaasset",
			"./internal/member",
			"./internal/member/public",
			"./internal/menu",
			"./internal/mutation",
			"./internal/page",
			"./internal/post",
			"./internal/post/public",
			"./internal/programevent",
			"./internal/referencecatalog",
			"./internal/release",
			"./internal/series",
			"./internal/series/public",
			"./internal/sharelink",
			"./internal/work",
			"./internal/worker",
		},
	},
	{
		// Owns process-global runtime variants or validates the harness lifecycle
		// itself. These packages remain the narrowest isolated band.
		Name:      "serial",
		Resources: integrationRuntimeExclusive,
		Packages: []string{
			"./cmd/backend-integration-stack",
			"./cmd/server/runtimetest",
			"./internal/filemedia/runtimetest",
			"./internal/testutil",
		},
	},
}

func bandByName(name string) (integrationBand, bool) {
	for _, band := range integrationCatalog {
		if band.Name == name {
			return band, true
		}
	}
	return integrationBand{}, false
}

func bandByPackage(packagePath string) (integrationBand, bool) {
	for _, band := range integrationCatalog {
		if slices.Contains(band.Packages, packagePath) {
			return band, true
		}
	}
	return integrationBand{}, false
}

func catalogPackagePaths() []string {
	var packages []string
	for _, band := range integrationCatalog {
		packages = append(packages, band.Packages...)
	}
	slices.Sort(packages)
	return packages
}

func discoverIntegrationPackages(ctx context.Context, repoRoot, goWork string) ([]string, error) {
	format := `{{.Dir}}|{{join .TestGoFiles ","}}|{{join .XTestGoFiles ","}}`
	command := exec.CommandContext(ctx, "go", "list", "-tags=integration", "-f", format, "./...")
	command.Dir = repoRoot
	command.Env = integrationCommandEnvironment(goWork)
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("go list integration packages: %w", err)
	}

	var packages []string
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), "|", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("parse go list output %q", scanner.Text())
		}
		directory := parts[0]
		files := append(strings.Split(parts[1], ","), strings.Split(parts[2], ",")...)
		integration := false
		for _, file := range files {
			if file == "" {
				continue
			}
			contents, err := os.ReadFile(filepath.Join(directory, file))
			if err != nil {
				return nil, fmt.Errorf("read integration test candidate %s: %w", file, err)
			}
			header, _, _ := strings.Cut(string(contents), "\n\n")
			if strings.Contains(header, "//go:build integration") {
				integration = true
				break
			}
		}
		if integration {
			relative, err := filepath.Rel(repoRoot, directory)
			if err != nil {
				return nil, fmt.Errorf("resolve integration package path: %w", err)
			}
			packagePath := "./" + filepath.ToSlash(relative)
			if packagePath == integrationHarnessPackage {
				continue
			}
			packages = append(packages, packagePath)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan go list output: %w", err)
	}
	slices.Sort(packages)
	return packages, nil
}

func verifyIntegrationCatalog(discoveredPackages []string) error {
	assignments := make(map[string]string)
	for _, band := range integrationCatalog {
		for _, packagePath := range band.Packages {
			if previous, ok := assignments[packagePath]; ok {
				return fmt.Errorf("integration catalog duplicate assignment: package=%s bands=%s,%s", packagePath, previous, band.Name)
			}
			assignments[packagePath] = band.Name
		}
	}
	want := catalogPackagePaths()
	var missing []string
	for _, packagePath := range discoveredPackages {
		if !slices.Contains(want, packagePath) {
			missing = append(missing, packagePath)
		}
	}
	var stale []string
	for _, packagePath := range want {
		if !slices.Contains(discoveredPackages, packagePath) {
			stale = append(stale, packagePath)
		}
	}
	if len(missing) == 0 && len(stale) == 0 {
		return nil
	}
	return fmt.Errorf("integration catalog drift: unclassified=%v stale=%v", missing, stale)
}
