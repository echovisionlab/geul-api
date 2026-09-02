package geoip

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type refreshExecutorStub struct {
	configured bool
	importedAt *time.Time
	loadErr    error
	imported   bool
	importErr  error
	called     bool
	now        time.Time
	cutoff     time.Time
}

func (s *refreshExecutorStub) ProviderConfigured() bool { return s.configured }

func (s *refreshExecutorStub) CurrentImportedAt(context.Context) (*time.Time, error) {
	return s.importedAt, s.loadErr
}

func (s *refreshExecutorStub) ImportLatestIfOlderThan(_ context.Context, now, cutoff time.Time) (bool, error) {
	s.called = true
	s.now = now
	s.cutoff = cutoff
	return s.imported, s.importErr
}

func TestRefreshServiceSkipsCurrentDataset(t *testing.T) {
	now := time.Date(2026, time.August, 23, 1, 2, 3, 0, time.FixedZone("test", 9*60*60))
	importedAt := now.UTC().Add(-RefreshInterval + time.Second)
	executor := &refreshExecutorStub{configured: true, importedAt: &importedAt}

	outcome, err := NewRefreshService(executor).Refresh(context.Background(), now)

	require.NoError(t, err)
	require.Equal(t, RefreshCurrent, outcome)
	require.False(t, executor.called)
}

func TestRefreshServiceTreatsExactCutoffAsDue(t *testing.T) {
	now := time.Date(2026, time.August, 23, 1, 2, 3, 0, time.UTC)
	importedAt := now.Add(-RefreshInterval)
	executor := &refreshExecutorStub{configured: true, importedAt: &importedAt, imported: true}

	outcome, err := NewRefreshService(executor).Refresh(context.Background(), now)

	require.NoError(t, err)
	require.Equal(t, RefreshUpdated, outcome)
	require.True(t, executor.called)
	require.Equal(t, now, executor.now)
	require.Equal(t, importedAt, executor.cutoff)
}

func TestRefreshServiceSkipsDueDatasetWithoutProviderConfiguration(t *testing.T) {
	executor := &refreshExecutorStub{}

	outcome, err := NewRefreshService(executor).Refresh(context.Background(), time.Now())

	require.NoError(t, err)
	require.Equal(t, RefreshProviderUnconfigured, outcome)
	require.False(t, executor.called)
}

func TestRefreshServiceReportsConcurrentRefreshWinner(t *testing.T) {
	executor := &refreshExecutorStub{configured: true, imported: false}

	outcome, err := NewRefreshService(executor).Refresh(context.Background(), time.Now())

	require.NoError(t, err)
	require.Equal(t, RefreshBecameCurrent, outcome)
}

func TestRefreshServiceWrapsInfrastructureFailures(t *testing.T) {
	t.Run("load", func(t *testing.T) {
		executor := &refreshExecutorStub{loadErr: errors.New("database unavailable")}

		_, err := NewRefreshService(executor).Refresh(context.Background(), time.Now())

		require.ErrorContains(t, err, "load current GeoIP dataset")
	})

	t.Run("import", func(t *testing.T) {
		executor := &refreshExecutorStub{configured: true, importErr: errors.New("provider unavailable")}

		_, err := NewRefreshService(executor).Refresh(context.Background(), time.Now())

		require.ErrorContains(t, err, "refresh GeoIP dataset")
	})
}
