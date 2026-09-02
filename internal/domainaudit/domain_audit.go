package domainaudit

import (
	"context"
	"errors"
	"time"

	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Appender persists an audit record in the caller's database transaction.
type Appender interface {
	AppendDomainAuditInTransaction(context.Context, *gorm.DB, sharedtelemetry.AuditRecord) error
}

// Builder creates an audit record from authoritative request metadata.
type Builder func(sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error)

// AppendRequest appends an audit record attributed to the authenticated member.
func AppendRequest(
	ctx context.Context,
	tx *gorm.DB,
	writer Appender,
	action sharedtelemetry.AuditAction,
	build Builder,
) error {
	if writer == nil {
		return errors.New("domain audit writer is required")
	}
	requestContext, ok := sharedtelemetry.RequestContextFrom(ctx)
	if !ok {
		apitelemetry.ReportDomainAuditAppendFailure(ctx, action, sharedtelemetry.AuditAppendFailureRequestContextMissing)
		return errors.New("domain audit requires request context")
	}
	actor, err := sharedtelemetry.ActorForRecord(requestContext.Actor)
	if err != nil || actor.Kind != sharedtelemetry.ActorKindMember {
		apitelemetry.ReportDomainAuditAppendFailure(ctx, action, sharedtelemetry.AuditAppendFailureActorInvalid)
		if err != nil {
			return err
		}
		return errors.New("request domain audit requires member actor")
	}
	return appendRecord(ctx, tx, writer, action, actor, build)
}

// AppendOptionalRequest preserves explicitly unaudited constructors while
// keeping audited services fail-closed inside their transaction.
func AppendOptionalRequest(
	ctx context.Context,
	tx *gorm.DB,
	writer Appender,
	action sharedtelemetry.AuditAction,
	build Builder,
) error {
	if writer == nil {
		return nil
	}
	return AppendRequest(ctx, tx, writer, action, build)
}

// AppendMember appends an audit record attributed to an explicitly identified member.
func AppendMember(
	ctx context.Context,
	tx *gorm.DB,
	writer Appender,
	memberID string,
	action sharedtelemetry.AuditAction,
	build Builder,
) error {
	if writer == nil {
		return errors.New("domain audit writer is required")
	}
	actor, err := sharedtelemetry.ActorForRecord(sharedtelemetry.MemberActor{MemberID: memberID})
	if err != nil {
		apitelemetry.ReportDomainAuditAppendFailure(ctx, action, sharedtelemetry.AuditAppendFailureActorInvalid)
		return err
	}
	return appendRecord(ctx, tx, writer, action, actor, build)
}

// AppendSystem appends an audit record attributed to a backend service.
func AppendSystem(
	ctx context.Context,
	tx *gorm.DB,
	writer Appender,
	serviceName sharedtelemetry.ServiceName,
	action sharedtelemetry.AuditAction,
	build Builder,
) error {
	if writer == nil {
		return errors.New("domain audit writer is required")
	}
	actor, err := sharedtelemetry.ActorForRecord(sharedtelemetry.SystemActor{ServiceName: serviceName})
	if err != nil {
		apitelemetry.ReportDomainAuditAppendFailure(ctx, action, sharedtelemetry.AuditAppendFailureActorInvalid)
		return err
	}
	return appendRecord(ctx, tx, writer, action, actor, build)
}

// AppendVersion preserves either an authenticated member or system actor for
// a version mutation originating at an internal service boundary.
func AppendVersion(
	ctx context.Context,
	tx *gorm.DB,
	writer Appender,
	action sharedtelemetry.AuditAction,
	build Builder,
) error {
	if writer == nil {
		return errors.New("domain audit writer is required")
	}
	if requestContext, ok := sharedtelemetry.RequestContextFrom(ctx); ok {
		actor, err := sharedtelemetry.ActorForRecord(requestContext.Actor)
		if err != nil {
			apitelemetry.ReportDomainAuditAppendFailure(ctx, action, sharedtelemetry.AuditAppendFailureActorInvalid)
			return err
		}
		switch actor.Kind {
		case sharedtelemetry.ActorKindMember, sharedtelemetry.ActorKindSystem:
			return appendRecord(ctx, tx, writer, action, actor, build)
		default:
			apitelemetry.ReportDomainAuditAppendFailure(ctx, action, sharedtelemetry.AuditAppendFailureActorInvalid)
			return errors.New("version domain audit requires member or system actor")
		}
	}
	apitelemetry.ReportDomainAuditAppendFailure(ctx, action, sharedtelemetry.AuditAppendFailureRequestContextMissing)
	return errors.New("version domain audit requires request context")
}

// AppendRequestTransaction runs a request audit append in its own transaction.
func AppendRequestTransaction(
	ctx context.Context,
	db *gorm.DB,
	writer Appender,
	action sharedtelemetry.AuditAction,
	build Builder,
) error {
	if db == nil {
		return errors.New("domain audit database is required")
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return AppendRequest(ctx, tx, writer, action, build)
	})
}

func appendRecord(
	ctx context.Context,
	tx *gorm.DB,
	writer Appender,
	action sharedtelemetry.AuditAction,
	actor sharedtelemetry.RecordActor,
	build Builder,
) error {
	record, err := build(sharedtelemetry.AuditMetadata{
		AuditID:     uuid.NewString(),
		OccurredAt:  time.Now().UTC(),
		Correlation: sharedtelemetry.CorrelationFromContext(ctx),
		RecordActor: actor,
	})
	if err != nil {
		apitelemetry.ReportDomainAuditAppendFailure(ctx, action, sharedtelemetry.AuditAppendFailureRecordBuildFailed)
		return err
	}
	return writer.AppendDomainAuditInTransaction(ctx, tx, record)
}
