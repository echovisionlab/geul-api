//go:build integration

package pgtest

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"testing"
)

type IntegrationCleanup struct {
	name string
	fn   func() error
	once sync.Once
	err  error
}

type integrationSignalHandler struct {
	signals  chan os.Signal
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

var (
	integrationCleanupMu          sync.Mutex
	integrationSuiteCleanupActive bool
	integrationRegisteredCleanups []*IntegrationCleanup
	integrationSuiteCleanups      []*IntegrationCleanup
	integrationSuiteTempRoot      string
	integrationSignalCleanupOnce  sync.Once
	integrationSignalCleanup      *integrationSignalHandler
)

func init() {
	installIntegrationSignalCleanup()
}

func installIntegrationSignalCleanup() {
	integrationSignalCleanupOnce.Do(func() {
		handler := newIntegrationSignalHandler()
		integrationSignalCleanup = handler
		signal.Notify(handler.signals, os.Interrupt, syscall.SIGTERM)
		go handler.run(runIntegrationRegisteredCleanups, os.Exit)
	})
}

func newIntegrationSignalHandler() *integrationSignalHandler {
	return &integrationSignalHandler{
		signals: make(chan os.Signal, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func (handler *integrationSignalHandler) run(cleanup func() error, exit func(int)) {
	defer close(handler.done)
	select {
	case <-handler.stop:
		return
	case sig := <-handler.signals:
		select {
		case <-handler.stop:
			return
		default:
		}
		_ = cleanup()
		if syscallSignal, ok := sig.(syscall.Signal); ok {
			exit(128 + int(syscallSignal))
			return
		}
		exit(1)
	}
}

func (handler *integrationSignalHandler) stopAndWait() {
	handler.stopOnce.Do(func() {
		signal.Stop(handler.signals)
		close(handler.stop)
	})
	<-handler.done
}

// TakeIntegrationSignalCleanupOwnership disables the package SIGINT/SIGTERM
// handler and waits for it to stop. The integration orchestrator calls this
// before installing its own signal lifecycle; child test binaries do not call
// it and retain automatic registered-cleanup handling.
func TakeIntegrationSignalCleanupOwnership() {
	installIntegrationSignalCleanup()
	integrationSignalCleanup.stopAndWait()
}

func NewIntegrationCleanup(name string, fn func() error) *IntegrationCleanup {
	return &IntegrationCleanup{name: name, fn: fn}
}

func (cleanup *IntegrationCleanup) Run() error {
	cleanup.once.Do(func() {
		cleanup.err = cleanup.fn()
	})
	return cleanup.err
}

func WithIntegrationSuiteCleanupRegistration(fn func()) {
	integrationCleanupMu.Lock()
	integrationSuiteCleanupActive = true
	integrationCleanupMu.Unlock()

	defer func() {
		integrationCleanupMu.Lock()
		integrationSuiteCleanupActive = false
		integrationCleanupMu.Unlock()
	}()

	fn()
}

func RegisterIntegrationCleanup(t *testing.T, name string, fn func() error) {
	t.Helper()

	cleanup := NewIntegrationCleanup(name, fn)

	integrationCleanupMu.Lock()
	integrationRegisteredCleanups = append(integrationRegisteredCleanups, cleanup)
	if integrationSuiteCleanupActive {
		integrationSuiteCleanups = append(integrationSuiteCleanups, cleanup)
		integrationCleanupMu.Unlock()
		return
	}
	integrationCleanupMu.Unlock()

	t.Cleanup(func() {
		if err := cleanup.Run(); err != nil {
			t.Fatalf("integration cleanup %s: %v", name, err)
		}
	})
}

// RegisterIntegrationProcessCleanup registers a TestMain-owned cleanup for
// both normal shutdown and the integration SIGINT/SIGTERM handler. The cleanup
// itself must be idempotent because TestMain will also invoke it explicitly.
func RegisterIntegrationProcessCleanup(name string, fn func() error) {
	cleanup := NewIntegrationCleanup(name, fn)
	integrationCleanupMu.Lock()
	integrationRegisteredCleanups = append(integrationRegisteredCleanups, cleanup)
	integrationCleanupMu.Unlock()
}

func IntegrationTempDir(t *testing.T, pattern string) string {
	t.Helper()

	integrationCleanupMu.Lock()
	if integrationSuiteCleanupActive {
		root := integrationSuiteTempRoot
		if root == "" {
			var err error
			root, err = os.MkdirTemp("", "geul-integration-suite-*")
			if err != nil {
				integrationCleanupMu.Unlock()
				t.Fatalf("create integration suite temp dir: %v", err)
			}
			integrationSuiteTempRoot = root
			cleanup := NewIntegrationCleanup("suite temp dir", func() error {
				return os.RemoveAll(root)
			})
			integrationRegisteredCleanups = append(integrationRegisteredCleanups, cleanup)
			integrationSuiteCleanups = append(integrationSuiteCleanups, cleanup)
		}
		integrationCleanupMu.Unlock()

		dir, err := os.MkdirTemp(root, pattern+"-*")
		if err != nil {
			t.Fatalf("create integration temp dir: %v", err)
		}
		return dir
	}
	integrationCleanupMu.Unlock()

	return t.TempDir()
}

// RunIntegrationSuiteCleanups runs and drains resources registered for the
// current integration test binary. It is safe to call more than once.
func RunIntegrationSuiteCleanups() error {
	integrationCleanupMu.Lock()
	cleanups := append([]*IntegrationCleanup(nil), integrationSuiteCleanups...)
	integrationSuiteCleanups = nil
	integrationSuiteTempRoot = ""
	integrationCleanupMu.Unlock()

	return runIntegrationCleanups(cleanups)
}

func runIntegrationRegisteredCleanups() error {
	integrationCleanupMu.Lock()
	cleanups := append([]*IntegrationCleanup(nil), integrationRegisteredCleanups...)
	integrationRegisteredCleanups = nil
	integrationSuiteCleanups = nil
	integrationSuiteTempRoot = ""
	integrationCleanupMu.Unlock()

	return runIntegrationCleanups(cleanups)
}

func runIntegrationCleanups(cleanups []*IntegrationCleanup) error {
	var firstErr error
	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanup := cleanups[i]
		if err := cleanup.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "integration cleanup %s: %v\n", cleanup.name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
