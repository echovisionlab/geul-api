package application

import (
	"errors"
	"fmt"
	"testing"

	"github.com/echovisionlab/geul-api/internal/llm"
	"github.com/stretchr/testify/require"
)

func TestBoundedTranslationFailureReasonLabelUsesLowercaseCatalogValues(t *testing.T) {
	for _, reason := range []string{
		FailureProviderConfiguration,
		FailureProviderAuthentication,
		FailureProviderRateLimited,
		FailureProviderUnavailable,
		FailureProviderRejected,
		FailureProviderResponseInvalid,
		FailureTargetApplyFailed,
		FailureOgHandoffFailed,
		FailureInternal,
	} {
		reason := reason
		require.Equal(t, reason, boundedTranslationFailureReasonLabel(&reason))
	}
	unknown := "TRANSLATION_FAILURE_REASON_PROVIDER_UNAVAILABLE"
	require.Equal(t, FailureInternal, boundedTranslationFailureReasonLabel(&unknown))
	require.Equal(t, FailureInternal, boundedTranslationFailureReasonLabel(nil))
}

func TestClassifyTranslationFailureMapsBoundedLLMProviderFailures(t *testing.T) {
	tests := []struct {
		category llm.ProviderFailureCategory
		want     string
	}{
		{category: llm.ProviderFailureConfiguration, want: FailureProviderConfiguration},
		{category: llm.ProviderFailureAuthentication, want: FailureProviderAuthentication},
		{category: llm.ProviderFailureRateLimited, want: FailureProviderRateLimited},
		{category: llm.ProviderFailureUnavailable, want: FailureProviderUnavailable},
		{category: llm.ProviderFailureRejected, want: FailureProviderRejected},
		{category: llm.ProviderFailureResponseInvalid, want: FailureProviderResponseInvalid},
		{category: llm.ProviderFailureInternal, want: FailureInternal},
	}
	for _, test := range tests {
		err := llm.NewProviderFailure(llm.ProviderFailureDetails{Category: test.category}, errors.New("sensitive provider response"))
		require.Equal(t, test.want, classifyTranslationFailure(err))
	}
}

func TestClassifyTranslationFailureMapsTargetApplyFailure(t *testing.T) {
	cause := fmt.Errorf("%w: candidate projection failed", errTranslationTargetApplyFailed)
	require.Equal(t, FailureTargetApplyFailed, classifyTranslationFailure(cause))
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
	require.Equal(t, FailureOgHandoffFailed, classifyTranslationFailure(commitFailure))
}
