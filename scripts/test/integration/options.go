package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/echovisionlab/geul-api/internal/testutil"
)

type suiteOptions struct {
	Band          string
	Package       string
	Jobs          int
	List          bool
	GoWork        string
	SchemaRoot    string
	PostgresImage string
	CDNImage      string
}

const integrationGoWorkOff = "off"

func parseOptions(args []string) (suiteOptions, error) {
	jobs := 2

	flags := flag.NewFlagSet("integration-suite", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := suiteOptions{Jobs: jobs, GoWork: integrationGoWorkOff}
	flags.StringVar(&options.Band, "band", "", "resource band to run")
	flags.StringVar(&options.Package, "package", "", "cataloged integration package to run")
	flags.IntVar(&options.Jobs, "jobs", jobs, "maximum concurrent packages")
	flags.BoolVar(&options.List, "list", false, "list the verified catalog")
	flags.StringVar(&options.GoWork, "go-work", integrationGoWorkOff, "Go workspace: off or an absolute go.work path")
	flags.StringVar(&options.SchemaRoot, "schema-root", "../geul-schema", "reviewed schema asset root")
	flags.StringVar(&options.PostgresImage, "postgres-image", testutil.AppIntegrationPostgresImage, "local PostgreSQL image")
	flags.StringVar(&options.CDNImage, "cdn-image", "geul-cdn:integration", "local CDN image")
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
	if strings.TrimSpace(options.SchemaRoot) == "" || strings.TrimSpace(options.PostgresImage) == "" || strings.TrimSpace(options.CDNImage) == "" {
		return suiteOptions{}, fmt.Errorf("schema root, PostgreSQL image, and CDN image are required")
	}
	if options.Band != "" && options.Package != "" {
		return suiteOptions{}, fmt.Errorf("integration band and package are mutually exclusive")
	}
	if options.Package != "" {
		if _, ok := bandByPackage(options.Package); !ok {
			return suiteOptions{}, fmt.Errorf("unknown integration package %q", options.Package)
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

func packageGoTestArguments(packagePath string) []string {
	return []string{
		"test",
		"-p", "1",
		"-parallel", "1",
		"-timeout", "30m",
		"-count=1",
		"-tags=integration",
		packagePath,
	}
}
