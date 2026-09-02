package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

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
	options.GoWork, err = validateIntegrationGoWork(options.GoWork)
	if err != nil {
		return err
	}
	discovered, err := discoverIntegrationPackages(repoRoot, options.GoWork)
	if err != nil {
		return err
	}
	if err := verifyIntegrationCatalog(discovered); err != nil {
		return err
	}
	if options.List {
		for _, band := range integrationCatalog {
			fmt.Printf("%s (%d)\n", band.Name, len(band.Packages))
			for _, packagePath := range band.Packages {
				fmt.Printf("  %s\n", packagePath)
			}
		}
		return nil
	}
	options.SchemaRoot, err = filepath.Abs(options.SchemaRoot)
	if err != nil {
		return fmt.Errorf("resolve schema root: %w", err)
	}

	takeSuiteSignalCleanupOwnership()
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	bands, err := selectedIntegrationBands(options.Band, options.Package)
	if err != nil {
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
	if err := runSuiteCommand(ctx, repoRoot, options.GoWork, preflight); err != nil {
		return fmt.Errorf("verify logical database leases: %w", err)
	}
	var backend *suiteBackend
	if integrationBandsNeedBackend(bands) {
		backend, err = startSuiteBackend(ctx, leasePath, options.CDNImage)
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
			if err := runSuiteCommand(ctx, repoRoot, options.GoWork, arguments); err != nil {
				return fmt.Errorf("run integration band %s: %w", band.Name, err)
			}
			continue
		}
		if err := runSerialIntegrationBand(ctx, band.Packages, backend, func(packagePath string) error {
			arguments := append(
				packageGoTestArguments(packagePath),
				"-args", "-geul-integration-lease-file="+leasePath,
			)
			return runSuiteCommand(ctx, repoRoot, options.GoWork, arguments)
		}); err != nil {
			return err
		}
	}
	return nil
}

func runSerialIntegrationBand(
	ctx context.Context,
	packages []string,
	backend *suiteBackend,
	runPackage func(string) error,
) error {
	packageErrors := make([]error, 0, len(packages))
	for _, packagePath := range packages {
		if err := resetSuiteBackend(ctx, backend); err != nil {
			packageErrors = append(
				packageErrors,
				fmt.Errorf("reset suite backend before %s: %w", packagePath, err),
			)
			return errors.Join(packageErrors...)
		}

		commandErr := runPackage(packagePath)
		resetErr := resetSuiteBackend(context.Background(), backend)
		if commandErr != nil {
			packageErrors = append(
				packageErrors,
				fmt.Errorf("run integration package %s: %w", packagePath, commandErr),
			)
		}
		if resetErr != nil {
			packageErrors = append(
				packageErrors,
				fmt.Errorf("reset suite backend after %s: %w", packagePath, resetErr),
			)
			return errors.Join(packageErrors...)
		}
	}
	return errors.Join(packageErrors...)
}

func resetSuiteBackend(parent context.Context, backend *suiteBackend) error {
	resetCtx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	return backend.Reset(resetCtx)
}

func integrationBandsNeedBackend(bands []integrationBand) bool {
	for _, band := range bands {
		if band.Resources != integrationPostgresOnly {
			return true
		}
	}
	return false
}

func writeSuiteLease(postgres *suitePostgres) (string, func() error, error) {
	file, err := os.CreateTemp("", "geul-api-integration-lease-*.json")
	if err != nil {
		return "", nil, fmt.Errorf("create integration lease descriptor: %w", err)
	}
	path := file.Name()
	remove := func() error {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	failed := true
	defer func() {
		if failed {
			_ = file.Close()
			_ = remove()
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", nil, fmt.Errorf("protect integration lease descriptor: %w", err)
	}
	descriptor := testutil.AppIntegrationLeaseDescriptor{
		Version:              testutil.AppIntegrationLeaseVersion,
		PostgresAdminDSN:     postgres.AdminDSN,
		PostgresContainerID:  postgres.ContainerID,
		PostgresTemplateName: postgres.Template,
	}
	if err := json.NewEncoder(file).Encode(descriptor); err != nil {
		return "", nil, fmt.Errorf("write integration lease descriptor: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", nil, fmt.Errorf("sync integration lease descriptor: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", nil, fmt.Errorf("close integration lease descriptor: %w", err)
	}
	failed = false
	return path, remove, nil
}

func updateSuiteLeaseBackend(path string, backend *testutil.AppIntegrationBackendLease) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read integration lease descriptor: %w", err)
	}
	var descriptor testutil.AppIntegrationLeaseDescriptor
	if err := json.Unmarshal(contents, &descriptor); err != nil {
		return fmt.Errorf("decode integration lease descriptor for backend update: %w", err)
	}
	descriptor.Backend = backend
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open integration lease descriptor for backend update: %w", err)
	}
	if err := json.NewEncoder(file).Encode(descriptor); err != nil {
		_ = file.Close()
		return fmt.Errorf("write integration backend lease: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync integration backend lease: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close integration backend lease: %w", err)
	}
	return nil
}

func runSuiteCommand(ctx context.Context, repoRoot, goWork string, arguments []string) error {
	command := exec.CommandContext(ctx, "go", arguments...)
	command.Dir = repoRoot
	command.Env = integrationCommandEnvironment(goWork)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := runCommandInProcessGroup(command, 2*time.Second); err != nil {
		return fmt.Errorf("go %s: %w", strings.Join(arguments, " "), err)
	}
	return nil
}

func runCommandInProcessGroup(command *exec.Cmd, terminationGrace time.Duration) error {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	command.WaitDelay = 10 * time.Second
	if err := command.Start(); err != nil {
		return err
	}
	processGroupID := command.Process.Pid
	commandErr := command.Wait()
	cleanupErr := terminateProcessGroup(processGroupID, terminationGrace)
	return errors.Join(commandErr, cleanupErr)
}

func terminateProcessGroup(processGroupID int, grace time.Duration) error {
	if processGroupID <= 0 {
		return nil
	}
	if err := signalProcessGroup(processGroupID, syscall.SIGTERM); err != nil {
		return err
	}
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		exists, err := processGroupExists(processGroupID)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		select {
		case <-deadline.C:
			return signalProcessGroup(processGroupID, syscall.SIGKILL)
		case <-ticker.C:
		}
	}
}

func signalProcessGroup(processGroupID int, signal syscall.Signal) error {
	err := syscall.Kill(-processGroupID, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func processGroupExists(processGroupID int) (bool, error) {
	err := syscall.Kill(-processGroupID, 0)
	switch {
	case err == nil, errors.Is(err, syscall.EPERM):
		return true, nil
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	default:
		return false, err
	}
}

func integrationCommandEnvironment(goWork string) []string {
	return environmentWithGoWork(os.Environ(), goWork)
}

func environmentWithGoWork(base []string, goWork string) []string {
	environment := make([]string, 0, len(base)+1)
	for _, entry := range base {
		if strings.HasPrefix(entry, "GOWORK=") {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, "GOWORK="+goWork)
}

func validateIntegrationGoWork(goWork string) (string, error) {
	if goWork == integrationGoWorkOff {
		return integrationGoWorkOff, nil
	}
	cleaned := filepath.Clean(goWork)
	if !filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("integration go.work must be off or an absolute path")
	}
	info, err := os.Stat(cleaned)
	if err != nil {
		return "", fmt.Errorf("inspect integration go.work: %w", err)
	}
	if !info.Mode().IsRegular() || filepath.Base(cleaned) != "go.work" {
		return "", fmt.Errorf("integration go.work must name a regular go.work file")
	}
	return cleaned, nil
}
