package workdir

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagerLifecycle(t *testing.T) {
	if _, err := NewManager("  "); err == nil {
		t.Fatal("expected blank root rejection")
	}

	withFileOps(t)
	mkdirAll = func(string, os.FileMode) error { return errors.New("mkdir failed") }
	if _, err := NewManager("/unused"); err == nil {
		t.Fatal("expected root creation error")
	}

	withFileOps(t)
	root := t.TempDir()
	manager, err := NewManager(root)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	jobDir, err := manager.CreateJobWorkDir("job / one")
	if err != nil {
		t.Fatalf("create job work dir: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(jobDir), "mesh-job---one-") {
		t.Fatalf("unexpected work dir name: %q", jobDir)
	}
	if err := manager.CleanupJobWorkDir(jobDir); err != nil {
		t.Fatalf("cleanup job work dir: %v", err)
	}
	if _, err := os.Stat(jobDir); !os.IsNotExist(err) {
		t.Fatalf("expected work dir removal, got %v", err)
	}
	if err := manager.CleanupJobWorkDir(root); err == nil {
		t.Fatal("expected root removal rejection")
	}

	withFileOps(t)
	manager = &Manager{root: root}
	mkdirTemp = func(string, string) (string, error) { return "", errors.New("temp failed") }
	if _, err := manager.CreateJobWorkDir("job"); err == nil {
		t.Fatal("expected temp creation error")
	}

	withFileOps(t)
	manager = &Manager{root: root}
	removeAll = func(string) error { return errors.New("remove failed") }
	if err := manager.CleanupJobWorkDir(filepath.Join(root, "mesh-job")); err == nil {
		t.Fatal("expected cleanup error")
	}
}

func TestCleanupStaleWorkDirs(t *testing.T) {
	withFileOps(t)
	missingRoot := filepath.Join(t.TempDir(), "missing")
	manager := &Manager{root: missingRoot}
	removed, err := manager.CleanupStaleWorkDirs()
	if err != nil || removed != 0 {
		t.Fatalf("missing root: removed=%d err=%v", removed, err)
	}

	withFileOps(t)
	readDir = func(string) ([]os.DirEntry, error) { return nil, errors.New("read failed") }
	if _, err := manager.CleanupStaleWorkDirs(); err == nil {
		t.Fatal("expected read error")
	}

	withFileOps(t)
	root := t.TempDir()
	for _, name := range []string{"mesh-old", "mesh-new", "keep-dir"} {
		if err := os.Mkdir(filepath.Join(root, name), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "mesh-file"), []byte("keep"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	manager = &Manager{root: root}
	removed, err = manager.CleanupStaleWorkDirs()
	if err != nil || removed != 2 {
		t.Fatalf("cleanup stale: removed=%d err=%v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(root, "keep-dir")); err != nil {
		t.Fatalf("non-mesh directory should remain: %v", err)
	}

	withFileOps(t)
	meshDir := filepath.Join(root, "mesh-failing")
	if err := os.Mkdir(meshDir, 0755); err != nil {
		t.Fatalf("mkdir failing entry: %v", err)
	}
	removeAll = func(string) error { return errors.New("remove failed") }
	removed, err = manager.CleanupStaleWorkDirs()
	if err == nil || removed != 0 {
		t.Fatalf("expected remove error, removed=%d err=%v", removed, err)
	}
}

func TestSanitize(t *testing.T) {
	if got := sanitize(" /\\ :\x00 "); got != "job" {
		t.Fatalf("expected fallback, got %q", got)
	}
	if got := sanitize("alpha:beta"); got != "alpha-beta" {
		t.Fatalf("unexpected sanitized value: %q", got)
	}
}

func withFileOps(t *testing.T) {
	t.Helper()
	mkdirAll = os.MkdirAll
	mkdirTemp = os.MkdirTemp
	readDir = os.ReadDir
	removeAll = os.RemoveAll
	t.Cleanup(func() {
		mkdirAll = os.MkdirAll
		mkdirTemp = os.MkdirTemp
		readDir = os.ReadDir
		removeAll = os.RemoveAll
	})
}
