package application

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/llm"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBoundedTranslationFailureReasonLabelUsesLowercaseCatalogValues(t *testing.T) {
	for _, reason := range []string{
		translationFailureProviderConfiguration,
		translationFailureProviderAuthentication,
		translationFailureProviderRateLimited,
		translationFailureProviderUnavailable,
		translationFailureProviderRejected,
		translationFailureProviderResponseInvalid,
		translationFailureTargetApplyFailed,
		translationFailureOgHandoffFailed,
		translationFailureInternal,
	} {
		reason := reason
		require.Equal(t, reason, boundedTranslationFailureReasonLabel(&reason))
	}
	unknown := "TRANSLATION_FAILURE_REASON_PROVIDER_UNAVAILABLE"
	require.Equal(t, translationFailureInternal, boundedTranslationFailureReasonLabel(&unknown))
	require.Equal(t, translationFailureInternal, boundedTranslationFailureReasonLabel(nil))
}

func TestClassifyTranslationFailureMapsBoundedLLMProviderFailures(t *testing.T) {
	tests := []struct {
		category llm.ProviderFailureCategory
		want     string
	}{
		{category: llm.ProviderFailureConfiguration, want: translationFailureProviderConfiguration},
		{category: llm.ProviderFailureAuthentication, want: translationFailureProviderAuthentication},
		{category: llm.ProviderFailureRateLimited, want: translationFailureProviderRateLimited},
		{category: llm.ProviderFailureUnavailable, want: translationFailureProviderUnavailable},
		{category: llm.ProviderFailureRejected, want: translationFailureProviderRejected},
		{category: llm.ProviderFailureResponseInvalid, want: translationFailureProviderResponseInvalid},
		{category: llm.ProviderFailureInternal, want: translationFailureInternal},
	}
	for _, test := range tests {
		err := llm.NewProviderFailure(llm.ProviderFailureDetails{Category: test.category}, errors.New("sensitive provider response"))
		require.Equal(t, test.want, classifyTranslationFailure(err))
	}
}

func TestClassifyTranslationFailureMapsTargetApplyFailure(t *testing.T) {
	cause := fmt.Errorf("%w: candidate projection failed", errTranslationTargetApplyFailed)
	require.Equal(t, translationFailureTargetApplyFailed, classifyTranslationFailure(cause))
}

func TestTranslationOgHandoffMetricOutcomeUsesAuthoritativeTransactionBoundary(t *testing.T) {
	outcome, record := translationOgHandoffMetricOutcome(nil, true)
	require.True(t, record)
	require.Equal(t, "committed", outcome)

	outcome, record = translationOgHandoffMetricOutcome(errors.New("commit failed"), true)
	require.True(t, record)
	require.Equal(t, "failed", outcome)

	outcome, record = translationOgHandoffMetricOutcome(errTranslationOgHandoffFailed, false)
	require.True(t, record)
	require.Equal(t, "failed", outcome)

	_, record = translationOgHandoffMetricOutcome(errors.New("translation completion update failed"), false)
	require.False(t, record)

	commitFailure := classifyTranslationOgHandoffTransactionError(errors.New("commit failed"), true)
	require.ErrorIs(t, commitFailure, errTranslationOgHandoffFailed)
	require.Equal(t, translationFailureOgHandoffFailed, classifyTranslationFailure(commitFailure))
}

func TestFailedJobUpdatesBoundedReasonBeforeMetricProjection(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	now := time.Unix(1_700_000_500, 0).UTC()
	job := seedTranslationRetryTestJob(t, db, translationJobStatusRunning, "operation", uuid.NewString())
	manager := &TranslationJobManager{
		db: db, publisher: stubTranslationJobPublisher{},
		now: func() time.Time { return now }, metrics: newTranslationMetrics(),
	}

	require.NoError(t, manager.failJob(context.Background(), job, now.Add(-time.Second), errTranslationOgHandoffFailed))
	require.Equal(t, translationJobStatusFailed, job.Status)
	require.Equal(t, translationFailureOgHandoffFailed, translationTestPointerValue(t, job.FailureReason))
	require.Equal(t, translationFailureOgHandoffFailed, boundedTranslationFailureReasonLabel(job.FailureReason))
}
