package page

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// appendPageMemberLocaleContentAudit attributes a changed locale to memberID
// in the joined transaction.
func appendPageMemberLocaleContentAudit(
	ctx context.Context,
	writer domainaudit.Appender,
	memberID string,
	pageID string,
	locale string,
	operation sharedtelemetry.AuditItemOperation,
) error {
	return domainaudit.AppendMember(
		ctx, nil, writer, memberID, sharedtelemetry.AuditPageUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPageLocaleContentAuditRecord(metadata, pageID, locale, operation)
		},
	)
}
