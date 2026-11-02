package release

import (
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// releaseLocaleContentRecord builds the record of one locale's content
// change.
func releaseLocaleContentRecord(releaseID, locale string, operation sharedtelemetry.AuditItemOperation) domainaudit.Builder {
	return func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewReleaseLocaleContentAuditRecord(metadata, releaseID, locale, operation)
	}
}

// releaseMemberLocaleAudit attributes a locale's content change to memberID,
// for a write that has no request member actor (a translation job) or is
// attributed to its contributor (an AI document edit).
func releaseMemberLocaleAudit(memberID, releaseID, locale string, operation sharedtelemetry.AuditItemOperation) mutation.Audit {
	return mutation.MemberAudit(memberID, sharedtelemetry.AuditReleaseUpdated, releaseLocaleContentRecord(releaseID, locale, operation))
}

// releaseRequestLocaleAudit records a Collaboration edit of one existing
// locale, source or target, by the request actor.
func releaseRequestLocaleAudit(releaseID, locale string) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditReleaseUpdated, releaseLocaleContentRecord(releaseID, locale, sharedtelemetry.AuditItemOperationUpdated))
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
