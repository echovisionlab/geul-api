package application_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	"github.com/echovisionlab/geul-api/internal/translation/translationtest"
)

// delivery is a queued job of a seeded Post Series, ready for the runner.
type delivery struct {
	*env
	entity *translationtest.Entity
	job    application.Job
}

func newDelivery(t *testing.T) delivery {
	t.Helper()
	e := newEnv(t)
	e.seedProvider(t)
	entity := e.seedSeries(t)
	job := e.queue(t, newPrincipal(t), entity, "ko")
	return delivery{env: e, entity: entity, job: job}
}

func (d delivery) stored(t *testing.T) (application.Job, bool) {
	t.Helper()
	return d.store.StoredJob(d.job.ID)
}

func requireTerminal(t *testing.T, err error) {
	t.Helper()
	class, terminal := mq.TerminalDeliveryErrorClass(err)
	require.True(t, terminal, "error %v is not a terminal delivery", err)
	require.Equal(t, "translation_generation_failed", class)
}

func (d delivery) requireFailed(t *testing.T, reason string) {
	t.Helper()
	_, found := d.stored(t)
	require.False(t, found, "a failed job is deleted")
	events := d.lifecycle.Events()
	require.Len(t, events, 1)
	require.Equal(t, application.LifecycleFailed, events[0].Status)
	require.Equal(t, reason, *events[0].FailureReason)
	require.Empty(t, d.store.Signals())
}

func TestProcessDeliveryAppliesTheProviderResultAndDeletesTheJob(t *testing.T) {
	d := newDelivery(t)
	generator := &echoGenerator{provider: "deepl", model: "default"}
	d.runner.SetGeneratorsForTest(nil, generator)

	events := captureTerminalEvents(t, func() {
		require.NoError(t, d.runner.ProcessDelivery(t.Context(), d.job.ID))
	})

	_, found := d.stored(t)
	require.False(t, found, "an applied job is deleted")
	require.Equal(t, 1, generator.calls)
	entity, _ := d.store.Entity("series", d.entity.EntityID)
	require.Equal(t, "Night walks", *entity.Entries["ko"].Title, "the target entry holds the provider result")
	signals := d.store.Signals()
	require.Len(t, signals, 1)
	require.False(t, signals[0].SourceLocaleChanged)
	require.Equal(t, "target-"+d.job.ID, signals[0].Target.TargetRevision)
	require.Equal(t, []string{"series/" + d.entity.EntityID + "/ko series_machine_translation_applied"}, d.store.FakeDomains().OGRequests())
	lifecycle := d.lifecycle.Events()
	require.Len(t, lifecycle, 1)
	require.Equal(t, application.LifecycleApplied, lifecycle[0].Status)
	require.Nil(t, lifecycle[0].FailureReason)
	require.Len(t, events, 1)
	require.Equal(t, "applied", events[0]["outcome"])
	require.Equal(t, d.job.ID, events[0]["job_id"])
}

func TestProcessDeliveryResumesARunningJobWithItsStart(t *testing.T) {
	d := newDelivery(t)
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	running, _ := d.stored(t)
	running.Status, running.StartedAt = application.JobRunning, &startedAt
	d.store.SeedJob(running)
	d.runner.SetGeneratorsForTest(nil, &echoGenerator{provider: "deepl", model: "default"})

	require.NoError(t, d.runner.ProcessDelivery(t.Context(), d.job.ID))

	_, found := d.stored(t)
	require.False(t, found)
	require.Len(t, d.store.FakeDomains().Applied(), 1)
}

func TestProcessDeliveryKeepsTheRequestPlanAfterTheSourceChanged(t *testing.T) {
	d := newDelivery(t)
	// The source text and locale moved on after the request was queued; the
	// stored request still names the units the provider translates.
	d.store.SeedEntity(translationtest.Entity{
		EntityType: "series", EntityID: d.entity.EntityID, SourceLocale: "ja",
		DocumentID: d.entity.DocumentID, DocumentRevision: d.entity.DocumentRevision,
		Source: &translation.SourceDocument{Title: "Changed title", Summary: stringPointer("Changed summary")},
	})
	d.runner.SetGeneratorsForTest(nil, &echoGenerator{provider: "deepl", model: "default"})

	require.NoError(t, d.runner.ProcessDelivery(t.Context(), d.job.ID))

	entity, _ := d.store.Entity("series", d.entity.EntityID)
	require.Equal(t, "Night walks", *entity.Entries["ko"].Title)
}

func TestProcessDeliveryFailsARunningJobTerminallyOnAProviderFailure(t *testing.T) {
	d := newDelivery(t)
	d.runner.SetGeneratorsForTest(nil, &echoGenerator{provider: "deepl", model: "default", err: errors.New("provider down")})

	events := captureTerminalEvents(t, func() {
		requireTerminal(t, d.runner.ProcessDelivery(t.Context(), d.job.ID))
	})

	d.requireFailed(t, application.FailureInternal)
	require.Len(t, events, 1)
	require.Equal(t, "failed", events[0]["outcome"])
	require.Equal(t, application.FailureInternal, events[0]["error_classification"])
}

