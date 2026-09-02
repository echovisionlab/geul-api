package application

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"
)

func TestTranslationTerminalTelemetryEmitsAppliedResults(t *testing.T) {
	now := time.Unix(1_700_001_000, 0).UTC()
	job := &model.TranslationJob{
		ID: uuid.NewString(), EntityType: "post", EntityID: uuid.NewString(), TargetLocale: "ko", Status: translationJobStatusApplied,
	}
	manager := &TranslationJobManager{publisher: stubTranslationJobPublisher{}, now: func() time.Time { return now }, metrics: newTranslationMetrics()}

	events := captureTranslationTerminalEvents(t, func() {
		require.NoError(t, manager.finalizeAppliedTranslationDelivery(context.Background(), &translationDeliveryExecution{
			job: job, startedAt: now.Add(-1500 * time.Millisecond),
		}, now))
	})

	require.Len(t, events, 1)
	requireTranslationTerminalEvent(t, events[0], job, "applied", "", 1500)
}

func TestTranslationTerminalTelemetryEmitsFailedResultsAfterRunningAndQueuedTransitions(t *testing.T) {
	t.Run("running", func(t *testing.T) {
		db := newTranslationRetryTestDB(t)
		now := time.Unix(1_700_001_200, 0).UTC()
		job := seedTranslationRetryTestJob(t, db, translationJobStatusRunning, "operation", uuid.NewString())
		manager := &TranslationJobManager{db: db, publisher: stubTranslationJobPublisher{}, now: func() time.Time { return now }, metrics: newTranslationMetrics()}

		events := captureTranslationTerminalEvents(t, func() {
			require.NoError(t, manager.failJob(context.Background(), job, now.Add(-2*time.Second), errTranslationOgHandoffFailed))
		})

		require.ErrorIs(t, db.First(&model.TranslationJob{}, "id = ?", job.ID).Error, gorm.ErrRecordNotFound)
		require.Len(t, events, 1)
		requireTranslationTerminalEvent(t, events[0], job, "failed", translationFailureOgHandoffFailed, 2000)
	})

	t.Run("queued", func(t *testing.T) {
		db := newTranslationRetryTestDB(t)
		now := time.Unix(1_700_001_300, 0).UTC()
		job := seedTranslationRetryTestJob(t, db, translationJobStatusQueued, "operation", uuid.NewString())
		manager := &TranslationJobManager{db: db, publisher: stubTranslationJobPublisher{}, now: func() time.Time { return now }, metrics: newTranslationMetrics()}

		events := captureTranslationTerminalEvents(t, func() {
			require.NoError(t, manager.failQueuedJob(context.Background(), job, errTranslationProviderUnavailable))
		})

		require.ErrorIs(t, db.First(&model.TranslationJob{}, "id = ?", job.ID).Error, gorm.ErrRecordNotFound)
		require.Len(t, events, 1)
		requireTranslationTerminalEvent(t, events[0], job, "failed", translationFailureProviderUnavailable, 0)
	})

}

func TestTranslationTerminalTelemetryDoesNotEmitAfterAnotherHandlerDeletedJob(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	now := time.Unix(1_700_001_400, 0).UTC()
	job := seedTranslationRetryTestJob(t, db, translationJobStatusRunning, "operation", uuid.NewString())
	require.NoError(t, db.Delete(&model.TranslationJob{}, "id = ?", job.ID).Error)
	manager := &TranslationJobManager{db: db, publisher: stubTranslationJobPublisher{}, now: func() time.Time { return now }, metrics: newTranslationMetrics()}

	events := captureTranslationTerminalEvents(t, func() {
		require.NoError(t, manager.failJob(context.Background(), job, now.Add(-time.Second), errTranslationProviderUnavailable))
	})

	require.Empty(t, events)
}

