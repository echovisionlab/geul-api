package main

import (
	"bufio"
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

var integrationCatalog = []integrationBand{
	{
		// PostgreSQL or in-process SQLite only. Each package receives an
		// independent logical database and can run with bounded package parallelism.
		Name:             "db",
		Resources:        integrationPostgresOnly,
		ParallelPackages: true,
		Packages: []string{
			"./cmd/server",
			"./internal/adapters/geoip",
			"./internal/adapters/legal",
			"./internal/adapters/ogprojection",
			"./internal/adapters/sitesettings",
			"./internal/contentblock",
			"./internal/email",
			"./internal/geoip",
			"./internal/legal/public",
			"./internal/localization",
			"./internal/mq",
			"./internal/postgreslock",
			"./internal/publiccontent",
			"./internal/scheduler",
			"./internal/telemetry",
			"./internal/transcode",
			"./internal/transcode/application",
			"./internal/translation",
		},
	},
	{
		// Uses the orchestrator-owned Kratos, SpiceDB, Oathkeeper, MinIO, or
		// backend lease. Packages run one at a time with a backend reset between them.
		Name:      "ory",
		Resources: integrationSharedBackend,
		Packages: []string{
			"./internal/account",
			"./internal/adapters/collaboration",
			"./internal/admin",
			"./internal/ai",
			"./internal/audience",
			"./internal/authentication",
			"./internal/authorizationtarget",
			"./internal/campaign",
			"./internal/emailauthoring",
			"./internal/emaildelivery",
			"./internal/filemedia/public",
			"./internal/form",
			"./internal/form/public",
			"./internal/identitystate",
			"./internal/legal",
			"./internal/maptheme",
			"./internal/maptheme/public",
			"./internal/mediaasset",
			"./internal/member",
			"./internal/member/public",
			"./internal/menu",
			"./internal/og",
			"./internal/page",
			"./internal/page/public",
			"./internal/post",
			"./internal/post/public",
			"./internal/programevent",
			"./internal/programevent/public",
			"./internal/referencecatalog",
			"./internal/referencecatalog/public",
			"./internal/series",
			"./internal/series/public",
			"./internal/sharelink/public",
			"./internal/sitemap/public",
			"./internal/sitesettings/integration",
			"./internal/sitesettings/public/integration",
			"./internal/translation/application",
			"./internal/work/integration",
			"./internal/work/public/integration",
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
			"./internal/filemedia",
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

func discoverIntegrationPackages(repoRoot, goWork string) ([]string, error) {
	format := `{{.Dir}}|{{join .TestGoFiles ","}}|{{join .XTestGoFiles ","}}`
	command := exec.Command("go", "list", "-tags=integration", "-f", format, "./...")
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

func verifyIntegrationCatalog(actual []string) error {
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
	for _, packagePath := range actual {
		if !slices.Contains(want, packagePath) {
			missing = append(missing, packagePath)
		}
	}
	var stale []string
	for _, packagePath := range want {
		if !slices.Contains(actual, packagePath) {
			stale = append(stale, packagePath)
		}
	}
	if len(missing) == 0 && len(stale) == 0 {
		return nil
	}
	return fmt.Errorf("integration catalog drift: unclassified=%v stale=%v", missing, stale)
}
