package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

var ErrAuditRecordNotInserted = errors.New("audit record insert did not affect exactly one row")
var ErrAuditRecordIDCollision = errors.New("audit record id already belongs to a different semantic record")

// DurableWriter is the single SQL boundary for the public Domain Audit and
// Security Access contracts. Domain callers pass their owning transaction;
// Security Access uses the facade-owned database transaction boundary.
type DurableWriter struct {
	db *gorm.DB
}

// AppendDomainAudit appends an external-authority mutation record in its own
// autocommit statement. It deliberately does not claim atomicity with that
// external authority.
func (writer *DurableWriter) AppendDomainAudit(ctx context.Context, record sharedtelemetry.AuditRecord) error {
	return writer.AppendDomainAuditInTransaction(ctx, writer.db, record)
}

func NewDurableWriter(db *gorm.DB) *DurableWriter {
	return &DurableWriter{db: db}
}

func (writer *DurableWriter) AppendDomainAuditInTransaction(
	ctx context.Context,
	transaction *gorm.DB,
	record sharedtelemetry.AuditRecord,
) error {
	if err := record.Validate(); err != nil {
		ReportDomainAuditAppendFailure(ctx, record.Action, sharedtelemetry.AuditAppendFailureRecordInvalid)
		return fmt.Errorf("validate domain audit %s: %w", record.Action, err)
	}
	if transaction == nil {
		ReportDomainAuditAppendFailure(ctx, record.Action, sharedtelemetry.AuditAppendFailureTransactionMissing)
		return fmt.Errorf("append domain audit %s: transaction is required", record.Action)
	}
	persisted, err := serializeDomainAudit(record)
	if err != nil {
		ReportDomainAuditAppendFailure(ctx, record.Action, sharedtelemetry.AuditAppendFailurePersistenceFailed)
		return fmt.Errorf("serialize domain audit %s: %w", record.Action, err)
	}
	result := transaction.WithContext(ctx).Exec(`
		INSERT INTO public.domain_audit (
			audit_id, occurred_at, action,
			actor_kind, actor_member_id, actor_service,
			request_id, trace_id, span_id,
			target_type, target_id, attributes
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb)
		ON CONFLICT (audit_id) DO NOTHING
	`,
		persisted.AuditID, persisted.OccurredAt, persisted.Action,
		persisted.ActorKind, nullIfEmpty(persisted.ActorMemberID), nullIfEmpty(persisted.ActorService),
		nullIfEmpty(persisted.RequestID), nullIfEmpty(persisted.TraceID), nullIfEmpty(persisted.SpanID),
		persisted.TargetType, persisted.TargetID, persisted.Attributes,
	)
	if result.Error != nil {
		ReportDomainAuditAppendFailure(ctx, record.Action, sharedtelemetry.AuditAppendFailurePersistenceFailed)
		return fmt.Errorf("append domain audit %s: %w", record.Action, result.Error)
	}
	if result.RowsAffected == 0 {
		matches, err := domainAuditSemanticMatch(ctx, transaction, persisted)
		if err != nil {
			ReportDomainAuditAppendFailure(ctx, record.Action, sharedtelemetry.AuditAppendFailurePersistenceFailed)
			return fmt.Errorf("verify replayed domain audit %s: %w", record.Action, err)
		}
		if !matches {
			ReportDomainAuditAppendFailure(ctx, record.Action, sharedtelemetry.AuditAppendFailurePersistenceFailed)
			return fmt.Errorf("append domain audit %s: %w", record.Action, ErrAuditRecordIDCollision)
		}
	}
	return nil
}

