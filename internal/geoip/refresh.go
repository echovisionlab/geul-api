package geoip

import (
	"context"
	"fmt"
	"time"
)

const RefreshInterval = 7 * 24 * time.Hour

type RefreshOutcome string

const (
	RefreshCurrent              RefreshOutcome = "current"
	RefreshProviderUnconfigured RefreshOutcome = "provider_unconfigured"
	RefreshUpdated              RefreshOutcome = "updated"
	RefreshBecameCurrent        RefreshOutcome = "became_current"
)

// RefreshExecutor is the infrastructure boundary required by the refresh
// application service. The domain owns when refresh is due; the executor owns
// provider transport and atomic persistence.
type RefreshExecutor interface {
	ProviderConfigured() bool
	CurrentImportedAt(context.Context) (*time.Time, error)
	ImportLatestIfOlderThan(context.Context, time.Time, time.Time) (bool, error)
}

type RefreshService struct {
	executor RefreshExecutor
}

func NewRefreshService(executor RefreshExecutor) *RefreshService {
	return &RefreshService{executor: executor}
}

func (s *RefreshService) Refresh(ctx context.Context, now time.Time) (RefreshOutcome, error) {
	if s == nil || s.executor == nil {
		return "", fmt.Errorf("GeoIP refresh executor is required")
	}

	now = now.UTC()
	cutoff := now.Add(-RefreshInterval)
	importedAt, err := s.executor.CurrentImportedAt(ctx)
	if err != nil {
		return "", fmt.Errorf("load current GeoIP dataset: %w", err)
	}
	if importedAt != nil && importedAt.After(cutoff) {
		return RefreshCurrent, nil
	}
	if !s.executor.ProviderConfigured() {
		return RefreshProviderUnconfigured, nil
	}

	imported, err := s.executor.ImportLatestIfOlderThan(ctx, now, cutoff)
	if err != nil {
		return "", fmt.Errorf("refresh GeoIP dataset: %w", err)
	}
	if !imported {
		return RefreshBecameCurrent, nil
	}
	return RefreshUpdated, nil
}