func TestTranslationTerminalTelemetryCorrelatesNewRequestAfterFailure(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	failedAt := time.Unix(1_700_001_500, 0).UTC()
	now := failedAt
	entityID := uuid.NewString()
	job := seedTranslationRetryTestJob(t, db, translationJobStatusRunning, "operation", entityID)
	failedJob := *job
	manager := &TranslationJobManager{db: db, publisher: stubTranslationJobPublisher{}, now: func() time.Time { return now }, metrics: newTranslationMetrics()}

	events := captureTranslationTerminalEvents(t, func() {
		require.NoError(t, manager.failJob(context.Background(), job, failedAt.Add(-time.Second), errTranslationProviderUnavailable))
		now = failedAt.Add(2 * time.Second)
		job = &model.TranslationJob{
			ID: uuid.NewString(), EntityType: failedJob.EntityType, EntityID: entityID,
			TargetLocale: failedJob.TargetLocale, Status: translationJobStatusApplied,
		}
		require.NoError(t, manager.finalizeAppliedTranslationDelivery(context.Background(), &translationDeliveryExecution{
			job: job, startedAt: failedAt.Add(-time.Second),
		}, now))
	})

	require.Len(t, events, 2)
	requireTranslationTerminalEvent(t, events[0], &failedJob, "failed", translationFailureProviderUnavailable, 1000)
	require.NotEqual(t, events[0]["job_id"], events[1]["job_id"])
	requireTranslationTerminalEvent(t, events[1], job, "applied", "", 3000)
}

func TestTranslationTerminalTelemetryPreservesRequestTraceAndSpanCorrelation(t *testing.T) {
	request, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.AnonymousActor{})
	require.NoError(t, err)
	traceID, err := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("0123456789abcdef")
	require.NoError(t, err)
	ctx := trace.ContextWithSpanContext(
		sharedtelemetry.WithRequestContext(context.Background(), request),
		trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled}),
	)
	job := &model.TranslationJob{ID: uuid.NewString(), EntityType: "post", EntityID: uuid.NewString(), TargetLocale: "en"}
	endedAt := time.Unix(1_700_001_600, 0).UTC()
	events := captureTranslationTerminalEvents(t, func() {
		emitTranslationJobTerminal(ctx, job, translationJobTerminalOutcomeApplied, "", endedAt.Add(-time.Second), endedAt)
	})

	require.Len(t, events, 1)
	require.Equal(t, request.RequestID, events[0]["request_id"])
	require.Equal(t, traceID.String(), events[0]["trace_id"])
	require.Equal(t, spanID.String(), events[0]["span_id"])
}

func captureTranslationTerminalEvents(t *testing.T, run func()) []map[string]any {
	t.Helper()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	run()

	decoder := json.NewDecoder(&output)
	var events []map[string]any
	for decoder.More() {
		var event map[string]any
		require.NoError(t, decoder.Decode(&event))
		if event["event"] == "translation.job.terminal" {
			events = append(events, event)
		}
	}
	return events
}

func requireTranslationTerminalEvent(
	t *testing.T,
	event map[string]any,
	job *model.TranslationJob,
	outcome string,
	errorClassification string,
	durationMS int64,
) {
	t.Helper()
	require.Equal(t, "translation.job.terminal", event["event"])
	require.Equal(t, "translation", event["domain"])
	require.Equal(t, job.ID, event["job_id"])
	require.Equal(t, job.EntityType, event["entity_type"])
	require.Equal(t, job.TargetLocale, event["target_locale"])
	require.EqualValues(t, durationMS, event["duration_ms"])
	require.Equal(t, outcome, event["outcome"])
	if errorClassification == "" {
		require.NotContains(t, event, "error_classification")
	} else {
		require.Equal(t, errorClassification, event["error_classification"])
	}
	require.NotContains(t, event, "entity_id")
	require.NotContains(t, event, "provider")
	require.NotContains(t, event, "model")
	require.NotContains(t, event, "reason")
	require.NotContains(t, event, "error")
}
