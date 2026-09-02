package programevent

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

// The unaudited constructors remain for focused legacy tests only. Production
// registration uses the audited constructors below; every durable mutation
// appends through this transaction before it can commit.
func appendOptionalProgramEventAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	action sharedtelemetry.AuditAction,
	build domainaudit.Builder,
) error {
	if writer == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, writer, action, build)
}

func (s *ProgramEventService) appendProgramEventChildAudit(ctx context.Context, tx *gorm.DB, eventID, kind, itemID string, operation sharedtelemetry.AuditItemOperation) error {
	return appendOptionalProgramEventAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditProgramEventUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewProgramEventChildAuditRecord(metadata, eventID, kind, itemID, operation)
	})
}

func (s *ProgramEventService) appendProgramEventChildOrderAudit(ctx context.Context, tx *gorm.DB, eventID, kind string, itemIDs []string) error {
	return appendOptionalProgramEventAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditProgramEventUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewProgramEventChildOrderAuditRecord(metadata, eventID, kind, itemIDs)
	})
}

func (s *ProgramEventService) appendProgramEventPosterAudit(ctx context.Context, tx *gorm.DB, eventID, fileID string, operation sharedtelemetry.AuditCollectionOperation) error {
	return appendOptionalProgramEventAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditProgramEventUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewProgramEventPosterAuditRecord(metadata, eventID, fileID, operation)
	})
}

func appendProgramEventMemberLocaleContentAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	memberID string,
	eventID string,
	locale string,
	operation sharedtelemetry.AuditItemOperation,
) error {
	if writer == nil {
		return nil
	}
	return domainaudit.AppendMember(ctx, tx, writer, memberID, sharedtelemetry.AuditProgramEventUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewProgramEventLocaleContentAuditRecord(metadata, eventID, locale, operation)
	})
}

func programEventTargetLocaleContentOperation(create, remove, previouslyExists bool) sharedtelemetry.AuditItemOperation {
	switch {
	case remove:
		return sharedtelemetry.AuditItemOperationDeleted
	case create || !previouslyExists:
		return sharedtelemetry.AuditItemOperationCreated
	default:
		return sharedtelemetry.AuditItemOperationUpdated
	}
}
