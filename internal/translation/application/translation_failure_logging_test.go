package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/stretchr/testify/require"
)

func TestTranslationTerminalMappingLogsOriginalCause(t *testing.T) {
	for _, status := range []string{translationJobStatusQueued, translationJobStatusRunning} {
		t.Run(status, func(t *testing.T) {
			db := newTranslationRetryTestDB(t)
			job := seedTranslationRetryTestJob(t, db, status, "operation-1", generateUUID())
			manager := &TranslationJobManager{db: db, publisher: stubTranslationJobPublisher{}, now: time.Now, metrics: newTranslationMetrics()}
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			cause := fmt.Errorf("dial: connection refused token=private-token: %w", context.DeadlineExceeded)
			var err error
			if status == translationJobStatusQueued {
				err = manager.handleQueuedDeliveryFailure(t.Context(), job, cause)
			} else {
				err = manager.handleRunningDeliveryFailure(t.Context(), job, time.Now(), pinTranslationProviderDocumentError(cause))
			}
			require.Error(t, err)
			decoder := json.NewDecoder(&output)
			var failure map[string]any
			for decoder.More() {
				var record map[string]any
				require.NoError(t, decoder.Decode(&record))
				if record["event"] == "translation.job.failed" {
					failure = record
				}
			}
			require.NotNil(t, failure)
			require.Contains(t, failure["failure_cause"], "connection refused")
			require.NotContains(t, failure["failure_cause"], "private-token")
			require.Equal(t, job.ID, failure["job_id"])
			require.Equal(t, job.OperationID, failure["operation_id"])
			require.Equal(t, job.TargetLocale, failure["target_locale"])
			require.Equal(t, job.EntityType, failure["entity_type"])
			require.Equal(t, job.EntityID, failure["entity_id"])
			require.Equal(t, translationFailureProviderUnavailable, failure["reason"])
			require.Error(t, db.First(&model.TranslationJob{}, "id = ?", job.ID).Error)
		})
	}
}

func TestTranslationDeliveryLoadFailureReturnsCauseToQueueConsumer(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	cause := errors.New(`relation "translation_job" unavailable: connection reset token=private-token`)
	db.AddError(cause)
	manager := &TranslationJobManager{db: db}
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	err := manager.ProcessDelivery(t.Context(), "job-load")
	require.ErrorIs(t, err, cause)
	// The PGMQ consumer logs the returned failure once; its message ID is the job ID.
	require.Empty(t, output.String())
	require.Contains(t, failurecause.Message(err), `relation "translation_job" unavailable: connection reset`)
	require.NotContains(t, failurecause.Message(err), "private-token")
}
