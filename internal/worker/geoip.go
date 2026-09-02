package worker

import (
	"context"
	"log/slog"
	"time"

	geoipadapter "github.com/echovisionlab/geul-api/internal/adapters/geoip"
	"github.com/echovisionlab/geul-api/internal/geoip"
)

// handleUpdateGeoIP is the scheduled-message dispatch boundary. Refresh policy
// and execution live in the owning domain and infrastructure adapter.
func (h *Handlers) handleUpdateGeoIP(ctx context.Context) error {
	executor := geoipadapter.NewRefreshExecutor(
		h.db,
		h.config.MaxMindAccountID,
		h.config.MaxMindLicenseKey,
	)
	outcome, err := geoip.NewRefreshService(executor).Refresh(ctx, time.Now().UTC())
	if err != nil {
		return err
	}

	switch outcome {
	case geoip.RefreshCurrent:
		slog.Info("GeoIP database is current")
	case geoip.RefreshProviderUnconfigured:
		slog.Warn("MaxMind credentials not configured, skipping GeoIP update")
	case geoip.RefreshUpdated:
		slog.Info("GeoIP database update completed")
	case geoip.RefreshBecameCurrent:
		slog.Info("GeoIP database became current while this update was downloading")
	}
	return nil
}
