package work

import (
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func workLocaleContentRecord(workID, locale string, operation sharedtelemetry.AuditItemOperation) domainaudit.Builder {
	return func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewWorkLocaleContentAuditRecord(metadata, workID, locale, operation)
	}
}

// workLocaleContentAudit attributes a locale content change to the Member
// whose collaboration or AI edit made it.
func workLocaleContentAudit(memberID, workID, locale string, operation sharedtelemetry.AuditItemOperation) mutation.Audit {
	return mutation.MemberAudit(memberID, sharedtelemetry.AuditWorkUpdated, workLocaleContentRecord(workID, locale, operation))
}
