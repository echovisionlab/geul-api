package label

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func labelLocaleContentRecord(labelID, locale string, operation sharedtelemetry.AuditItemOperation) domainaudit.Builder {
	return func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewLabelLocaleContentAuditRecord(metadata, labelID, locale, operation)
	}
}

// labelRequestLocaleAudit attributes a locale content change to the
// request's Member actor.
func labelRequestLocaleAudit(labelID, locale string, operation sharedtelemetry.AuditItemOperation) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditLabelUpdated, labelLocaleContentRecord(labelID, locale, operation))
}

// labelMemberLocaleAudit attributes a locale content change to memberID.
func labelMemberLocaleAudit(memberID, labelID, locale string, operation sharedtelemetry.AuditItemOperation) mutation.Audit {
	return mutation.MemberAudit(memberID, sharedtelemetry.AuditLabelUpdated, labelLocaleContentRecord(labelID, locale, operation))
}

// appendLabelMemberLocaleAudit appends the record of a translation delivery,
// which runs in the translation domain's transaction.
func appendLabelMemberLocaleAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	memberID string,
	labelID string,
	locale string,
	operation sharedtelemetry.AuditItemOperation,
) error {
	return domainaudit.AppendMember(ctx, tx, writer, memberID, sharedtelemetry.AuditLabelUpdated, labelLocaleContentRecord(labelID, locale, operation))
}
