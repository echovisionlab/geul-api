package main

import (
	"fmt"
	"os"
	"path/filepath"
)

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