// domainAuditSemanticMatch makes trusted producer retries idempotent by
// audit_id. Delivery time and correlation may differ across HTTP attempts; the
// Actor, action, target, and typed mutation evidence must remain identical.
func domainAuditSemanticMatch(ctx context.Context, transaction *gorm.DB, record domainAuditPersistenceRecord) (bool, error) {
	var matches bool
	err := transaction.WithContext(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM public.domain_audit
			WHERE audit_id = ?
			  AND action = ?
			  AND actor_kind = ?
			  AND actor_member_id::text IS NOT DISTINCT FROM NULLIF(?, '')
			  AND actor_service IS NOT DISTINCT FROM NULLIF(?, '')
			  AND target_type = ?
			  AND target_id = ?
			  AND attributes = ?::jsonb
		)
	`,
		record.AuditID,
		record.Action,
		record.ActorKind,
		record.ActorMemberID,
		record.ActorService,
		record.TargetType,
		record.TargetID,
		record.Attributes,
	).Scan(&matches).Error
	return matches, err
}

func (writer *DurableWriter) AppendSecurityAccess(
	ctx context.Context,
	record sharedtelemetry.SecurityAccessRecord,
) error {
	if err := record.Validate(); err != nil {
		ReportSecurityAccessAppendFailure(ctx, record.Action, sharedtelemetry.AuditAppendFailureRecordInvalid)
		return fmt.Errorf("validate security access %s: %w", record.Action, err)
	}
	if writer == nil || writer.db == nil {
		if writer != nil {
			ReportSecurityAccessAppendFailure(ctx, record.Action, sharedtelemetry.AuditAppendFailureDatabaseMissing)
		}
		return fmt.Errorf("append security access %s: database is required", record.Action)
	}

	persisted, err := serializeSecurityAccess(record)
	if err != nil {
		ReportSecurityAccessAppendFailure(ctx, record.Action, sharedtelemetry.AuditAppendFailurePersistenceFailed)
		return fmt.Errorf("serialize security access %s: %w", record.Action, err)
	}
	result := writer.db.WithContext(ctx).Exec(`
		INSERT INTO public.security_access (
			access_id, occurred_at, action,
			actor_kind, actor_member_id,
			request_id, trace_id, span_id, source_ip, attributes
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?::inet, ?::jsonb)
	`,
		persisted.AccessID, persisted.OccurredAt, persisted.Action, persisted.ActorKind,
		nullIfEmpty(persisted.ActorMemberID), persisted.RequestID, nullIfEmpty(persisted.TraceID),
		nullIfEmpty(persisted.SpanID), persisted.SourceIP, persisted.Attributes,
	)
	if err := exactInsertError(result); err != nil {
		ReportSecurityAccessAppendFailure(ctx, record.Action, sharedtelemetry.AuditAppendFailurePersistenceFailed)
		return fmt.Errorf("append security access %s: %w", record.Action, err)
	}
	return nil
}

func exactInsertError(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrAuditRecordNotInserted
	}
	return nil
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func ReportDomainAuditAppendFailure(ctx context.Context, action sharedtelemetry.AuditAction, reason sharedtelemetry.AuditAppendFailureReason) {
	record, err := sharedtelemetry.NewDomainAuditAppendFailedRecord(auditAppendFailureMetadata(ctx), action, reason)
	emitAuditAppendFailure(ctx, record, err)
}

func ReportSecurityAccessAppendFailure(ctx context.Context, action sharedtelemetry.SecurityAction, reason sharedtelemetry.AuditAppendFailureReason) {
	record, err := sharedtelemetry.NewSecurityAccessAppendFailedRecord(auditAppendFailureMetadata(ctx), action, reason)
	emitAuditAppendFailure(ctx, record, err)
}

func auditAppendFailureMetadata(ctx context.Context) sharedtelemetry.SystemMetadata {
	return sharedtelemetry.SystemMetadata{
		OccurredAt: time.Now().UTC(), Correlation: sharedtelemetry.CorrelationFromContext(ctx),
	}
}

func emitAuditAppendFailure(ctx context.Context, record sharedtelemetry.SystemRecord, err error) {
	if err != nil {
		return
	}
	_ = sharedtelemetry.EmitSystem(ctx, slog.Default().Handler(), record)
}
