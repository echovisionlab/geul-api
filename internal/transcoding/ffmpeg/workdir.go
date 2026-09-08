package ffmpeg

import (
	"fmt"
	"os"
	"path/filepath"
)

// CreateJobWorkDir creates a temporary directory for a job
func (e *Executor) CreateJobWorkDir(jobID string) (string, error) {
	dir := filepath.Join(e.tempDir, jobID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create work directory: %w", err)
	}
	return dir, nil
}

// CleanupStaleWorkDirs removes leftover job directories from previous process lifecycles.
// The temp dir is container-local, so clearing its children at startup is safe.
func (e *Executor) CleanupStaleWorkDirs() (int, error) {
	return cleanupStaleWorkDirs(e.tempDir, os.ReadDir, os.RemoveAll)
}

func cleanupStaleWorkDirs(
	tempDir string,
	readDir func(string) ([]os.DirEntry, error),
	removeAll func(string) error,
) (int, error) {
	entries, err := readDir(tempDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read temp directory: %w", err)
	}

	removed := 0
	for _, entry := range entries {
		target := filepath.Join(tempDir, entry.Name())
		if err := removeAll(target); err != nil {
			return removed, fmt.Errorf("remove stale temp path %s: %w", target, err)
		}
		removed++
	}

	return removed, nil
}

// CleanupJobWorkDir removes a job's temporary directory
func (e *Executor) CleanupJobWorkDir(jobID string) error {
	dir := filepath.Join(e.tempDir, jobID)
	return os.RemoveAll(dir)
}
