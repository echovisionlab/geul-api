package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil"
)

func TestRunSerialIntegrationBandCollectsPackageFailures(t *testing.T) {
	t.Parallel()

	firstErr := errors.New("first package failed")
	lastErr := errors.New("last package failed")
	operations := make([]string, 0, 8)
	backend := &suiteBackend{resetFn: func(context.Context) error {
		operations = append(operations, "reset")
		return nil
	}}
	runPackage := func(packagePath string) error {
		operations = append(operations, "run "+packagePath)
		switch packagePath {
		case "./first":
			return firstErr
		case "./last":
			return lastErr
		default:
			return nil
		}
	}

	err := runSerialIntegrationBand(
		context.Background(),
		[]string{"./first", "./second", "./last"},
		backend,
		runPackage,
	)
	if !errors.Is(err, firstErr) || !errors.Is(err, lastErr) {
		t.Fatalf("band error = %v, want both package failures", err)
	}
	wantOperations := []string{
		"reset", "run ./first", "reset",
		"reset", "run ./second", "reset",
		"reset", "run ./last", "reset",
	}
	if !reflect.DeepEqual(operations, wantOperations) {
		t.Fatalf("operations = %q, want %q", operations, wantOperations)
	}
}

func TestRunSerialIntegrationBandStopsAndReportsPackageAndResetFailures(t *testing.T) {
	t.Parallel()

	commandErr := errors.New("package command failed")
	resetErr := errors.New("backend reset failed")
	resetCalls := 0
	operations := make([]string, 0, 3)
	backend := &suiteBackend{resetFn: func(context.Context) error {
		resetCalls++
		operations = append(operations, "reset")
		if resetCalls == 2 {
			return resetErr
		}
		return nil
	}}
	runPackage := func(packagePath string) error {
		operations = append(operations, "run "+packagePath)
		if packagePath == "./first" {
			return commandErr
		}
		t.Fatalf("ran %s after backend reset failure", packagePath)
		return nil
	}

	err := runSerialIntegrationBand(
		context.Background(),
		[]string{"./first", "./must-not-run"},
		backend,
		runPackage,
	)
	if !errors.Is(err, commandErr) || !errors.Is(err, resetErr) {
		t.Fatalf("band error = %v, want command and reset failures", err)
	}
	if !strings.Contains(err.Error(), "run integration package ./first") ||
		!strings.Contains(err.Error(), "reset suite backend after ./first") {
		t.Fatalf("band error lacks package/reset context: %v", err)
	}
	wantOperations := []string{"reset", "run ./first", "reset"}
	if !reflect.DeepEqual(operations, wantOperations) {
		t.Fatalf("operations = %q, want %q", operations, wantOperations)
	}
}

func TestRunSerialIntegrationBandCancellationStopsNextPackageAfterCleanup(t *testing.T) {
	t.Parallel()

	type contextKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), contextKey{}, "suite"))
	defer cancel()
	resetCalls := 0
	backend := &suiteBackend{resetFn: func(resetCtx context.Context) error {
		resetCalls++
		if err := resetCtx.Err(); err != nil {
			t.Fatalf("reset must remain usable after package cancellation: %v", err)
		}
		if resetCtx.Value(contextKey{}) != "suite" {
			t.Fatal("reset lost suite context values")
		}
		if _, bounded := resetCtx.Deadline(); !bounded {
			t.Fatal("reset has no cleanup deadline")
		}
		return nil
	}}
	runCalls := 0
	err := runSerialIntegrationBand(ctx, []string{"./cancelled", "./must-not-run"}, backend, func(packagePath string) error {
		runCalls++
		if packagePath != "./cancelled" {
			t.Fatalf("ran %s after cancellation", packagePath)
		}
		cancel()
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("band error = %v, want context cancellation", err)
	}
	if runCalls != 1 || resetCalls != 2 {
		t.Fatalf("run calls = %d, reset calls = %d; want one package and both resets", runCalls, resetCalls)
	}
}

func TestRunSerialIntegrationBandDoesNotStartCancelledSuite(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	backend := &suiteBackend{resetFn: func(context.Context) error {
		t.Fatal("reset started after suite cancellation")
		return nil
	}}
	err := runSerialIntegrationBand(ctx, []string{"./must-not-run"}, backend, func(string) error {
		t.Fatal("package started after suite cancellation")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("band error = %v, want context cancellation", err)
	}
}

func TestRunSerialIntegrationBandCancellationDuringResetDoesNotStartPackage(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	backend := &suiteBackend{resetFn: func(context.Context) error {
		cancel()
		return nil
	}}
	err := runSerialIntegrationBand(ctx, []string{"./must-not-run"}, backend, func(string) error {
		t.Fatal("package started after cancellation during reset")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("band error = %v, want context cancellation", err)
	}
}

func TestEnvironmentWithGoWorkReplacesAmbientWorkspace(t *testing.T) {
	t.Parallel()

	got := environmentWithGoWork(
		[]string{"PATH=/bin", "GOWORK=/old/go.work", "HOME=/home/test"},
		"/repo/go.work",
	)
	want := []string{"PATH=/bin", "HOME=/home/test", "GOWORK=/repo/go.work"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment = %q, want %q", got, want)
	}
}

func TestEnvironmentWithMinIOImageReplacesAmbientOverride(t *testing.T) {
	t.Parallel()

	got := environmentWithMinIOImage(
		[]string{"PATH=/bin", testutil.AppIntegrationMinIOImageEnv + "=stale-minio:tag", "HOME=/home/test"},
		"geul-test-minio:local",
	)
	want := []string{"PATH=/bin", "HOME=/home/test", testutil.AppIntegrationMinIOImageEnv + "=geul-test-minio:local"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment = %q, want %q", got, want)
	}
}

func TestValidateIntegrationGoWork(t *testing.T) {
	t.Parallel()

	if got, err := validateIntegrationGoWork(integrationGoWorkOff); err != nil || got != integrationGoWorkOff {
		t.Fatalf("validate off = %q, %v", got, err)
	}

	directory := t.TempDir()
	goWork := filepath.Join(directory, "go.work")
	if err := os.WriteFile(goWork, []byte("go 1.26.6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := validateIntegrationGoWork(goWork)
	if err != nil || got != goWork {
		t.Fatalf("validate go.work = %q, %v", got, err)
	}

	for _, invalid := range []string{
		"relative/go.work",
		filepath.Join(directory, "missing", "go.work"),
		filepath.Join(directory, "not-go-work"),
	} {
		if _, err := validateIntegrationGoWork(invalid); err == nil {
			t.Fatalf("validateIntegrationGoWork(%q) succeeded", invalid)
		}
	}
}