func TestProcessDeliveryLeavesTheJobRunningWhenTheTransportStops(t *testing.T) {
	d := newDelivery(t)
	d.runner.SetGeneratorsForTest(nil, &echoGenerator{provider: "deepl", model: "default", err: context.Canceled})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := d.runner.ProcessDelivery(ctx, d.job.ID)

	require.ErrorIs(t, err, context.Canceled)
	_, terminal := mq.TerminalDeliveryErrorClass(err)
	require.False(t, terminal)
	stored, found := d.stored(t)
	require.True(t, found, "the job waits for its redelivery")
	require.Equal(t, application.JobRunning, stored.Status)
	require.Empty(t, d.lifecycle.Events())
}

func TestProcessDeliveryFailsTheJobOnAProviderTimeout(t *testing.T) {
	d := newDelivery(t)
	d.runner.SetGeneratorsForTest(nil, &echoGenerator{provider: "deepl", model: "default", err: context.DeadlineExceeded})

	err := d.runner.ProcessDelivery(t.Context(), d.job.ID)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	requireTerminal(t, err)
	d.requireFailed(t, application.FailureProviderUnavailable)
}

func TestProcessDeliveryKeepsTheJobWhenProvidersCannotBeRead(t *testing.T) {
	for _, status := range []application.JobStatus{application.JobQueued, application.JobRunning} {
		t.Run(string(status), func(t *testing.T) {
			d := newDelivery(t)
			stored, _ := d.stored(t)
			stored.Status = status
			d.store.SeedJob(stored)
			cause := errors.New("provider configuration unavailable")
			d.runner.SetGeneratorsForTest(cause)

			err := d.runner.ProcessDelivery(t.Context(), d.job.ID)

			require.ErrorIs(t, err, cause)
			_, terminal := mq.TerminalDeliveryErrorClass(err)
			require.False(t, terminal)
			after, found := d.stored(t)
			require.True(t, found)
			require.Equal(t, status, after.Status)
		})
	}
}

func TestProcessDeliveryFailsTheJobWithoutAnActiveProvider(t *testing.T) {
	t.Run("queued", func(t *testing.T) {
		d := newDelivery(t)
		d.runner.SetGeneratorsForTest(nil)

		events := captureTerminalEvents(t, func() {
			requireTerminal(t, d.runner.ProcessDelivery(t.Context(), d.job.ID))
		})

		d.requireFailed(t, application.FailureProviderUnavailable)
		require.Len(t, events, 1)
		require.EqualValues(t, 0, events[0]["duration_ms"], "a job that never ran has no duration")
	})
	t.Run("running", func(t *testing.T) {
		d := newDelivery(t)
		stored, _ := d.stored(t)
		stored.Status = application.JobRunning
		d.store.SeedJob(stored)
		d.runner.SetGeneratorsForTest(nil)

		err := d.runner.ProcessDelivery(t.Context(), d.job.ID)

		require.ErrorIs(t, err, application.ErrProviderUnavailableForTest)
		d.requireFailed(t, application.FailureProviderUnavailable)
	})
}

func TestProcessDeliveryBoundsATargetApplyFailure(t *testing.T) {
	d := newDelivery(t)
	d.store.FakeDomains().Apply = func(application.Job, *translation.Candidate) (application.AppliedTranslationTarget, error) {
		return application.AppliedTranslationTarget{}, errors.New("sensitive target persistence detail")
	}
	d.runner.SetGeneratorsForTest(nil, &echoGenerator{provider: "deepl", model: "default"})

	err := d.runner.ProcessDelivery(t.Context(), d.job.ID)

	requireTerminal(t, err)
	require.NotContains(t, err.Error(), "sensitive target persistence detail")
	d.requireFailed(t, application.FailureTargetApplyFailed)
	require.Contains(t, d.jobUnits.Steps(), mutation.StepRollback, "the apply transaction rolled back")
}

func TestProcessDeliveryFailsTheJobOfADeletedRootWithoutApplying(t *testing.T) {
	d := newDelivery(t)
	d.store.DeleteEntity("series", d.entity.EntityID)
	d.runner.SetGeneratorsForTest(nil, &echoGenerator{provider: "deepl", model: "default"})

	requireTerminal(t, d.runner.ProcessDelivery(t.Context(), d.job.ID))

	require.Empty(t, d.store.FakeDomains().Applied())
	d.requireFailed(t, application.FailureInternal)
}

