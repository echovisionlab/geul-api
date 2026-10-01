package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/echovisionlab/geul-api/internal/testutil"
)

type suiteOptions struct {
	Band          string
	Package       string
	Run           string
	Jobs          int
	List          bool
	GoWork        string
	SchemaRoot    string
	PostgresImage string
}

const integrationGoWorkOff = "off"

func parseOptions(args []string) (suiteOptions, error) {
	jobs := 2

	flags := flag.NewFlagSet("integration-suite", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := suiteOptions{Jobs: jobs, GoWork: integrationGoWorkOff}
	flags.StringVar(&options.Band, "band", "", "resource band to run")
	flags.StringVar(&options.Package, "package", "", "cataloged integration package to run")
	flags.StringVar(&options.Run, "run", "", "test name regexp (requires --package)")
	flags.IntVar(&options.Jobs, "jobs", jobs, "maximum concurrent packages")
	flags.BoolVar(&options.List, "list", false, "list the verified catalog")
	flags.StringVar(&options.GoWork, "go-work", integrationGoWorkOff, "Go workspace: off or an absolute go.work path")
	flags.StringVar(&options.SchemaRoot, "schema-root", "../geul-schema", "reviewed schema asset root")
	flags.StringVar(&options.PostgresImage, "postgres-image", testutil.AppIntegrationPostgresImage, "local PostgreSQL image")
	if err := flags.Parse(args); err != nil {
		return suiteOptions{}, err
	}
	if flags.NArg() != 0 {
		return suiteOptions{}, fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if options.Jobs < 1 || options.Jobs > 4 {
		return suiteOptions{}, fmt.Errorf("integration jobs must be between 1 and 4")
	}
	if options.GoWork != integrationGoWorkOff && !filepath.IsAbs(options.GoWork) {
		return suiteOptions{}, fmt.Errorf("integration go.work must be off or an absolute path")
	}
	if strings.TrimSpace(options.SchemaRoot) == "" || strings.TrimSpace(options.PostgresImage) == "" {
		return suiteOptions{}, fmt.Errorf("schema root and PostgreSQL image are required")
	}
	if options.Band != "" && options.Package != "" {
		return suiteOptions{}, fmt.Errorf("integration band and package are mutually exclusive")
	}
	if options.Package != "" {
		if _, ok := bandByPackage(options.Package); !ok {
			return suiteOptions{}, fmt.Errorf("unknown integration package %q", options.Package)
		}
	}
	runProvided := false
	flags.Visit(func(parsed *flag.Flag) {
		if parsed.Name == "run" {
			runProvided = true
		}
	})
	if runProvided {
		if options.Package == "" {
			return suiteOptions{}, fmt.Errorf("integration test name regexp requires --package")
		}
		if options.Run == "" {
			return suiteOptions{}, fmt.Errorf("integration test name regexp cannot be empty")
		}
		if _, err := regexp.Compile(options.Run); err != nil {
			return suiteOptions{}, fmt.Errorf("invalid integration test name regexp: %w", err)
		}
	}
	if options.List {
		return options, nil
	}
	if options.Band != "" {
		if _, ok := bandByName(options.Band); !ok {
			return suiteOptions{}, fmt.Errorf("unknown integration band %q", options.Band)
		}
	}
	return options, nil
}

func selectedIntegrationBands(name, packagePath string) ([]integrationBand, error) {
	if name == "" && packagePath == "" {
		return append([]integrationBand(nil), integrationCatalog...), nil
	}
	if packagePath != "" {
		band, ok := bandByPackage(packagePath)
		if !ok {
			return nil, fmt.Errorf("unknown integration package %q", packagePath)
		}
		band.ParallelPackages = false
		band.Packages = []string{packagePath}
		return []integrationBand{band}, nil
	}
	band, ok := bandByName(name)
	if !ok {
		return nil, fmt.Errorf("unknown integration band %q", name)
	}
	return []integrationBand{band}, nil
}

func bandGoTestArguments(band integrationBand, jobs int) []string {
	if !band.ParallelPackages {
		jobs = 1
	}
	return append([]string{
		"test",
		"-p", strconv.Itoa(jobs),
		"-parallel", "1",
		"-timeout", "30m",
		"-count=1",
		"-tags=integration",
	}, band.Packages...)
}

func packageGoTestArguments(packagePath, run string) []string {
	arguments := []string{
		"test",
		"-p", "1",
		"-parallel", "1",
		"-timeout", "30m",
		"-count=1",
		"-tags=integration",
	}
	if run != "" {
		arguments = append(arguments, "-run", run)
	}
	return append(arguments, packagePath)
}
