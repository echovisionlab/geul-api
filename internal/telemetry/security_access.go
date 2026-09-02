package telemetry

import (
	"context"
	"errors"
	"time"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

type SecurityAccessAppender interface {
	AppendSecurityAccess(context.Context, sharedtelemetry.SecurityAccessRecord) error
}

type SecurityAccessBuilder func(sharedtelemetry.SecurityAccessMetadata) (sharedtelemetry.SecurityAccessRecord, error)

// BuildAndAppendSecurityAccess owns the repeated durable Security Access
// envelope. The caller still owns the action-specific actor policy, success
// boundary, and whether an append error can change the business result.
func BuildAndAppendSecurityAccess(
	ctx context.Context,
	writer SecurityAccessAppender,
	action sharedtelemetry.SecurityAction,
	actor sharedtelemetry.RecordActor,
	occurredAt time.Time,
	build SecurityAccessBuilder,
) error {
	if writer == nil {
		return errors.New("security access writer is required")
	}
	if err := actor.Validate(); err != nil {
		ReportSecurityAccessAppendFailure(ctx, action, sharedtelemetry.AuditAppendFailureActorInvalid)
		return err
	}
	requestContext, ok := RequestContextFrom(ctx)
	if !ok || requestContext.SourceIP == "" {
		ReportSecurityAccessAppendFailure(ctx, action, sharedtelemetry.AuditAppendFailureRequestContextMissing)
		return errors.New("security access requires request context and source IP")
	}
	record, err := build(sharedtelemetry.SecurityAccessMetadata{
		AccessID:    uuid.NewString(),
		OccurredAt:  occurredAt,
		Correlation: sharedtelemetry.CorrelationFromContext(ctx),
		RecordActor: actor,
		SourceIP:    requestContext.SourceIP,
	})
	if err != nil {
		ReportSecurityAccessAppendFailure(ctx, action, sharedtelemetry.AuditAppendFailureRecordBuildFailed)
		return err
	}
	return writer.AppendSecurityAccess(ctx, record)
}