func TestProcessDeliverySkipsAJobCancelledDuringGeneration(t *testing.T) {
	d := newDelivery(t)
	generator := &echoGenerator{provider: "deepl", model: "default", during: func() {
		_, err := d.store.DeleteJob(context.Background(), d.job.ID, application.JobRunning)
		require.NoError(t, err)
	}}
	d.runner.SetGeneratorsForTest(nil, generator)

	events := captureTerminalEvents(t, func() {
		require.NoError(t, d.runner.ProcessDelivery(t.Context(), d.job.ID))
	})

	require.Empty(t, d.store.FakeDomains().Applied())
	require.Empty(t, d.store.Signals())
	require.Empty(t, d.lifecycle.Events())
	require.Empty(t, events, "the cancelling request reports the outcome")
}

func TestProcessDeliveryDoesNotReportAJobAnotherHandlerEnded(t *testing.T) {
	d := newDelivery(t)
	generator := &echoGenerator{provider: "deepl", model: "default", err: errors.New("provider down"), during: func() {
		_, err := d.store.DeleteJob(context.Background(), d.job.ID, application.JobRunning)
		require.NoError(t, err)
	}}
	d.runner.SetGeneratorsForTest(nil, generator)

	events := captureTerminalEvents(t, func() {
		requireTerminal(t, d.runner.ProcessDelivery(t.Context(), d.job.ID))
	})

	require.Empty(t, d.lifecycle.Events())
	require.Empty(t, events)
}

func TestProcessDeliveryAppliesDespiteALifecyclePublishFailure(t *testing.T) {
	d := newDelivery(t)
	d.lifecycle.err = errors.New("lifecycle notify failed")
	d.runner.SetGeneratorsForTest(nil, &echoGenerator{provider: "deepl", model: "default"})

	require.NoError(t, d.runner.ProcessDelivery(t.Context(), d.job.ID))

	_, found := d.stored(t)
	require.False(t, found)
	require.Len(t, d.store.Signals(), 1, "the transactional content signal stands")
}

func TestProcessDeliveryRollsTheApplyBackWhenTheContentSignalFails(t *testing.T) {
	d := newDelivery(t)
	d.store.FailSignals(errors.New("notify unavailable"))
	d.runner.SetGeneratorsForTest(nil, &echoGenerator{provider: "deepl", model: "default"})

	requireTerminal(t, d.runner.ProcessDelivery(t.Context(), d.job.ID))

	entity, _ := d.store.Entity("series", d.entity.EntityID)
	require.NotContains(t, entity.Entries, "ko", "the applied target rolled back with the signal")
	require.Len(t, d.store.FakeDomains().Applied(), 1)
	d.requireFailed(t, application.FailureInternal)
}

func TestProcessDeliveryNoOpTargetPublishesNothing(t *testing.T) {
	d := newDelivery(t)
	d.store.FakeDomains().Apply = func(application.Job, *translation.Candidate) (application.AppliedTranslationTarget, error) {
		return application.AppliedTranslationTarget{DocumentRevision: d.entity.DocumentRevision.String()}, nil
	}
	d.runner.SetGeneratorsForTest(nil, &echoGenerator{provider: "deepl", model: "default"})

	require.NoError(t, d.runner.ProcessDelivery(t.Context(), d.job.ID))

	require.Empty(t, d.store.Signals())
	require.Empty(t, d.store.FakeDomains().OGRequests())
	_, found := d.stored(t)
	require.False(t, found)
	require.Equal(t, application.LifecycleApplied, d.lifecycle.Events()[0].Status)
}

func TestProcessDeliveryClassifiesAnOGHandoffFailure(t *testing.T) {
	d := newDelivery(t)
	d.store.FakeDomains().OG = func(string, string, string, string) (bool, error) {
		return false, errors.New("OG planner unavailable")
	}
	d.runner.SetGeneratorsForTest(nil, &echoGenerator{provider: "deepl", model: "default"})

	requireTerminal(t, d.runner.ProcessDelivery(t.Context(), d.job.ID))

	d.requireFailed(t, application.FailureOgHandoffFailed)
}

func TestProcessDeliveryRejectsARequestArtifactThatNoLongerMatches(t *testing.T) {
	d := newDelivery(t)
	stored, _ := d.stored(t)
	stored.RequestManifest = []byte(`{"version":2}`)
	d.store.SeedJob(stored)
	generator := &echoGenerator{provider: "deepl", model: "default"}
	d.runner.SetGeneratorsForTest(nil, generator)

	requireTerminal(t, d.runner.ProcessDelivery(t.Context(), d.job.ID))

	require.Zero(t, generator.calls, "a tampered request never reaches the provider")
	d.requireFailed(t, application.FailureInternal)
}

func TestProcessDeliveryIgnoresAMissingJob(t *testing.T) {
	e := newEnv(t)
	e.runner.SetGeneratorsForTest(fmt.Errorf("providers must not be read"))

	require.NoError(t, e.runner.ProcessDelivery(t.Context(), "11111111-1111-4111-8111-111111111111"))
	require.Error(t, e.runner.ProcessDelivery(t.Context(), " "))
}

func captureTerminalEvents(t *testing.T, run func()) []map[string]any {
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
