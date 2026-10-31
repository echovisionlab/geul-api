package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/echovisionlab/geul-api/internal/testutil"
)

type suiteBackend struct {
	Lease   *testutil.AppIntegrationBackendLease
	closeFn func() error
	resetFn func(context.Context) error
}

func (backend *suiteBackend) Close() error {
	if backend == nil || backend.closeFn == nil {
		return nil
	}
	err := backend.closeFn()
	backend.closeFn = nil
	return err
}

func (backend *suiteBackend) Reset(ctx context.Context) error {
	if backend == nil || backend.resetFn == nil {
		return nil
	}
	return backend.resetFn(ctx)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (runErr error) {
	repoRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "go.mod")); err != nil {
		return fmt.Errorf("integration suite must run from the API repository root: %w", err)
	}
	options, err := parseOptions(os.Args[1:])
	if err != nil {
		return err
	}
	if err := configureSuiteMinIOImage(options.MinIOImage); err != nil {
		return fmt.Errorf("configure MinIO integration image: %w", err)
	}
	options.GoWork, err = validateIntegrationGoWork(options.GoWork)
	if err != nil {
		return err
	}

	takeSuiteSignalCleanupOwnership()
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	discovered, err := discoverIntegrationPackages(ctx, repoRoot, options.GoWork)
	if err != nil {
		return err
	}
	if err := verifyIntegrationCatalog(discovered); err != nil {
		return err
	}
	bands, err := selectedIntegrationBands(options.Band, options.Package)
	if err != nil {
		return err
	}
	if options.ChangedFrom != "" {
		bands, err = affectedIntegrationBands(ctx, repoRoot, options.GoWork, options.ChangedFrom)
		if err != nil {
			return err
		}
	}
	if options.List {
		for _, band := range bands {
			fmt.Printf("%s (%d)\n", band.Name, len(band.Packages))
			for _, packagePath := range band.Packages {
				fmt.Printf("  %s\n", packagePath)
			}
		}
		return nil
	}
	if options.ListImages {
		if len(bands) > 0 {
			for _, image := range suiteImages(options, bands) {
				fmt.Println(image)
			}
		}
		return nil
	}
	if len(bands) == 0 {
		fmt.Println("no cataloged integration package depends on the changed files")
		return nil
	}
	options.SchemaRoot, err = filepath.Abs(options.SchemaRoot)
	if err != nil {
		return fmt.Errorf("resolve schema root: %w", err)
	}

	if err := requireSuiteAssets(repoRoot, options.SchemaRoot, bands); err != nil {
		return err
	}
	if err := requireLocalDockerImages(ctx, suiteImages(options, bands)); err != nil {
		return err
	}
	postgres, err := startSuitePostgres(ctx, options)
	if err != nil {
		return err
	}
	registerSuiteProcessCleanup("suite PostgreSQL", postgres.Close)
	defer func() {
		runErr = errors.Join(runErr, postgres.Close())
	}()

	leasePath, removeLease, err := writeSuiteLease(postgres)
	if err != nil {
		return err
	}
	registerSuiteProcessCleanup("suite lease descriptor", removeLease)
	defer func() {
		runErr = errors.Join(runErr, removeLease())
	}()
	preflight := []string{
		"test", "-p", "1", "-parallel", "1", "-timeout", "30m", "-count=1", "-tags=integration",
		"./internal/testutil", "-run", "^TestStartAppPostgresAdminLeasesAreDistinctAndDropped$",
		"-args", "-geul-integration-lease-file=" + leasePath,
	}
	if err := runSuiteCommand(ctx, repoRoot, options.GoWork, options.MinIOImage, preflight); err != nil {
		return fmt.Errorf("verify logical database leases: %w", err)
	}
	var backend *suiteBackend
	if integrationBandsNeedBackend(bands) {
		backend, err = startSuiteBackend(ctx, leasePath)
		if err != nil {
			return err
		}
		registerSuiteProcessCleanup("suite backend", backend.Close)
		defer func() {
			runErr = errors.Join(runErr, backend.Close())
		}()
		if err := updateSuiteLeaseBackend(leasePath, backend.Lease); err != nil {
			return err
		}
	}
	for _, band := range bands {
		if band.ParallelPackages {
			arguments := append(
				bandGoTestArguments(band, options.Jobs),
				"-args", "-geul-integration-lease-file="+leasePath,
			)
			if err := runSuiteCommand(ctx, repoRoot, options.GoWork, options.MinIOImage, arguments); err != nil {
				return fmt.Errorf("run integration band %s: %w", band.Name, err)
			}
			continue
		}
		if err := runSerialIntegrationBand(ctx, band.Packages, backend, func(packagePath string) error {
			arguments := append(
				packageGoTestArguments(packagePath, options.Run, band.Resources, options.Jobs),
				"-args", "-geul-integration-lease-file="+leasePath,
			)
			return runSuiteCommand(ctx, repoRoot, options.GoWork, options.MinIOImage, arguments)
		}); err != nil {
			return err
		}
	}
	return nil
}

func suiteImages(options suiteOptions, bands []integrationBand) []string {
	images := []string{options.PostgresImage}
	if integrationBandsNeedBackend(bands) {
		images = append(images, requiredSuiteBackendImages()...)
	}
	return images
}

func integrationBandsNeedBackend(bands []integrationBand) bool {
	for _, band := range bands {
		if band.Resources != integrationPostgresOnly {
			return true
		}
	}
	return false
}
