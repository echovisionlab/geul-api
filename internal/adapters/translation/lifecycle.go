package translationadapter

import (
	"context"
	"errors"
	"strings"

	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// LifecycleTransport publishes a translation lifecycle event.
type LifecycleTransport interface {
	PublishTranslationLifecycle(ctx context.Context, event *managev1.TranslationLifecycleEvent) error
}

// LifecyclePublisher announces job outcomes over transport as the event
// contract names them.
type LifecyclePublisher struct {
	transport LifecycleTransport
}

// NewLifecyclePublisher returns the job runner's lifecycle publisher over
// transport.
func NewLifecyclePublisher(transport LifecycleTransport) (LifecyclePublisher, error) {
	if transport == nil {
		return LifecyclePublisher{}, errors.New("translation lifecycle transport is required")
	}
	return LifecyclePublisher{transport: transport}, nil
}

func (p LifecyclePublisher) PublishTranslationLifecycle(ctx context.Context, event application.LifecycleEvent) error {
	definition, _ := core.DefinitionForKind(event.EntityType)
	return p.transport.PublishTranslationLifecycle(ctx, &managev1.TranslationLifecycleEvent{
		JobId:         event.JobID,
		EntityType:    definition.Proto,
		EntityId:      event.EntityID,
		TargetLocale:  event.TargetLocale,
		Status:        lifecycleStatusMessage(event.Status),
		TimestampMs:   event.Timestamp.UnixMilli(),
		FailureReason: failureReasonMessage(event.FailureReason),
	})
}

var _ application.LifecyclePublisher = LifecyclePublisher{}

func lifecycleStatusMessage(status application.LifecycleStatus) managev1.TranslationLifecycleStatus {
	switch status {
	case application.LifecycleApplied:
		return managev1.TranslationLifecycleStatus_TRANSLATION_LIFECYCLE_STATUS_APPLIED
	case application.LifecycleFailed:
		return managev1.TranslationLifecycleStatus_TRANSLATION_LIFECYCLE_STATUS_FAILED
	default:
		return managev1.TranslationLifecycleStatus_TRANSLATION_LIFECYCLE_STATUS_UNSPECIFIED
	}
}

func failureReasonMessage(reason *string) managev1.TranslationFailureReason {
	if reason == nil {
		return managev1.TranslationFailureReason_TRANSLATION_FAILURE_REASON_UNSPECIFIED
	}
	switch strings.TrimSpace(*reason) {
	case application.FailureProviderConfiguration:
		return managev1.TranslationFailureReason_TRANSLATION_FAILURE_REASON_PROVIDER_CONFIGURATION
	case application.FailureProviderAuthentication:
		return managev1.TranslationFailureReason_TRANSLATION_FAILURE_REASON_PROVIDER_AUTHENTICATION
	case application.FailureProviderRateLimited:
		return managev1.TranslationFailureReason_TRANSLATION_FAILURE_REASON_PROVIDER_RATE_LIMITED
	case application.FailureProviderUnavailable:
		return managev1.TranslationFailureReason_TRANSLATION_FAILURE_REASON_PROVIDER_UNAVAILABLE
	case application.FailureProviderRejected:
		return managev1.TranslationFailureReason_TRANSLATION_FAILURE_REASON_PROVIDER_REJECTED
	case application.FailureProviderResponseInvalid:
		return managev1.TranslationFailureReason_TRANSLATION_FAILURE_REASON_PROVIDER_RESPONSE_INVALID
	case application.FailureTargetApplyFailed:
		return managev1.TranslationFailureReason_TRANSLATION_FAILURE_REASON_TARGET_APPLY_FAILED
	case application.FailureOgHandoffFailed:
		return managev1.TranslationFailureReason_TRANSLATION_FAILURE_REASON_OG_HANDOFF_FAILED
	default:
		return managev1.TranslationFailureReason_TRANSLATION_FAILURE_REASON_INTERNAL
	}
}
