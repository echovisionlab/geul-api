package release

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func appendReleaseMemberTargetLocaleAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	memberID string,
	releaseID string,
	locale string,
	operation sharedtelemetry.AuditItemOperation,
) error {
	if writer == nil {
		return errors.New("release target-locale audit writer is required")
	}
	return domainaudit.AppendMember(
		ctx,
		tx,
		writer,
		memberID,
		sharedtelemetry.AuditReleaseUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewReleaseLocaleContentAuditRecord(metadata, releaseID, locale, operation)
		},
	)
}

func appendReleaseRequestTargetLocaleAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	releaseID string,
	locale string,
	operation sharedtelemetry.AuditItemOperation,
) error {
	if writer == nil {
		return errors.New("release target-locale audit writer is required")
	}
	return domainaudit.AppendRequest(
		ctx,
		tx,
		writer,
		sharedtelemetry.AuditReleaseUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewReleaseLocaleContentAuditRecord(metadata, releaseID, locale, operation)
		},
	)
}

func releaseTargetLocaleAuditOperation(create, remove, previouslyExists bool) sharedtelemetry.AuditItemOperation {
	switch {
	case remove:
		return sharedtelemetry.AuditItemOperationDeleted
	case create || !previouslyExists:
		return sharedtelemetry.AuditItemOperationCreated
	default:
		return sharedtelemetry.AuditItemOperationUpdated
	}
}
