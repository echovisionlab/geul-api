package work

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func appendWorkMemberLocaleContentAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	memberID string,
	workID string,
	locale string,
	operation sharedtelemetry.AuditItemOperation,
) error {
	return domainaudit.AppendMember(
		ctx,
		tx,
		writer,
		memberID,
		sharedtelemetry.AuditWorkUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewWorkLocaleContentAuditRecord(metadata, workID, locale, operation)
		},
	)
}

func appendWorkMemberTargetLocaleAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	memberID string,
	workID string,
	locale string,
	operation sharedtelemetry.AuditItemOperation,
) error {
	return appendWorkMemberLocaleContentAudit(ctx, tx, writer, memberID, workID, locale, operation)
}
