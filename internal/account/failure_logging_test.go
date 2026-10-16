package account

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

func TestEmailChangeReconciliationLogsSanitizedCause(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(sharedtelemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	logAccountEmailChangeError(context.Background(), "Reconciliation failed", "reconcile_scan_failed", fmt.Errorf("load requests: %w", errors.New(`database timeout on "account_request" <email> {constraint} person@example.test token=private`)))
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, "reconcile_scan_failed", entry["reason"])
	require.Contains(t, entry["failure_cause"], "load requests: database timeout")
	require.Contains(t, entry["failure_cause"], `"account_request" <email> {constraint}`)
	require.NotContains(t, output.String(), "person@example.test")
	require.NotContains(t, output.String(), "private")
}
