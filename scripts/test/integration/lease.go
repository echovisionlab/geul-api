package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

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
	descriptor := pgtest.AppIntegrationLeaseDescriptor{
		Version:              pgtest.AppIntegrationLeaseVersion,
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

func updateSuiteLeaseBackend(path string, backend *pgtest.AppIntegrationBackendLease) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read integration lease descriptor: %w", err)
	}
	var descriptor pgtest.AppIntegrationLeaseDescriptor
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
