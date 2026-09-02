package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/llm"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type boundedTranslationFailureError struct {
	reason string
	cause  error
}

func (e boundedTranslationFailureError) Error() string { return e.reason }
func (e boundedTranslationFailureError) Unwrap() error { return e.cause }

func boundTranslationFailure(cause error) error {
	if cause == nil {
		return nil
	}
	var bounded boundedTranslationFailureError
	if errors.As(cause, &bounded) {
		return cause
	}
	reason := classifyTranslationFailure(cause)
	if reason == "" {
		return fmt.Errorf("%s", translationFailureInternal)
	}
	return boundedTranslationFailureError{reason: reason, cause: cause}
}

const (
	translationFailureProviderConfiguration   = "provider_configuration"
	translationFailureProviderAuthentication  = "provider_authentication"
	translationFailureProviderRateLimited     = "provider_rate_limited"
	translationFailureProviderUnavailable     = "provider_unavailable"
	translationFailureProviderRejected        = "provider_rejected"
	translationFailureProviderResponseInvalid = "provider_response_invalid"
	translationFailureTargetApplyFailed       = "target_apply_failed"
	translationFailureOgHandoffFailed         = "og_handoff_failed"
	translationFailureInternal                = "internal"
)

var errTranslationTargetApplyFailed = errors.New("translation target apply failed")

func markTranslationTargetApplyFailure(cause error) error {
	if cause == nil || errors.Is(cause, errTranslationTargetApplyFailed) {
		return cause
	}
	return fmt.Errorf("%w: %w", errTranslationTargetApplyFailed, cause)
}

// classifyTranslationFailure deliberately returns a bounded public/storage
// reason. Provider bodies and arbitrary error strings never cross this edge.
func classifyTranslationFailure(cause error) string {
	if details, ok := llm.ProviderFailureDetailsFromError(cause); ok {
		switch details.Category {
		case llm.ProviderFailureConfiguration:
			return translationFailureProviderConfiguration
		case llm.ProviderFailureAuthentication:
			return translationFailureProviderAuthentication
		case llm.ProviderFailureRateLimited:
			return translationFailureProviderRateLimited
		case llm.ProviderFailureUnavailable:
			return translationFailureProviderUnavailable
		case llm.ProviderFailureRejected:
			return translationFailureProviderRejected
		case llm.ProviderFailureResponseInvalid:
			return translationFailureProviderResponseInvalid
		case llm.ProviderFailureInternal:
			return translationFailureInternal
		}
	}
	switch {
	case errors.Is(cause, errTranslationProviderUnavailable):
		return translationFailureProviderUnavailable
	case errors.Is(cause, translation.ErrProviderDocumentNotFound):
		return translationFailureProviderUnavailable
	case errors.Is(cause, context.DeadlineExceeded):
		return translationFailureProviderUnavailable
	case errors.Is(cause, translation.ErrProviderProfileUnsupported):
		return translationFailureProviderConfiguration
	case errors.Is(cause, errTranslationProviderDocumentRejected):
		return translationFailureProviderRejected
	case errors.Is(cause, translation.ErrProviderResponseInvalid):
		return translationFailureProviderResponseInvalid
	case errors.Is(cause, errTranslationOgHandoffFailed):
		return translationFailureOgHandoffFailed
	case errors.Is(cause, errTranslationTargetApplyFailed):
		return translationFailureTargetApplyFailed
	default:
		return translationFailureInternal
	}
}

func toProtoTranslationFailureReason(reason *string) managev1.TranslationFailureReason {
	if reason == nil {
		return managev1.TranslationFailureReason_TRANSLATION_FAILURE_REASON_UNSPECIFIED
	}
	switch strings.TrimSpace(*reason) {
	case translationFailureProviderConfiguration:
		return managev1.TranslationFailureReason_TRANSLATION_FAILURE_REASON_PROVIDER_CONFIGURATION
	case translationFailureProviderAuthentication:
		return managev1.TranslationFailureReason_TRANSLATION_FAILURE_REASON_PROVIDER_AUTHENTICATION
	case translationFailureProviderRateLimited:
		return managev1.TranslationFailureReason_TRANSLATION_FAILURE_REASON_PROVIDER_RATE_LIMITED
	case translationFailureProviderUnavailable:
		return managev1.TranslationFailureReason_TRANSLATION_FAILURE_REASON_PROVIDER_UNAVAILABLE
	case translationFailureProviderRejected:
		return managev1.TranslationFailureReason_TRANSLATION_FAILURE_REASON_PROVIDER_REJECTED
	case translationFailureProviderResponseInvalid:
		return managev1.TranslationFailureReason_TRANSLATION_FAILURE_REASON_PROVIDER_RESPONSE_INVALID
	case translationFailureTargetApplyFailed:
		return managev1.TranslationFailureReason_TRANSLATION_FAILURE_REASON_TARGET_APPLY_FAILED
	case translationFailureOgHandoffFailed:
		return managev1.TranslationFailureReason_TRANSLATION_FAILURE_REASON_OG_HANDOFF_FAILED
	case translationFailureInternal:
		return managev1.TranslationFailureReason_TRANSLATION_FAILURE_REASON_INTERNAL
	default:
		return managev1.TranslationFailureReason_TRANSLATION_FAILURE_REASON_INTERNAL
	}
}
