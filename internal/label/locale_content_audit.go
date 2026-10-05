package label

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func appendLabelRequestLocaleAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	labelID string,
	locale string,
	operation sharedtelemetry.AuditItemOperation,
) error {
	return domainaudit.AppendRequest(
		ctx,
		tx,
		writer,
		sharedtelemetry.AuditLabelUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewLabelLocaleContentAuditRecord(metadata, labelID, locale, operation)
		},
	)
}

func appendLabelMemberTargetLocaleAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	memberID string,
	labelID string,
	locale string,
	operation sharedtelemetry.AuditItemOperation,
) error {
	return appendLabelMemberLocaleAudit(ctx, tx, writer, memberID, labelID, locale, operation)
}

func appendLabelMemberLocaleAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	memberID string,
	labelID string,
	locale string,
	operation sharedtelemetry.AuditItemOperation,
) error {
	return domainaudit.AppendMember(
		ctx,
		tx,
		writer,
		memberID,
		sharedtelemetry.AuditLabelUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewLabelLocaleContentAuditRecord(metadata, labelID, locale, operation)
		},
	)
}
