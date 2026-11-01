package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/llm"
	"github.com/echovisionlab/geul-api/internal/translation"
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
		return fmt.Errorf("%s", FailureInternal)
	}
	return boundedTranslationFailureError{reason: reason, cause: cause}
}

// The bounded failure reasons of a failed job.
const (
	FailureProviderConfiguration   = "provider_configuration"
	FailureProviderAuthentication  = "provider_authentication"
	FailureProviderRateLimited     = "provider_rate_limited"
	FailureProviderUnavailable     = "provider_unavailable"
	FailureProviderRejected        = "provider_rejected"
	FailureProviderResponseInvalid = "provider_response_invalid"
	FailureTargetApplyFailed       = "target_apply_failed"
	FailureOgHandoffFailed         = "og_handoff_failed"
	FailureInternal                = "internal"
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
			return FailureProviderConfiguration
		case llm.ProviderFailureAuthentication:
			return FailureProviderAuthentication
		case llm.ProviderFailureRateLimited:
			return FailureProviderRateLimited
		case llm.ProviderFailureUnavailable:
			return FailureProviderUnavailable
		case llm.ProviderFailureRejected:
			return FailureProviderRejected
		case llm.ProviderFailureResponseInvalid:
			return FailureProviderResponseInvalid
		case llm.ProviderFailureInternal:
			return FailureInternal
		}
	}
	switch {
	case errors.Is(cause, errTranslationProviderUnavailable):
		return FailureProviderUnavailable
	case errors.Is(cause, translation.ErrProviderDocumentNotFound):
		return FailureProviderUnavailable
	case errors.Is(cause, context.DeadlineExceeded):
		return FailureProviderUnavailable
	case errors.Is(cause, translation.ErrProviderProfileUnsupported):
		return FailureProviderConfiguration
	case errors.Is(cause, errTranslationProviderDocumentRejected):
		return FailureProviderRejected
	case errors.Is(cause, translation.ErrProviderResponseInvalid):
		return FailureProviderResponseInvalid
	case errors.Is(cause, errTranslationOgHandoffFailed):
		return FailureOgHandoffFailed
	case errors.Is(cause, errTranslationTargetApplyFailed):
		return FailureTargetApplyFailed
	default:
		return FailureInternal
	}
}
