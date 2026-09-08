package workdir

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Manager struct {
	root string
}

var (
	mkdirAll  = os.MkdirAll
	mkdirTemp = os.MkdirTemp
	readDir   = os.ReadDir
	removeAll = os.RemoveAll
)

func NewManager(root string) (*Manager, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("temp root is required")
	}
	if err := mkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("create temp root: %w", err)
	}
	return &Manager{root: root}, nil
}

func (m *Manager) CreateJobWorkDir(jobID string) (string, error) {
	prefix := "mesh-" + sanitize(jobID) + "-"
	return mkdirTemp(m.root, prefix)
}

func (m *Manager) CleanupJobWorkDir(path string) error {
	if !strings.HasPrefix(filepath.Clean(path), filepath.Clean(m.root)+string(os.PathSeparator)) {
		return fmt.Errorf("refusing to remove path outside temp root: %s", path)
	}
	return removeAll(path)
}

func (m *Manager) CleanupStaleWorkDirs() (int, error) {
	entries, err := readDir(m.root)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	removed := 0
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "mesh-") {
			continue
		}
		if err := removeAll(filepath.Join(m.root, entry.Name())); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func sanitize(value string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", " ", "-", ":", "-", "\x00", "-")
	value = strings.Trim(replacer.Replace(value), "-")
	if value == "" {
		return "job"
	}
	return value
}
