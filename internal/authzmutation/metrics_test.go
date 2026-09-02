package authzmutation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestSynchronousAuthorizationCommitMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	previousProvider := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		otel.SetMeterProvider(previousProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	previousMetrics := defaultSynchronousAuthorizationMetrics
	defaultSynchronousAuthorizationMetrics = newSynchronousAuthorizationMetrics()
	t.Cleanup(func() { defaultSynchronousAuthorizationMetrics = previousMetrics })

	RecordAuthorizationCommitUncertain(t.Context(), time.Time{})
	RecordAuthorizationCommitSucceeded(t.Context(), time.Now().Add(-10*time.Millisecond))
	RecordAuthorizationCommitUncertain(t.Context(), time.Now().Add(-20*time.Millisecond))
	RecordAuthorizationRollbackCompensationFailed(t.Context())

	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(t.Context(), &collected))

	duration := findAuthorizationMetric(t, collected, "authorization_spicedb_to_database_commit_duration_seconds")
	histogram, ok := duration.Data.(metricdata.Histogram[float64])
	require.True(t, ok, "duration data = %T", duration.Data)
	require.Len(t, histogram.DataPoints, 2)
	for _, point := range histogram.DataPoints {
		require.Contains(t, point.Bounds, 5.0)
		require.EqualValues(t, 1, point.Count)
	}
	require.Equal(t, map[string]uint64{
		authorizationCommitOutcomeSucceeded: 1,
		authorizationCommitOutcomeUncertain: 1,
	}, authorizationHistogramCounts(t, histogram))

	failures := findAuthorizationMetric(t, collected, "authorization_boundary_failure_total")
	sum, ok := failures.Data.(metricdata.Sum[int64])
	require.True(t, ok, "failure data = %T", failures.Data)
	require.Equal(t, map[string]int64{
		authorizationBoundaryFailureCommitUncertain:            1,
		authorizationBoundaryFailureRollbackCompensationFailed: 1,
	}, authorizationFailureCounts(t, sum))
}

func findAuthorizationMetric(
	t *testing.T,
	collected metricdata.ResourceMetrics,
	name string,
) metricdata.Metrics {
	t.Helper()
	for _, scope := range collected.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name == name {
				return metric
			}
		}
	}
	t.Fatalf("metric %q was not collected", name)
	return metricdata.Metrics{}
}

func authorizationHistogramCounts(
	t *testing.T,
	histogram metricdata.Histogram[float64],
) map[string]uint64 {
	t.Helper()
	counts := make(map[string]uint64, len(histogram.DataPoints))
	for _, point := range histogram.DataPoints {
		outcome, ok := point.Attributes.Value(attribute.Key("outcome"))
		require.True(t, ok)
		counts[outcome.AsString()] = point.Count
	}
	return counts
}

func authorizationFailureCounts(
	t *testing.T,
	sum metricdata.Sum[int64],
) map[string]int64 {
	t.Helper()
	counts := make(map[string]int64, len(sum.DataPoints))
	for _, point := range sum.DataPoints {
		failure, ok := point.Attributes.Value(attribute.Key("failure"))
		require.True(t, ok)
		counts[failure.AsString()] = point.Value
	}
	return counts
}
