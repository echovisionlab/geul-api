//go:build integration

package testutil

import (
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	code := m.Run()
	if err := RunIntegrationSuiteCleanups(); err != nil && code == 0 {
		code = 1
	}
	os.Exit(code)
}

func TestRunIntegrationSuiteCleanupsIsIdempotent(t *testing.T) {
	integrationCleanupMu.Lock()
	registeredBefore := integrationRegisteredCleanups
	suiteBefore := integrationSuiteCleanups
	tempRootBefore := integrationSuiteTempRoot
	integrationRegisteredCleanups = nil
	integrationSuiteCleanups = nil
	integrationSuiteTempRoot = ""
	integrationCleanupMu.Unlock()
	defer func() {
		integrationCleanupMu.Lock()
		integrationRegisteredCleanups = registeredBefore
		integrationSuiteCleanups = suiteBefore
		integrationSuiteTempRoot = tempRootBefore
		integrationCleanupMu.Unlock()
	}()

	calls := 0
	withIntegrationSuiteCleanupRegistration(func() {
		registerIntegrationCleanup(t, "shared runtime", func() error {
			calls++
			return nil
		})
	})

	require.NoError(t, RunIntegrationSuiteCleanups())
	require.NoError(t, RunIntegrationSuiteCleanups())
	require.Equal(t, 1, calls)
}

func TestIntegrationSignalHandlerStopTransfersOwnershipIdempotently(t *testing.T) {
	handler := newIntegrationSignalHandler()
	cleanupCalls := 0
	exitCalls := 0
	go handler.run(
		func() error {
			cleanupCalls++
			return nil
		},
		func(int) { exitCalls++ },
	)

	handler.stopAndWait()
	handler.stopAndWait()
	require.Zero(t, cleanupCalls)
	require.Zero(t, exitCalls)
}

func TestIntegrationSignalHandlerRunsCleanupAndPreservesSignalExitCode(t *testing.T) {
	handler := newIntegrationSignalHandler()
	cleanupCalls := 0
	exitCode := 0
	handler.signals <- syscall.SIGTERM
	handler.run(
		func() error {
			cleanupCalls++
			return nil
		},
		func(code int) { exitCode = code },
	)

	require.Equal(t, 1, cleanupCalls)
	require.Equal(t, 128+int(syscall.SIGTERM), exitCode)
}
